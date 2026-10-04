package cli

import (
	"context"
	"fmt"

	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	"ouroboros/internal/node"
	"ouroboros/internal/update"
)

// updatePlugin contributes the command that updates ouro itself.
func updatePlugin() kernel.Plugin {
	return kernel.Plugin{Name: "update", Start: func(ctx *kernel.Context) error {
		var err error
		dir := use[home.Dir](ctx, &err)
		c := &updateCommand{term: use[*console](ctx, &err), local: use[node.Local](ctx, &err)}
		if err != nil {
			return err
		}
		c.updater = &update.Updater{Repo: update.Repo, Dir: dir.UpdateDir(), Revision: update.Revision()}
		kernel.Provide(ctx, c.updater)
		kernel.Add(ctx, command{"update", "", "build and install the newest version", c.run})
		return nil
	}}
}

// updateCommand installs the newest version over the running binary and
// restarts the node if it is running.
type updateCommand struct {
	term    *console
	updater *update.Updater
	local   node.Local
}

func (c *updateCommand) run(ctx context.Context, _ []string) (err error) {
	u := *c.updater
	u.Out = c.term.out
	if u.Exe == "" {
		// Resolved before the update: afterwards the path of a replaced binary is gone.
		if u.Exe, err = node.Executable(); err != nil {
			return err
		}
	}
	rev, updated, err := u.Run(ctx)
	if err != nil {
		return err
	}
	if !updated {
		fmt.Fprintf(c.term.out, "● already up to date (%s)\n", update.Short(rev))
		return nil
	}
	if err := c.restart(u.Exe); err != nil {
		return err
	}
	fmt.Fprintf(c.term.out, "● updated to %s\n", update.Short(rev))
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
