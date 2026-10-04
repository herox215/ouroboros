// Package example is a complete plugin, to copy for a new one: it lets a node
// greet the nodes it is connected to. It adds a method to the node, which
// connected peers can run, and a command that runs that method on a peer.
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

	"ouroboros/plugin"
)

// Method is the JSON-RPC method the plugin adds. The name of the plugin in
// front keeps it apart from the methods of others.
const Method = "example/greet"

// greeting are the params of Method.
type greeting struct {
	From string `json:"from"` // who greets
}

// needs are the services the plugin uses: ouro starts it after the plugins
// that provide them.
type needs struct {
	Console *plugin.Console
	Network *plugin.Network
}

// Plugin adds the method "example/greet" and the command "ouro greet".
func Plugin() plugin.Plugin {
	return plugin.New("example", func(ctx *plugin.Context, n needs) error {
		plugin.Add(ctx, plugin.Method{Name: Method, Handle: greet})
		plugin.Add(ctx, plugin.Command{Name: "greet", Args: "NODE NAME", Help: "greet a connected node as NAME", Run: func(c context.Context, args []string) error {
			if len(args) != 2 {
				return errors.New("usage: ouro greet NODE NAME")
			}
			node, err := n.Network.Resolve(c, args[0])
			if err != nil {
				return err
			}
			// Signed as this node, for that node and this method, and sent
			// only to the holder of its key. A node that is not connected is
			// not called.
			reply, err := n.Network.Call(c, node, Method, greeting{From: args[1]})
			if err != nil {
				return err
			}
			fmt.Fprintln(n.Console.Out, reply.Text())
			return nil
		}})
		return nil
	})
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
