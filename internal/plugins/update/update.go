// Package update is the plugin with the command that updates ouro itself.
package update

import (
	"context"
	"fmt"

	"ouroboros/internal/home"
	"ouroboros/internal/node"
	"ouroboros/internal/update"
	"ouroboros/plugin"
)

// needs are the services the plugin uses.
type needs struct {
	Console *plugin.Console
}

// services are what the plugin provides.
type services struct {
	Updater *update.Updater
}

// Plugin contributes the command that updates ouro itself and restarts the
// node in dir if it is running.
func Plugin(dir home.Dir) plugin.Plugin {
	return plugin.NewService("update", func(ctx *plugin.Context, n needs) (services, error) {
		c := &updateCommand{
			term:    n.Console,
			local:   node.Local{Home: dir},
			updater: &update.Updater{Repo: update.Repo, Dir: dir.UpdateDir(), Revision: update.Revision()},
		}
		plugin.Add(ctx, plugin.Command{Name: "update", Help: "build and install the newest version", Run: c.run})
		return services{c.updater}, nil
	})
}

// updateCommand installs the newest version over the running binary and
// restarts the node if it is running.
type updateCommand struct {
	term    *plugin.Console
	updater *update.Updater
	local   node.Local
}

func (c *updateCommand) run(ctx context.Context, _ []string) (err error) {
	u := *c.updater
	u.Out = c.term.Out
	if u.Exe == "" {
		// Resolved before the update: afterwards the path of a replaced binary is gone.
		if u.Exe, err = c.local.Executable(); err != nil {
			return err
		}
	}
	rev, updated, err := u.Run(ctx)
	if err != nil {
		return err
	}
	if !updated {
		fmt.Fprintf(c.term.Out, "● already up to date (%s)\n", update.Short(rev))
		return nil
	}
	if err := c.restart(u.Exe); err != nil {
		return err
	}
	fmt.Fprintf(c.term.Out, "● updated to %s\n", update.Short(rev))
	return nil
}

// restart replaces a running node by one that runs exe.
func (c *updateCommand) restart(exe string) error {
	local := c.local
	local.Exe = func() (string, error) { return exe, nil }
	if running, err := local.Stop(); err != nil || !running {
		return err
	}
	_, err := local.Start()
	return err
}
