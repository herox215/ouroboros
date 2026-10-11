package update

import (
	"context"
	"io"

	"ouroboros/internal/home"
	"ouroboros/internal/node"
	"ouroboros/internal/update"
	"ouroboros/plugin"
)

// Command returns the update command of the node in dir, which prints to out,
// installs with updater and takes exe for the binary of the node.
func Command(dir home.Dir, out io.Writer, updater *update.Updater, exe func() (string, error)) func(context.Context, []string) error {
	c := &updateCommand{
		term:    plugin.NewConsole(plugin.Terminal{Out: out}),
		local:   node.Local{Home: dir, Exe: exe},
		updater: updater,
	}
	return c.run
}

// Restart replaces the node in dir, if it runs, by one that runs exe.
func Restart(dir home.Dir, exe string) error {
	return (&updateCommand{local: node.Local{Home: dir}}).restart(exe)
}

// Tool returns what runs the tool rebuild of the node in dir, which builds
// with updater and takes exe for the binary of the node, and tests the
// packages test first.
func Tool(dir home.Dir, updater *update.Updater, exe func() (string, error), test ...string) func(context.Context) (string, error) {
	c := &updateCommand{local: node.Local{Home: dir, Exe: exe}, updater: updater}
	return func(ctx context.Context) (string, error) { return c.rebuild(ctx, rebuildArgs{Test: test}) }
}

// MaxOutput and Tail are what an agent gets of a failed build.
const MaxOutput = maxOutput

func Tail(output string) string { return tail([]byte(output)) }
