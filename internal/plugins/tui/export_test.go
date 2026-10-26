package tui

import (
	"context"
	"testing"

	"ouroboros/internal/tui"
	"ouroboros/plugin"
)

// Backend is what the terminal UI controls: what owner does and the sessions,
// with the tool calls shown as the plugins of ctx say.
func Backend(owner *plugin.Owner, ctx *plugin.Context, sessions plugin.Sessions) tui.Backend {
	return &ui{Owner: owner, ctx: ctx, sessions: sessions}
}

// Use makes the plugin hand the UI to use instead of showing it on a
// terminal, until the test ends.
func Use(t *testing.T, use func(m *tui.Model) error) {
	old := show
	show = func(_ context.Context, m *tui.Model) error { return use(m) }
	t.Cleanup(func() { show = old })
}

// ResumeEnv says what the UI of a new process goes on from.
const ResumeEnv = resumeEnv

// Show runs m on the terminal as the plugin does.
func Show(ctx context.Context, m *tui.Model) error { return show(ctx, m) }

// Shown is e as the UI of the plugins of ctx shows it.
func Shown(ctx *plugin.Context, e plugin.SessionEvent) tui.Event { return (&ui{ctx: ctx}).event(e) }
