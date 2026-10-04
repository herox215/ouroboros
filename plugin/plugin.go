// Package plugin is what a plugin of ouro is written against: the one package
// a plugin imports. Everything it may see of ouro is here, and nothing of the
// fixed frame is: the identity of the node, the signatures of its requests,
// trust and contacts and the built-in A2A methods are no services, cannot be
// replaced and are not reachable through anything this package hands out.
//
// A plugin is a package of its own with one function that returns a Plugin,
// added to the list in internal/cli (Plugins):
//
//	package shout
//
//	type needs struct {
//		Console *plugin.Console // a service of another plugin
//	}
//
//	func Plugin() plugin.Plugin {
//		return plugin.New("shout", func(ctx *plugin.Context, n needs) error {
//			plugin.Add(ctx, plugin.Command{Name: "shout", Args: "TEXT", Help: "print TEXT in capitals",
//				Run: func(_ context.Context, args []string) error {
//					fmt.Fprintln(n.Console.Out, strings.ToUpper(strings.Join(args, " ")))
//					return nil
//				}})
//			return nil
//		})
//	}
//
// The struct the start function takes declares what the plugin depends on:
// one exported field per service. ouro starts every plugin once, after the
// plugins that provide what it needs, and does not start at all if a service
// is missing or plugins need each other in a circle. The services of the
// standard ouro a plugin may need:
//
//	*Console  the dialogue with the user on the terminal
//	*Network  the other nodes: Find, Peers, Resolve, Send, Call
//	LLM       the model behind the agents
//
// In its start function a plugin contributes with Add:
//
//	Command     a command of ouro
//	Tool        something the agents can do
//	Method      a JSON-RPC method for connected peers, run with Network.Call
//	PeerSource  hosts to look for nodes on
//
// and offers services of its own with NewService: a plugin that provides an
// LLM replaces the openai plugin.
package plugin

import "ouroboros/internal/kernel"

// Plugin is a part of ouro that can be left out or replaced by another. It is
// made with New or NewService.
type Plugin = kernel.Plugin

// Context is ouro as one plugin sees it, for Add and All. It stays valid
// after the start: keep it to read contributions later, and tell it with
// OnStop what to release when the plugin stops.
type Context = kernel.Context

// New returns the plugin called name. start runs once, with the services the
// plugin needs: N is a struct with an exported field for each, struct{} for a
// plugin that needs none.
func New[N any](name string, start func(ctx *Context, needs N) error) Plugin {
	if start == nil {
		return kernel.NewPlugin[N, struct{}](name, nil)
	}
	return NewService(name, func(ctx *Context, needs N) (struct{}, error) {
		return struct{}{}, start(ctx, needs)
	})
}

// NewService returns a plugin that offers services to the others: start
// returns them in P, a struct with an exported field for each. A service is
// known by its type and has one provider: a second plugin that provides the
// same type stops ouro from starting.
func NewService[N, P any](name string, start func(ctx *Context, needs N) (P, error)) Plugin {
	return kernel.NewPlugin(name, start)
}

// Add contributes value to the items of type T: a Command, a Tool, a Method,
// a PeerSource, or a type of the plugin's own that others read with All.
// It is taken back when the plugin stops.
func Add[T any](ctx *Context, value T) { kernel.Add(ctx, value) }

// All returns what the plugins contributed of type T, in the order they did.
// Read it when it is used, not in the start function: only then every plugin
// has been started.
func All[T any](ctx *Context) []T { return kernel.All[T](ctx) }
