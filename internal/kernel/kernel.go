// Package kernel runs ouro as a set of plugins.
//
// A plugin is a named Start function. In Start it does three things with the
// Context it gets:
//
//	Use[T]      get the service of type T another plugin provides
//	Provide[T]  offer a service of type T to the other plugins
//	Add[T]      contribute an item of type T: a command, a tool, ...
//
// Whoever needs the contributions reads them with All[T] at the moment it
// uses them, not in Start: by then every plugin has been started.
//
// The kernel finds the order itself. A plugin whose Use fails because the
// service is not there yet is taken back and started again after the others,
// so Start must do nothing but the calls above and building its own values.
// Everything a plugin registers is taken back when it stops.
//
// The kernel knows nothing about what ouro does: T is any type. A new kind of
// extension is a new type passed to Add and All, not a change here.
package kernel

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
)

// Plugin is a part of ouro that can be left out or replaced by another.
type Plugin struct {
	Name string
	// Start wires the plugin up. It may be called more than once, see the
	// package comment, and returns the error of a failed Use unchanged.
	Start func(ctx *Context) error
}

// MissingError is the error of Use for a service no started plugin provides.
type MissingError struct {
	Plugin  string // that asked
	Service string // type of the service
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("plugin %s needs %s, which no plugin provides", e.Plugin, e.Service)
}

// Kernel holds the services and contributions of the started plugins.
type Kernel struct {
	mu       sync.RWMutex
	services map[reflect.Type]any
	items    map[reflect.Type][]item
	scopes   []*scope // of the started plugins, in the order they started
	nextID   int
}

// item is one contribution; id tells it apart when it is taken back.
type item struct {
	id    int
	value any
}

// scope is what one plugin registered.
type scope struct {
	name string
	undo []func() // in the order of the registrations
	err  error    // a registration that failed
}

// Context is the kernel as one plugin sees it. It stays valid after Start:
// keep it to read contributions later.
type Context struct {
	k     *Kernel
	scope *scope
}

// New returns a kernel without plugins.
func New() *Kernel {
	return &Kernel{services: map[reflect.Type]any{}, items: map[reflect.Type][]item{}}
}

// Context returns a context that belongs to no plugin, for whoever runs the
// kernel to look up services and contributions.
func (k *Kernel) Context() *Context {
	return &Context{k: k, scope: &scope{name: "kernel"}}
}

// Start starts the plugins, each after the plugins that provide what it uses.
// If one fails, all are stopped again.
func (k *Kernel) Start(plugins ...Plugin) error {
	if err := k.checkNames(plugins); err != nil {
		return err
	}
	pending := plugins
	for len(pending) > 0 {
		var waiting []Plugin
		var missing []string
		for _, p := range pending {
			s := &scope{name: p.Name}
			err := errors.Join(p.Start(&Context{k: k, scope: s}), s.err)
			var need *MissingError
			switch {
			case err == nil:
				k.mu.Lock()
				k.scopes = append(k.scopes, s)
				k.mu.Unlock()
			case errors.As(err, &need):
				k.stop(s)
				waiting = append(waiting, p)
				missing = append(missing, need.Error())
			default:
				k.stop(s)
				k.Stop()
				return fmt.Errorf("plugin %s: %w", p.Name, err)
			}
		}
		if len(waiting) == len(pending) { // nobody got further
			k.Stop()
			return errors.New(strings.Join(missing, "; "))
		}
		pending = waiting
	}
	return nil
}

func (k *Kernel) checkNames(plugins []Plugin) error {
	k.mu.RLock()
	defer k.mu.RUnlock()
	seen := map[string]bool{}
	for _, s := range k.scopes {
		seen[s.name] = true
	}
	for _, p := range plugins {
		if p.Name == "" || p.Start == nil {
			return errors.New("a plugin needs a name and a Start function")
		}
		if seen[p.Name] {
			return fmt.Errorf("plugin %s is listed twice", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

// Stop stops all plugins, the last started first.
func (k *Kernel) Stop() {
	k.mu.Lock()
	scopes := k.scopes
	k.scopes = nil
	k.mu.Unlock()
	for _, s := range slices.Backward(scopes) {
		k.stop(s)
	}
}

// Plugins returns the names of the started plugins in the order they started.
func (k *Kernel) Plugins() []string {
	k.mu.RLock()
	defer k.mu.RUnlock()
	names := make([]string, len(k.scopes))
	for i, s := range k.scopes {
		names[i] = s.name
	}
	return names
}

// stop takes back what the plugin of s registered, the last first.
func (k *Kernel) stop(s *scope) {
	for _, undo := range slices.Backward(s.undo) {
		undo()
	}
	s.undo = nil
}

// OnStop makes the kernel call f when the plugin stops: the place to release
// what the plugin opened itself.
func (ctx *Context) OnStop(f func()) {
	ctx.scope.undo = append(ctx.scope.undo, f)
}

// Provide offers service to the other plugins under its type T. There is one
// service per type: a second Provide for T fails the plugin, so that no
// plugin can replace what another one offers.
func Provide[T any](ctx *Context, service T) {
	k, t := ctx.k, reflect.TypeFor[T]()
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, taken := k.services[t]; taken {
		ctx.scope.err = errors.Join(ctx.scope.err, fmt.Errorf("%s is provided already", t))
		return
	}
	k.services[t] = service
	ctx.scope.undo = append(ctx.scope.undo, func() {
		k.mu.Lock()
		delete(k.services, t)
		k.mu.Unlock()
	})
}

// Use returns the service of type T. Without one it returns a *MissingError,
// which Start hands on to be started again later.
func Use[T any](ctx *Context) (T, error) {
	k, t := ctx.k, reflect.TypeFor[T]()
	k.mu.RLock()
	defer k.mu.RUnlock()
	service, ok := k.services[t]
	if !ok {
		var zero T
		return zero, &MissingError{Plugin: ctx.scope.name, Service: t.String()}
	}
	return service.(T), nil
}

// Add contributes value to the items of type T, which All returns.
func Add[T any](ctx *Context, value T) {
	k, t := ctx.k, reflect.TypeFor[T]()
	k.mu.Lock()
	defer k.mu.Unlock()
	k.nextID++
	id := k.nextID
	k.items[t] = append(k.items[t], item{id, value})
	ctx.scope.undo = append(ctx.scope.undo, func() {
		k.mu.Lock()
		k.items[t] = slices.DeleteFunc(k.items[t], func(i item) bool { return i.id == id })
		k.mu.Unlock()
	})
}

// All returns what the started plugins contributed of type T, in the order
// they did.
func All[T any](ctx *Context) []T {
	k := ctx.k
	k.mu.RLock()
	defer k.mu.RUnlock()
	items := k.items[reflect.TypeFor[T]()]
	all := make([]T, len(items))
	for i, it := range items {
		all[i] = it.value.(T)
	}
	return all
}
