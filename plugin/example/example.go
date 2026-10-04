// Package example is a complete plugin, to copy for a new one: it lets a node
// greet the nodes it is connected to. It adds a method to the node, which
// connected peers can run, and three ways to run that method on a peer: a
// command, a tool for the agents and a screen in the terminal UI.
//
// Like every plugin it imports ouroboros/plugin and nothing else of ouro. It
// is not part of the standard ouro: to try it, add example.Plugin() to the
// list in internal/cli (Plugins), on both nodes.
package example

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"ouroboros/plugin"
)

// Method is the JSON-RPC method the plugin adds. The name of the plugin in
// front keeps it apart from the methods of others.
const Method = "example/greet"

// Tool is the tool with which an agent greets a peer.
const Tool = "greet_peer"

// greeting are the params of Method.
type greeting struct {
	From string `json:"from"` // who greets
}

// toolArgs are the arguments of Tool; the JSON names are those of its params.
type toolArgs struct {
	Peer string `json:"peer"`
	From string `json:"from"`
}

// needs are the services the plugin uses: ouro starts it after the plugins
// that provide them.
type needs struct {
	Console *plugin.Console
	Network *plugin.Network
}

// Plugin adds the method "example/greet", the command "ouro greet", the tool
// "greet_peer" and the screen "Greetings" of the terminal UI.
func Plugin() plugin.Plugin {
	return plugin.New("example", func(ctx *plugin.Context, n needs) error {
		g := &greeter{network: n.Network, answers: map[string]string{}}
		plugin.Add(ctx, plugin.Method{Name: Method, Handle: greet})
		plugin.Add(ctx, plugin.Command{Name: "greet", Args: "NODE NAME", Help: "greet a connected node as NAME", Run: func(c context.Context, args []string) error {
			if len(args) != 2 {
				return errors.New("usage: ouro greet NODE NAME")
			}
			reply, err := g.greet(c, args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Fprintln(n.Console.Out, reply)
			return nil
		}})

		// For the agents: the tool, and what the owner is shown while the
		// agent waits for its result.
		plugin.Add(ctx, plugin.NewTool(Tool, "Greet a connected peer and return its answer.",
			[]plugin.Param{{Name: "peer", Description: "name or ID of the peer"}, {Name: "from", Description: "who greets"}},
			func(c context.Context, args toolArgs) (string, error) { return g.greet(c, args.Peer, args.From) }))
		plugin.Add(ctx, plugin.ToolView{Tool: Tool, Show: func(args json.RawMessage) string {
			var call toolArgs
			if json.Unmarshal(args, &call) != nil || call.Peer == "" {
				return ""
			}
			return "greeting " + call.Peer
		}})

		// For the terminal UI: g opens the screen, on which a greets everyone.
		plugin.Add(ctx, plugin.Screen{Key: "g", Title: "Greetings", Body: g.screen,
			Keys: []plugin.Key{{Key: "a", Help: "greet all", Run: g.greetAll}}})
		return nil
	})
}

// greeter greets peers and keeps what they answered for the screen.
type greeter struct {
	network *plugin.Network

	mu      sync.Mutex        // the screen is read while a key runs
	answers map[string]string // by the ID of the peer
}

// greet runs Method on the node target as from and returns its answer.
func (g *greeter) greet(ctx context.Context, target, from string) (string, error) {
	node, err := g.network.Resolve(ctx, target)
	if err != nil {
		return "", err
	}
	// Signed as this node, for that node and this method, and sent only to
	// the holder of its key. A node that is not connected is not called.
	reply, err := g.network.Call(ctx, node, Method, greeting{From: from})
	if err != nil {
		return "", err
	}
	return reply.Text(), nil
}

// connected returns the peers that can be greeted.
func (g *greeter) connected(ctx context.Context) ([]plugin.Peer, error) {
	peers, err := g.network.Peers(ctx)
	var connected []plugin.Peer
	for _, p := range peers {
		if p.State == plugin.Connected {
			connected = append(connected, p)
		}
	}
	return connected, err
}

// screen is what the screen shows: the connected peers and their answers.
func (g *greeter) screen(ctx context.Context) (string, error) {
	peers, err := g.connected(ctx)
	if err != nil {
		return "", err
	}
	if len(peers) == 0 {
		return "No connected peers to greet.", nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	lines := make([]string, len(peers))
	for i, p := range peers {
		lines[i] = plugin.Label(p.Node)
		if answer, ok := g.answers[p.ID]; ok {
			lines[i] += ": " + answer
		}
	}
	return strings.Join(lines, "\n"), nil
}

// greetAll is the key of the screen: it greets every connected peer and
// returns what the footer says about it.
func (g *greeter) greetAll(ctx context.Context) (string, error) {
	peers, err := g.connected(ctx)
	if err != nil {
		return "", err
	}
	for _, p := range peers {
		answer := "no answer"
		if reply, err := g.network.Call(ctx, p.Node, Method, greeting{From: "the terminal UI"}); err == nil {
			answer = reply.Text()
		}
		g.mu.Lock()
		g.answers[p.ID] = answer
		g.mu.Unlock()
	}
	return fmt.Sprintf("greeted %d peers", len(peers)), nil
}

// greet answers a greeting. It only runs for a peer the owner of this node is
// connected to, whose request was signed for this node: ouro has checked
// both, and from is who signed.
func greet(_ context.Context, from plugin.Caller, params json.RawMessage) (*plugin.Message, *plugin.Error) {
	var g greeting
	if json.Unmarshal(params, &g) != nil || g.From == "" {
		return nil, &plugin.Error{Code: plugin.CodeInvalidParams, Message: `needs "from"`}
	}
	return plugin.Reply(fmt.Sprintf("hello %s, I know you by the key %.8s", g.From, from.PublicKey)), nil
}
