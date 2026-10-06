// Package kernel runs ouro as a set of plugins.
//
// A plugin is a name and a start function, made with NewPlugin. The function
// says what the plugin depends on in its signature:
//
//	func(ctx *Context, needs N) (provides P, err error)
//
// N and P are structs with one exported field per service: the services of
// other plugins the plugin uses, and the services it offers. The kernel reads
// both before anything runs, fills N and takes over what is returned in P. A
// plugin can therefore neither use a service it did not declare nor declare
// one it does not hand over.
//
// With its Context a plugin contributes:
//
//	Add[T]  contribute an item of type T: a command, a tool, ...
//
// Whoever needs the contributions reads them with All[T] at the moment it
// uses them, not when it starts: by then every plugin has been started.
//
// Start starts every plugin exactly once, each after the plugins that provide
// what it needs. A service nobody provides, one that two plugins provide and
// plugins that need each other in a circle are found before the first plugin
// starts, and nothing is started then. Everything a plugin registers is taken
// back when it stops.
//
// The kernel knows nothing about what ouro does: T is any type. A new kind of
// extension is a new type passed to Add and All, not a change here. What must
// never become a service is named with Seal.
package kernel

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
)

// Plugin is a part of ouro that can be left out or replaced by another. It is
// made with NewPlugin.
type Plugin struct {
	Name string

	needs    []reflect.Type // the services it uses
	provides []reflect.Type // the services it offers
	start    func(ctx *Context) error
	err      error // why it was declared wrongly
}

// NewPlugin returns the plugin called name. start gets the services listed in
// N and returns the ones listed in P, see the package comment; a plugin
// without the one or the other uses struct{}. It runs once.
func NewPlugin[N, P any](name string, start func(ctx *Context, needs N) (P, error)) Plugin {
	p := Plugin{Name: name}
	var errNeeds, errProvides error
	p.needs, errNeeds = services(reflect.TypeFor[N]())
	p.provides, errProvides = services(reflect.TypeFor[P]())
	if p.err = errors.Join(errNeeds, errProvides); p.err != nil || start == nil {
		return p
	}
	p.start = func(ctx *Context) error {
		var needs N
		ctx.k.fill(reflect.ValueOf(&needs).Elem())
		provided, err := start(ctx, needs)
		if err != nil {
			return err
		}
		return ctx.provide(reflect.ValueOf(provided))
	}
	return p
}

// services are the types of the fields of the struct t: what a plugin needs
// or provides.
func services(t reflect.Type) ([]reflect.Type, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%s is not a struct: list the services as its fields", t)
	}
	var types []reflect.Type
	for i := range t.NumField() {
		f := t.Field(i)
		switch {
		case !f.IsExported():
			return nil, fmt.Errorf("field %s of %s is not exported: the kernel cannot reach it", f.Name, t)
		case slices.Contains(types, f.Type):
			return nil, fmt.Errorf("%s lists the service %s twice", t, f.Type)
		}
		types = append(types, f.Type)
	}
	return types, nil
}

// Kernel holds the services and contributions of the started plugins.
type Kernel struct {
	mu       sync.RWMutex
	services map[reflect.Type]service
	items    map[reflect.Type][]item
	sealed   map[reflect.Type]bool
	scopes   []*scope // of the started plugins, in the order they started
	nextID   int
}

// service is what a plugin provides under one type.
type service struct {
	value  reflect.Value
	plugin string // that provides it
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
}

// Context is the kernel as one plugin sees it. It stays valid after the start:
// keep it to read contributions later.
type Context struct {
	k     *Kernel
	scope *scope
}

// New returns a kernel without plugins.
func New() *Kernel {
	return &Kernel{services: map[reflect.Type]service{}, items: map[reflect.Type][]item{}, sealed: map[reflect.Type]bool{}}
}

// Context returns a context that belongs to no plugin, for whoever runs the
// kernel to read contributions.
func (k *Kernel) Context() *Context {
	return &Context{k: k, scope: &scope{name: "kernel"}}
}

// Seal keeps T from ever being a service: a plugin that needs or provides a
// type through which a T can be had stops the kernel from starting. That is
// T itself, a type with an exported field of it or a method that returns it,
// and a function or interface that is handed one.
func Seal[T any](k *Kernel) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.sealed[reflect.TypeFor[T]()] = true
}

// Service returns the service of type T, for whoever runs the kernel. A
// plugin has no kernel to ask: it declares what it needs.
func Service[T any](k *Kernel) (T, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	s, ok := k.services[reflect.TypeFor[T]()]
	if !ok {
		var zero T
		return zero, false
	}
	return s.value.Interface().(T), true
}

// Start starts the plugins, each once and after the plugins that provide what
// it needs. Plugins that cannot all be started are not started at all. If one
// fails while starting, all are stopped again.
func (k *Kernel) Start(plugins ...Plugin) error {
	order, err := k.plan(plugins)
	if err != nil {
		return err
	}
	for _, p := range order {
		s := &scope{name: p.Name}
		if err := p.start(&Context{k: k, scope: s}); err != nil {
			k.stop(s)
			k.Stop()
			return fmt.Errorf("plugin %s: %w", p.Name, err)
		}
		k.mu.Lock()
		k.scopes = append(k.scopes, s)
		k.mu.Unlock()
	}
	return nil
}

// Check reports whether plugins could be started together, and starts none:
// the error is the one Start would fail with before it started one, for a
// plugin that needs what none of them provides, a service provided twice or
// plugins that need each other in a circle. What a kernel seals is not
// asked.
func Check(plugins ...Plugin) error {
	_, err := New().plan(plugins)
	return err
}

