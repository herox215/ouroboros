// Package update is the plugin with the command that updates ouro itself and
// the tool with which the agent of the owner builds its own source.
package update

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ouroboros/internal/assistant"
	"ouroboros/internal/home"
	"ouroboros/internal/node"
	"ouroboros/internal/sanity"
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
			updater: &update.Updater{Repo: update.Repo, Source: update.Source, Dir: dir.UpdateDir(), Revision: update.Revision(), Skills: dir.Skills()},
		}
		plugin.Add(ctx, plugin.Command{Name: "update", Help: "build and install the newest version", Run: c.run})
		plugin.Add(ctx, plugin.Command{Name: "rollback", Help: "install the build before the last update or rebuild again", Run: c.rollback})
		plugin.Add(ctx, plugin.Tool{
			Name: Rebuild,
			Description: "Build ouro, the program you are part of, from its source in " + sourceName(update.Source) + " as it is now, and install it: " +
				"this is how a change of the source takes effect. The build is the check of a change of text; " +
				"for a change of logic name the packages you changed in test, and their tests run first: " +
				"never all of them, coverage or mutation tests. " +
				"Only when the tests and the build pass is the binary replaced; " +
				"otherwise nothing is installed, and the error ends with the output: fix the source and call it again. " +
				"The skills in skills/ of the checkout are installed into the skills directory of the node with the build, " +
				"so a copy of a skill there is never synced by hand. " +
				"Once you have answered, the node starts anew with the new build and the conversation goes on there, " +
				"and so does the front end that showed it. What you built is yours to use only in the new build: " +
				"with continue: true you are told to go on there by yourself, so end this answer with what is left to do.",
			Schema: `{"type":"object","properties":{"test":{"type":"array","items":{"type":"string"},` +
				`"description":"the packages whose tests run before the build, like [\"./internal/tui/\"]"},` +
				`"continue":{"type":"boolean","description":"go on by yourself in the new build, to use what you built"}}}`,
			Trusted: true,
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var args rebuildArgs
				if err := plugin.Decode(Rebuild, raw, &args); err != nil {
					return "", err
				}
				return c.rebuild(ctx, args)
			},
		})
		plugin.Add(ctx, plugin.ToolView{Tool: Rebuild, Show: func(json.RawMessage) string {
			return "building " + sourceName(update.Source)
		}, Detail: assistant.HowItEnded})
		plugin.Add(ctx, plugin.Tool{
			Name:        "check_sanity",
			Description: "Run all Ouroboros Go tests with race detection and coverage, then native GUI and JavaScript frontend tests. Report failing packages/tests, skips and uncovered source line ranges that may need tests. Runs the source checkout of this build, not the current conversation directory. Keeps full logs/profile in a temporary directory; does not install a build. Expensive: use when explicitly requested, not after every edit. Coverage measures statements, not assertion quality or all branches; failures can leave it incomplete. Missing GUI dependencies are reported as failures, never silently skipped.",
			Schema:      `{"type":"object","properties":{"timeout_minutes":{"type":"integer","minimum":1,"maximum":60,"description":"overall timeout in minutes, default 8"},"gui":{"type":"boolean","description":"include native GUI and frontend tests, default true; false reports these as skipped"}},"additionalProperties":false}`,
			Trusted:     true,
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				var args sanity.Options
				if err := plugin.Decode("check_sanity", raw, &args); err != nil {
					return "", err
				}
				goTool, err := update.GoTool()
				if err != nil {
					return "", err
				}
				return sanity.Run(ctx, c.updater.Source, goTool, args)
			},
		})
		plugin.Add(ctx, plugin.ToolView{Tool: "check_sanity", Show: func(json.RawMessage) string { return "checking tests and coverage in " + sourceName(c.updater.Source) }, Detail: assistant.HowItEnded})
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

// rollback installs the binary from before the last update or rebuild again,
// as when a build an agent made does not do what it should, and restarts the
// node if it is running. A second rollback goes forward again.
func (c *updateCommand) rollback(context.Context, []string) error {
	exe := c.updater.Exe
	if exe == "" {
		var err error
		if exe, err = c.local.Executable(); err != nil {
			return err
		}
	}
	if err := update.Back(exe); err != nil {
		return err
	}
	if err := c.restart(exe); err != nil {
		return err
	}
	fmt.Fprintln(c.term.Out, "● back to the build before")
	return nil
}

// sourceName names the checkout source to the agent and the owner.
func sourceName(source string) string {
	if source == "" {
		return "a checkout this build does not know"
	}
	return source
}

// rebuildArgs are the arguments of the tool rebuild.
type rebuildArgs struct {
	Test     []string `json:"test"`
	Continue bool     `json:"continue"`
}

// rebuild installs a build of the source over the running binary. The node
// whose answer asked for it, to the owner or to a peer the owner listed the
// tool for in tools.peers, starts anew with it once no answer is on its way,
// see plugin.Restart; where nobody does that, a running node is restarted at
// once.
func (c *updateCommand) rebuild(ctx context.Context, args rebuildArgs) (string, error) {
	var out bytes.Buffer
	u := *c.updater
	u.Out = &out
	u.Tests = args.Test
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
	if len(u.Tests) > 0 {
		done = "the tests of " + strings.Join(u.Tests, ", ") + " passed; " + done
	}
	// The node this runs in cannot stop itself while the answer is on its
	// way: it takes its own place once the answer is given.
	if plugin.Restart(ctx, u.Exe) {
		if args.Continue && plugin.GoOn(ctx) {
			return done + "The node starts anew with it once you have answered, and there you are told to go on: " +
				"end this answer with what is left to do, and do it then.", nil
		}
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
