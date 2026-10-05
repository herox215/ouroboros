package tui

import (
	"context"
	"encoding/json"
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

// HandOver returns the plugin.Restarting that starts exe with what the UI
// of the new process goes on from.
func HandOver(exe string, chat tui.Chat, memory json.RawMessage, wasRunning bool) error {
	return handOver(exe, resume{chat, memory, wasRunning})
}

// ResumeEnv names the file the UI of a new process goes on from.
const ResumeEnv = resumeEnv
