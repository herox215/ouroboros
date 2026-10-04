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
