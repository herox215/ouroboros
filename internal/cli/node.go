package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"ouroboros/internal/a2a"
	"ouroboros/internal/assistant"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	"ouroboros/internal/node"
)

// nodePlugin contributes the commands that start, stop and run the local
// node. The node it runs answers peers with the agent of the assistant
// plugin and serves every a2a.Method a plugin contributed.
func nodePlugin() kernel.Plugin {
	return kernel.Plugin{Name: "node", Start: func(ctx *kernel.Context) error {
		var err error
		s := use[Settings](ctx, &err)
		t := use[Terminal](ctx, &err)
		dir := use[home.Dir](ctx, &err)
		term := use[*console](ctx, &err)
		local := use[node.Local](ctx, &err)
		builder := use[assistant.Builder](ctx, &err)
		if err != nil {
			return err
		}
		wiz := &wizard{term: term, home: dir, baseURL: s.BaseURL, apiKey: s.APIKey}
		kernel.Provide(ctx, wiz)
		c := &nodeCommands{term: term, local: local, wizard: wiz, server: node.Server{
			Home: dir, HTTPPort: s.HTTPPort, DiscoPort: s.DiscoPort, Version: Version, Log: t.Out,
			Responder: func(cfg config.Config) (a2a.Responder, error) {
				bot, err := builder.ForPeers(cfg)
				if err != nil {
					return nil, err
				}
				return bot.Respond, nil
			},
			Methods: func() []a2a.Method { return kernel.All[a2a.Method](ctx) },
		}}
		kernel.Add(ctx, command{"start", "[--setup]", "start the node (asks for endpoint, key + model on first run)", c.start})
		kernel.Add(ctx, command{"stop", "", "stop the node", c.stop})
		kernel.Add(ctx, command{"status", "", "show the local node", c.status})
		kernel.Add(ctx, command{node.ServeCommand, "", "run the node in the foreground", c.serve})
		return nil
	}}
}

type nodeCommands struct {
	term   *console
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
		fmt.Fprintf(c.term.out, "● already running (pid %d, port %d)\n", p.PID, p.Port)
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
	p, err := c.local.Start()
	if err != nil {
		return err
	}
	fmt.Fprintf(c.term.out, "● ouroboros %s (%s) online on :%d → %s\n", cfg.ID, cfg.Name, p.Port, cfg.Model)
	return nil
}

func (c *nodeCommands) stop(context.Context, []string) error {
	stopped, err := c.local.Stop()
	if err != nil {
		return err
	}
	if stopped {
		fmt.Fprintln(c.term.out, "○ stopped")
	} else {
		fmt.Fprintln(c.term.out, "○ not running")
	}
	return nil
}

func (c *nodeCommands) status(context.Context, []string) error {
	s, err := c.local.Status()
	if err != nil {
		return err
	}
	if !s.Running {
		fmt.Fprintln(c.term.out, "○ not running")
		return nil
	}
	fmt.Fprintf(c.term.out, "● running  id=%s  name=%s  port=%d  model=%s\n", s.ID, s.Name, s.Port, s.Model)
	return nil
}

// serve runs the node in the foreground until ctx is done or a signal arrives.
func (c *nodeCommands) serve(ctx context.Context, _ []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return withHint(c.server.Run(ctx))
}
