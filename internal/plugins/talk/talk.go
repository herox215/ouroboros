// Package talk is the plugin with the commands that find the nodes on the
// network and talk to them.
package talk

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"ouroboros/internal/plugins/base"
	"ouroboros/plugin"
)

// needs are the services the plugin uses.
type needs struct {
	Console   *plugin.Console
	Network   *plugin.Network
	Assistant *plugin.Assistant
}

// Plugin contributes the commands that find the nodes on the network and
// talk to them: directly, or through the assistant of this node.
func Plugin() plugin.Plugin {
	return plugin.New("talk", func(ctx *plugin.Context, n needs) error {
		c := &talkCommands{ctx: ctx, term: n.Console, network: n.Network, assistant: n.Assistant}
		plugin.Add(ctx, plugin.Command{Name: "list", Help: "find ouroboros nodes on the network", Run: c.list})
		plugin.Add(ctx, plugin.Command{Name: "chat", Args: `["msg"]`, Help: "ask your agent, which can ask the other nodes (interactive without msg)", Run: c.chat})
		plugin.Add(ctx, plugin.Command{Name: "ask", Args: `ID ["msg"]`, Help: "write to a node directly (interactive without msg)", Run: c.ask})
		return nil
	})
}

type talkCommands struct {
	ctx       *plugin.Context // to find the front end
	term      *plugin.Console
	network   *plugin.Network
	assistant *plugin.Assistant
}

func (c *talkCommands) list(ctx context.Context, _ []string) error {
	nodes, err := c.network.Find(ctx)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		fmt.Fprintln(c.term.Out, "no ouroboros nodes found")
		return nil
	}
	tw := tabwriter.NewWriter(c.term.Out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tMODEL\tADDRESS")
	for _, n := range nodes {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", n.ID, n.Name, n.Model, n.Addr())
	}
	return tw.Flush()
}

// ask writes to a node directly, without the assistant in between.
func (c *talkCommands) ask(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(`usage: ouro ask ID ["message"]`)
	}
	node, err := c.network.Resolve(ctx, args[0])
	if err != nil {
		return err
	}
	contextID := plugin.NewID()
	send := func(text string) (string, error) { return c.network.Send(ctx, node, contextID, text) }

	if len(args) > 1 {
		reply, err := send(strings.Join(args[1:], " "))
		if errors.Is(err, plugin.ErrPending) {
			return fmt.Errorf("connection request sent to %s, waiting for approval", plugin.Label(node))
		}
		return c.print(reply, err)
	}
	return c.term.Converse("chatting with "+node.Addr(), func(line string) (string, error) {
		reply, err := send(line)
		if errors.Is(err, plugin.ErrPending) {
			return "connection request sent — waiting for approval", nil
		}
		return reply, err
	})
}

// chat talks to the assistant: once with the given message, otherwise in the
// front end a plugin contributed, which is what ouro without a command opens,
// or line by line if there is none or no terminal.
func (c *talkCommands) chat(ctx context.Context, args []string) error {
	if len(args) == 0 {
		front, err := plugin.FrontEndFor(c.ctx, c.term)
		if !errors.Is(err, plugin.ErrNoFrontEnd) {
			if err != nil {
				return err
			}
			return front.Run(ctx)
		}
	}
	if err := base.WithHint(c.assistant.Ready()); err != nil {
		return err
	}
	contextID := plugin.NewID()
	answer := func(text string) (string, error) { return c.assistant.Ask(ctx, contextID, text, nil) }
	if len(args) > 0 {
		return c.print(answer(strings.Join(args, " ")))
	}
	return c.term.Converse("chatting with your agent", answer)
}

func (c *talkCommands) print(reply string, err error) error {
	if err != nil {
		return err
	}
	fmt.Fprintln(c.term.Out, reply)
	return nil
}
