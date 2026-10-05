// Package node is the plugin with the commands that start, stop and run the
// local node, and the setup dialog that configures it.
package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"ouroboros/internal/a2a"
	"ouroboros/internal/assistant"
	"ouroboros/internal/home"
	"ouroboros/internal/node"
	"ouroboros/internal/plugins/base"
	"ouroboros/internal/setup"
	"ouroboros/plugin"
)

// Options are how the node runs.
type Options struct {
	HTTPPort  int       // A2A port; falls back to a free port if taken
	DiscoPort int       // UDP discovery port
	Version   string    // of ouro, as told in the agent card
	Log       io.Writer // of the node that runs in the foreground
	BaseURL   string    // default LLM endpoint offered during setup
	APIKey    string    // key for BaseURL offered during setup
	OnStop    func()    // called when the node in the foreground stops; may be nil
}

// needs are the services the plugin uses.
type needs struct {
	Console *plugin.Console
	Builder assistant.Builder
}

// Plugin contributes the commands that start, stop and run the node in dir.
// The node it runs answers peers with the agent of the assistant plugin,
// serves every plugin.Method a plugin contributed and opens every
// plugin.ChannelHandler.
//
// Who the node is and whom it trusts is read from dir by the node itself (see
// node.Server): no plugin provides it, and none can put something in its
// place.
func Plugin(dir home.Dir, o Options) plugin.Plugin {
	return plugin.New("node", func(ctx *plugin.Context, n needs) error {
		c := &nodeCommands{
			term:   n.Console,
			local:  node.Local{Home: dir},
			wizard: &wizard{term: n.Console, home: dir, offer: setup.Offer{BaseURL: o.BaseURL, APIKey: o.APIKey}},
			server: node.Server{
				Home: dir, HTTPPort: o.HTTPPort, DiscoPort: o.DiscoPort, Version: o.Version, Log: dir.LogTo(o.Log),
				Responder: func(logf func(format string, args ...any)) (a2a.Responder, error) {
					// The agent for the configuration as it is with every
					// message: what the owner changes holds without a restart.
					peers := n.Builder.Peers(dir.Config, logf)
					if err := peers.Ready(); err != nil {
						return nil, err
					}
					return func(ctx context.Context, contextID, text string) (string, error) {
						return peers.Respond(ctx, conversation(ctx, contextID), text)
					}, nil
				},
				Methods:  func() []a2a.Method { return plugin.All[plugin.Method](ctx) },
				Channels: func() []a2a.ChannelHandler { return plugin.All[plugin.ChannelHandler](ctx) },
				OnStop:   o.OnStop,
			},
		}
		plugin.Add(ctx, plugin.Command{Name: "start", Args: "[--setup]", Help: "start the node (asks for endpoint, key + model on first run)", Run: c.start})
		plugin.Add(ctx, plugin.Command{Name: "stop", Help: "stop the node", Run: c.stop})
		plugin.Add(ctx, plugin.Command{Name: "status", Help: "show the local node", Run: c.status})
		plugin.Add(ctx, plugin.Command{Name: node.ServeCommand, Help: "run the node in the foreground", Run: c.serve})
		return nil
	})
}

// conversation is what the agent remembers the conversation contextID of the
// peer it answers by. The peer picks contextID, so that alone would let it
// continue, and read, what another peer said: a conversation is one of the
// peer whose key signed the message.
func conversation(ctx context.Context, contextID string) string {
	origin, _ := a2a.OriginFrom(ctx)
	sum := sha256.Sum256([]byte(origin.PublicKey + "\n" + contextID))
	return hex.EncodeToString(sum[:])
}

type nodeCommands struct {
	term   *plugin.Console
	local  node.Local
	wizard *wizard
	server node.Server
}

func (c *nodeCommands) start(ctx context.Context, args []string) error {
	setup := false
	for _, arg := range args {
		if arg != "--setup" {
			return fmt.Errorf("unknown flag %q", arg)
		}
		setup = true
	}
	if p, ok := c.local.Running(); ok {
		fmt.Fprintf(c.term.Out, "● already running (pid %d, port %d)\n", p.PID, p.Port)
		return nil
	}
	cfg, err := c.local.Home.Config()
	if err != nil {
		return err
	}
	if setup || !cfg.Complete() {
		if cfg, err = c.wizard.run(ctx, cfg); err != nil {
			return err
		}
	}
	// A configuration from before nodes had keys gets its key, and its ID, here.
	id, err := c.local.Home.Identity()
	if err != nil {
		return err
	}
	p, err := c.local.Start()
	if err != nil {
		return err
	}
	fmt.Fprintf(c.term.Out, "● ouroboros %s (%s) online on :%d → %s\n", id.ID(), cfg.Name, p.Port, cfg.Model)
	return nil
}

func (c *nodeCommands) stop(context.Context, []string) error {
	stopped, err := c.local.Stop()
	if err != nil {
		return err
	}
	if stopped {
		fmt.Fprintln(c.term.Out, "○ stopped")
	} else {
		fmt.Fprintln(c.term.Out, "○ not running")
	}
	return nil
}

func (c *nodeCommands) status(context.Context, []string) error {
	s, err := c.local.Status()
	if err != nil {
		return err
	}
	if !s.Running {
		fmt.Fprintln(c.term.Out, "○ not running")
		return nil
	}
	fmt.Fprintf(c.term.Out, "● running  id=%s  name=%s  port=%d  model=%s\n", s.ID, s.Name, s.Port, s.Model)
	return nil
}

// serve runs the node in the foreground until ctx is done or a signal arrives.
func (c *nodeCommands) serve(ctx context.Context, _ []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return base.WithHint(c.server.Run(ctx))
}
