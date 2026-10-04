package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"ouroboros/internal/a2a"
	"ouroboros/internal/assistant"
	"ouroboros/internal/kernel"
	"ouroboros/internal/peers"
)

// talkPlugin contributes the commands that find the nodes on the network and
// talk to them: directly, or through the assistant of this node.
func talkPlugin() kernel.Plugin {
	return kernel.Plugin{Name: "talk", Start: func(ctx *kernel.Context) error {
		var err error
		c := &talkCommands{
			term:      use[*console](ctx, &err),
			network:   use[*peers.Network](ctx, &err),
			assistant: use[*assistant.Assistant](ctx, &err),
			dispatch:  use[*dispatcher](ctx, &err),
		}
		if err != nil {
			return err
		}
		kernel.Add(ctx, command{"list", "", "find ouroboros nodes on the network", c.list})
		kernel.Add(ctx, command{"chat", `["msg"]`, "ask your agent, which can ask the other nodes (terminal UI without msg)", c.chat})
		kernel.Add(ctx, command{"ask", `ID ["msg"]`, "write to a node directly (interactive without msg)", c.ask})
		return nil
	}}
}

type talkCommands struct {
	term      *console
	network   *peers.Network
	assistant *assistant.Assistant
	dispatch  *dispatcher
}

func (c *talkCommands) list(ctx context.Context, _ []string) error {
	nodes, err := c.network.Find(ctx)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		fmt.Fprintln(c.term.out, "no ouroboros nodes found")
		return nil
	}
	tw := tabwriter.NewWriter(c.term.out, 0, 0, 3, ' ', 0)
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
	contextID := a2a.NewID()
	send := func(text string) (string, error) { return c.network.Send(ctx, node, contextID, text) }

	if len(args) > 1 {
		reply, err := send(strings.Join(args[1:], " "))
		if errors.Is(err, peers.ErrPending) {
			return fmt.Errorf("connection request sent to %s, waiting for approval", peers.Label(node))
		}
		return c.print(reply, err)
	}
	return c.term.converse("chatting with "+node.Addr(), func(line string) (string, error) {
		reply, err := send(line)
		if errors.Is(err, peers.ErrPending) {
			return "connection request sent — waiting for approval", nil
		}
		return reply, err
	})
}

// chat talks to the assistant: once with the given message, otherwise in the
// terminal UI or, without a terminal, line by line.
func (c *talkCommands) chat(ctx context.Context, args []string) error {
	if _, ui := c.dispatch.find(defaultCommand); ui && len(args) == 0 && c.dispatch.interactive {
		return c.dispatch.run(ctx, []string{defaultCommand})
	}
	if err := withHint(c.assistant.Ready()); err != nil {
		return err
	}
	contextID := a2a.NewID()
	answer := func(text string) (string, error) { return c.assistant.Ask(ctx, contextID, text, nil) }
	if len(args) > 0 {
		return c.print(answer(strings.Join(args, " ")))
	}
	return c.term.converse("chatting with your agent", answer)
}

func (c *talkCommands) print(reply string, err error) error {
	if err != nil {
		return err
	}
	fmt.Fprintln(c.term.out, reply)
	return nil
}