// plan returns plugins in the order to start them in: as listed, but each
// after the plugins that provide what it needs. It fails if they cannot all
// be started, and says which plugins and services are in the way.
func (k *Kernel) plan(plugins []Plugin) ([]Plugin, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	// Who provides what: the plugins that run already, and the listed ones.
	provider := map[reflect.Type]string{}
	for t, s := range k.services {
		provider[t] = s.plugin
	}
	listed := map[string]Plugin{}
	for _, s := range k.scopes {
		listed[s.name] = Plugin{}
	}
	for _, p := range plugins {
		switch {
		case p.err != nil:
			return nil, fmt.Errorf("plugin %s: %w", p.Name, p.err)
		case p.Name == "" || p.start == nil:
			return nil, errors.New("a plugin needs a name and a start function: make it with NewPlugin")
		}
		if _, twice := listed[p.Name]; twice {
			return nil, fmt.Errorf("plugin %s is listed twice", p.Name)
		}
		listed[p.Name] = p
		for _, t := range slices.Concat(p.needs, p.provides) {
			if sealed := k.sealedIn(t, map[reflect.Type]bool{}); sealed != nil {
				return nil, fmt.Errorf("plugin %s declares the service %s, which gives access to %s: "+
					"that is part of the fixed frame of ouro and never a service, take it out of the plugin", p.Name, t, sealed)
			}
		}
		for _, t := range p.provides {
			if other, taken := provider[t]; taken {
				return nil, fmt.Errorf("%s is provided by plugin %s and by plugin %s: "+
					"a service has one provider, take one of the two out", t, other, p.Name)
			}
			provider[t] = p.Name
		}
	}
	var missing []string
	for _, p := range plugins {
		for _, t := range p.needs {
			if _, ok := provider[t]; !ok {
				missing = append(missing, fmt.Sprintf("plugin %s needs %s, which no plugin provides", p.Name, t))
			}
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s: add a plugin that provides it, or take out the plugin that needs it", strings.Join(missing, "; "))
	}

	var order []Plugin
	const visiting, done = 1, 2
	state := map[string]int{}
	// How the plugin that is visited was reached: who needs what of whom.
	type step struct{ plugin, needs string }
	var path []step
	var visit func(p Plugin) error
	visit = func(p Plugin) error {
		state[p.Name] = visiting
		for _, t := range p.needs {
			from := listed[provider[t]]
			if from.start == nil || state[from.Name] == done { // runs already, or is in order
				continue
			}
			path = append(path, step{p.Name, fmt.Sprintf("%s needs %s of %s", p.Name, t, from.Name)})
			if state[from.Name] == visiting {
				// The circle starts where from was left to get here.
				first := slices.IndexFunc(path, func(s step) bool { return s.plugin == from.Name })
				var circle []string
				for _, s := range path[first:] {
					circle = append(circle, s.needs)
				}
				return fmt.Errorf("plugins need each other in a circle, so none of them can start first: %s: "+
					"take one of these needs out", strings.Join(circle, ", "))
			}
			if err := visit(from); err != nil {
				return err
			}
			path = path[:len(path)-1]
		}
		state[p.Name] = done
		order = append(order, p)
		return nil
	}
	for _, p := range plugins {
		if state[p.Name] == done {
			continue
		}
		if err := visit(p); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// sealedIn returns the sealed type that can be had through t, or nil. seen
// are the types that were looked at already.
func (k *Kernel) sealedIn(t reflect.Type, seen map[reflect.Type]bool) reflect.Type {
	if seen[t] {
		return nil
	}
	seen[t] = true
	if k.sealed[t] {
		return t
	}
	var next []reflect.Type
	funcTypes := func(f reflect.Type) {
		for i := range f.NumIn() {
			next = append(next, f.In(i))
		}
		for i := range f.NumOut() {
			next = append(next, f.Out(i))
		}
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		next = append(next, t.Elem())
	case reflect.Map:
		next = append(next, t.Key(), t.Elem())
	case reflect.Struct:
		for i := range t.NumField() {
			if f := t.Field(i); f.IsExported() {
				next = append(next, f.Type)
			}
		}
	case reflect.Func:
		// Whoever provides a function is handed its arguments.
		funcTypes(t)
	case reflect.Interface:
		// And whoever implements an interface those of its methods.
		for i := range t.NumMethod() {
			funcTypes(t.Method(i).Type)
		}
	}
	if t.Kind() != reflect.Interface {
		// The methods of a type are its own code: only what they return counts.
		for i := range t.NumMethod() {
			m := t.Method(i).Type
			for j := range m.NumOut() {
				next = append(next, m.Out(j))
			}
		}
		if t.Kind() != reflect.Pointer {
			next = append(next, reflect.PointerTo(t)) // for the methods of the pointer
		}
	}
	for _, n := range next {
		if sealed := k.sealedIn(n, seen); sealed != nil {
			return sealed
		}
	}
	return nil
}

// fill sets every field of needs to the service of its type. plan made sure
// that there is one.
func (k *Kernel) fill(needs reflect.Value) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	for i := range needs.NumField() {
		needs.Field(i).Set(k.services[needs.Field(i).Type()].value)
	}
}

// provide takes over the services in the fields of provided, which the plugin
// of ctx offers from now on. A service that was left empty fails the plugin:
// whoever needs it would get nothing.
func (ctx *Context) provide(provided reflect.Value) error {
	k := ctx.k
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := range provided.NumField() {
		v := provided.Field(i)
		t := v.Type()
		switch t.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Func, reflect.Map, reflect.Slice, reflect.Chan:
			if v.IsNil() {
				return fmt.Errorf("declares the service %s but did not provide it", t)
			}
		}
		k.services[t] = service{value: v, plugin: ctx.scope.name}
		ctx.scope.undo = append(ctx.scope.undo, func() {
			k.mu.Lock()
			delete(k.services, t)
			k.mu.Unlock()
		})
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
