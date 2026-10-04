package tui

import (
	"context"
	"testing"

	"ouroboros/internal/tui"
	"ouroboros/plugin"
)

// Backend is what the terminal UI controls: what owner does and bot, with the
// tool calls shown as the plugins of ctx say.
func Backend(owner *plugin.Owner, ctx *plugin.Context, bot *plugin.Assistant) tui.Backend {
	return &ui{Owner: owner, ctx: ctx, assistant: bot}
}

// Use makes the plugin hand the UI to use instead of showing it on a
// terminal, until the test ends.
func Use(t *testing.T, use func(m *tui.Model) error) {
	old := show
	show = func(_ context.Context, m *tui.Model) error { return use(m) }
	t.Cleanup(func() { show = old })
}
