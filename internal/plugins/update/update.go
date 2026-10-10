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
const Rebuild = assistant.ToolRebuild

// maxOutput is how much of the output of a failed build the agent
// gets: its end, where the failure is.
const maxOutput = 8 << 10

// Plugin contributes the command that updates ouro itself and the tool that
// builds its source. The command restarts the node in dir if it is running;
// the tool asks the node it runs in to take its own place, see rebuild.
func Plugin(dir home.Dir) plugin.Plugin {
	return plugin.NewService("update", func(ctx *plugin.Context, n needs) (services, error) {
		c := &updateCommand{
			term:    n.Console,
			local:   node.Local{Home: dir},
			updater: &update.Updater{Repo: update.Repo, Source: update.Source, Dir: dir.UpdateDir(), Revision: update.Revision()},
		}
		plugin.Add(ctx, plugin.Command{Name: "update", Help: "build and install the newest version", Run: c.run})
		plugin.Add(ctx, plugin.NewTool(Rebuild,
			"Build ouro, the program you are part of, from its source in "+sourceName(update.Source)+" as it is now, and install it: "+
				"this is how a change of the source takes effect. It only builds, and runs no tests: run them yourself where they matter. "+
				"Only when the build passes is the binary replaced; "+
				"otherwise nothing is installed, and the error ends with the output: fix the source and call it again. "+
				"Once you have answered, the node starts anew with the new build and the conversation goes on there, "+
				"and so does the terminal UI. Only for the owner of the node.",
			nil, c.rebuild))
		plugin.Add(ctx, plugin.ToolView{Tool: Rebuild, Show: func(json.RawMessage) string {
			return "building " + sourceName(update.Source)
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

// rebuild installs a build of the source over the running binary. The node whose session asked for it starts anew with it once
// the answer is given, see plugin.Restart; where nobody does that, a running
// node is restarted at once. A peer never gets that far, whatever the
// configuration allows it.
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
	done := "built " + u.Source + " and installed the build as " + u.Exe + ". "
	// The node this runs in cannot stop itself while the answer is on its
	// way: it takes its own place once the answer is given.
	if plugin.Restart(ctx, u.Exe) {
		return done + "The node starts anew with it once you have answered, and this conversation goes on there.", nil
	}
	if err := c.restart(u.Exe); err != nil {
		return "", fmt.Errorf("the build is installed, but the node did not restart: %w", err)
	}
	return done + "A running node was restarted with it; this conversation uses the old build until ouro is started again.", nil
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
