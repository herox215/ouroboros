// Package update is the plugin with the command that updates ouro itself and
// the tool with which the agent of the owner builds its own source.
package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"ouroboros/internal/assistant"
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

// Rebuild is the name of the tool that builds the source of ouro.
const Rebuild = "rebuild"

// maxOutput is how much of the output of a failed build or test the agent
// gets: its end, where the failure is.
const maxOutput = 8 << 10

// Plugin contributes the command that updates ouro itself and the tool that
// builds its source. Both restart the node in dir if it is running.
func Plugin(dir home.Dir) plugin.Plugin {
	return plugin.NewService("update", func(ctx *plugin.Context, n needs) (services, error) {
		c := &updateCommand{
			term:    n.Console,
			local:   node.Local{Home: dir},
			updater: &update.Updater{Repo: update.Repo, Source: update.Source, Dir: dir.UpdateDir(), Revision: update.Revision()},
		}
		plugin.Add(ctx, plugin.Command{Name: "update", Help: "build and install the newest version", Run: c.run})
		plugin.Add(ctx, plugin.NewTool(Rebuild,
			"Build ouro, the program you are part of, from its source and install it: this is how a change of that source takes effect. "+
				"The source is the Go module in "+sourceName(update.Source)+", built with its files as they are now. "+
				"Its tests are run first (make test), which takes a minute or more. "+
				"Only when the build and every test pass, the running binary is replaced and a running node restarted. "+
				"When one fails nothing is installed, and the error carries the end of the output: fix the source and call the tool again. "+
				"In the terminal UI, ouro starts anew with the new build once your answer is given, and this conversation goes on there; "+
				"elsewhere it goes on with the old build until ouro is started again. Only the owner of the node can use the tool.",
			nil, c.rebuild))
		plugin.Add(ctx, plugin.ToolView{Tool: Rebuild, Show: func(json.RawMessage) string {
			return "building and testing " + sourceName(update.Source)
		}, Detail: assistant.HowItEnded})
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

// sourceName names the checkout source to the agent and the owner.
func sourceName(source string) string {
	if source == "" {
		return "a checkout this build does not know"
	}
	return source
}

// rebuild installs a build of the source over the running binary, once its
// tests pass, and restarts the node if it is running. A peer never gets
// that far, whatever the configuration allows it.
func (c *updateCommand) rebuild(ctx context.Context, _ struct{}) (string, error) {
	if _, peer := plugin.OriginFrom(ctx); peer {
		return "", errors.New(Rebuild + " is for the owner of the node only")
	}
	var out bytes.Buffer
	u := *c.updater
	u.Out = &out
	if u.Exe == "" {
		exe, err := c.local.Executable()
		if err != nil {
			return "", err
		}
		u.Exe = exe
	}
	if err := u.Rebuild(ctx); err != nil {
		if out.Len() == 0 {
			return "", err
		}
		return "", fmt.Errorf("%w; nothing was installed. The end of the output:\n%s", err, tail(out.Bytes()))
	}
	if err := c.restart(u.Exe); err != nil {
		return "", fmt.Errorf("the build is installed, but the node did not restart: %w", err)
	}
	done := "built " + u.Source + ", its tests pass, and the build is installed as " + u.Exe + ". A running node was restarted with it; "
	if plugin.Restart(ctx, u.Exe) {
		return done + "ouro starts anew with it once you have answered, and this conversation goes on there.", nil
	}
	return done + "this conversation uses the old build until ouro is started again.", nil
}

// tail is the end of output, from the start of a line.
func tail(output []byte) string {
	output = bytes.TrimSpace(output)
	if len(output) > maxOutput {
		output = output[len(output)-maxOutput:]
		if nl := bytes.IndexByte(output, '\n'); nl >= 0 {
			output = output[nl+1:]
		}
	}
	return string(output)
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
