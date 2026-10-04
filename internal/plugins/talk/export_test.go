package talk

import (
	"context"
	"io"

	"ouroboros/plugin"
)

// List returns the command "list" on network, which prints to out.
func List(out io.Writer, network *plugin.Network) func(context.Context, []string) error {
	return (&talkCommands{term: plugin.NewConsole(plugin.Terminal{Out: out}), network: network}).list
}

// Interrupt makes every answer of the chat as good as interrupted by Ctrl-C
// before it starts, until the test ends.
func Interrupt(t interface{ Cleanup(func()) }) {
	old := interrupted
	interrupted = func(ctx context.Context) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		return ctx, cancel
	}
	t.Cleanup(func() { interrupted = old })
}
