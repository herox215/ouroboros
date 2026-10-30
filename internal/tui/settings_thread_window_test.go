package tui

import (
	"testing"

	"ouroboros/plugin"
)

// The window of the thread is edited like any other text: enter saves what
// is typed as the setting of the window.
func TestSettingsThreadWindow(t *testing.T) {
	m, b := openSettings(t, someSettings())
	pick(t, m, agentsSection, "Sessions the thread continues", "change")
	press(m, "enter")
	if m.config.editing == nil || m.config.input.Value() != "3h" {
		t.Fatalf("editing %+v %q", m.config.editing, m.config.input.Value())
	}
	m.config.input.SetValue("90m")
	do(m)
	if len(b.changes) != 1 || b.changes[0] != (Setting{Kind: plugin.SetThreadWindow, Value: "90m"}) {
		t.Fatalf("saved %+v", b.changes)
	}
}
