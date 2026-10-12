// Package talk is the plugin with the commands that find the nodes on the
// network and talk to them.
package talk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"ouroboros/internal/plugins/base"
	"ouroboros/plugin"
)

// needs are the services the plugin uses.
type needs struct {
	Console  *plugin.Console
	Network  *plugin.Network
	Sessions plugin.Sessions
}

// Plugin contributes the commands that find the nodes on the network and
// talk to them: directly, or through the agent of the owner of this node,
// in a session of the node.
func Plugin() plugin.Plugin {
	return plugin.New("talk", func(ctx *plugin.Context, n needs) error {
		c := &talkCommands{ctx: ctx, term: n.Console, network: n.Network, sessions: n.Sessions}
		plugin.Add(ctx, plugin.Command{Name: "list", Help: "find ouroboros nodes on the network", Run: c.list})
		plugin.Add(ctx, plugin.Command{Name: "chat", Args: `["msg"]`, Help: "ask your agent, which can ask the other nodes (interactive without msg)", Run: c.chat})
		plugin.Add(ctx, plugin.Command{Name: "ask", Args: `ID ["msg"]`, Help: "write to a node directly (interactive without msg)", Run: c.ask})
		return nil
	})
}

type talkCommands struct {
	ctx      *plugin.Context // to find the front end
	term     *plugin.Console
	network  *plugin.Network
	sessions plugin.Sessions
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
		text := strings.Join(args[1:], " ")
		if strings.TrimSpace(text) == "" {
			return errors.New("the message is empty") // a peer is not asked for nothing
		}
		reply, err := send(text)
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

// interrupted returns ctx, ended by Ctrl-C, a request to terminate the
// process or the terminal closing under it: the chat then lets go of the
// node it started rather than dying with it running; tests replace it.
var interrupted = func(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
}

// chat talks to the agent of the owner: once with the given message,
// otherwise in the front end a plugin contributed, which is what ouro without
// a command opens, or line by line if there is none or no terminal.
//
// The agent answers in a session of the node, which is started for the chat
// if it does not run, and stopped again afterwards once no answer keeps it
// running.
func (c *talkCommands) chat(ctx context.Context, args []string) (err error) {
	if len(args) == 0 {
		front, err := plugin.FrontEndFor(c.ctx, c.term)
		if !errors.Is(err, plugin.ErrNoFrontEnd) {
			if err != nil {
				return err
			}
			return front.Run(ctx)
		}
	}
	started, err := c.sessions.Open(ctx)
	if err != nil {
		return base.WithHint(err)
	}
	if started {
		defer func() { err = errors.Join(err, c.sessions.Release(context.WithoutCancel(ctx))) }()
	}
	id := plugin.NewID() // the session of this chat
	answer := func(text string) (string, error) {
		// Ctrl-C stops the answer, and the log says what it was waiting for.
		ctx, stop := interrupted(ctx)
		defer stop()
		return plugin.Await(ctx, c.sessions, id, text)
	}
	if len(args) > 0 {
		return c.print(answer(strings.Join(args, " ")))
	}
	return c.term.Converse("chatting with your agent", answer)
}

func (c *talkCommands) print(reply string, err error) error {
	if errors.Is(err, context.Canceled) {
		return errStopped
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(c.term.Out, reply)
	return nil
}

// errStopped is the error of an answer that was stopped, as by Ctrl-C: it
// says so in words, and is context.Canceled.
var errStopped error = stopped{}

type stopped struct{}

func (stopped) Error() string { return "the answer was stopped" }
func (stopped) Unwrap() error { return context.Canceled }
