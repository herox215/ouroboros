package tui

import (
	"testing"

	"ouroboros/plugin"
)

// The interval of the archivist is edited like any other text: enter saves
// what is typed as the setting of the memory interval.
func TestSettingsMemoryInterval(t *testing.T) {
	s := someSettings()
	s.MemoryInterval = "12h"
	m, b := openSettings(t, s)
	pick(t, m, agentsSection, "Memory archivist", "change")
	press(m, "enter")
	if m.config.editing == nil || m.config.input.Value() != "12h" {
		t.Fatalf("editing %+v %q", m.config.editing, m.config.input.Value())
	}
	m.config.input.SetValue("24h")
	do(m)
	if len(b.changes) != 1 || b.changes[0] != (Setting{Kind: plugin.SetMemoryInterval, Value: "24h"}) {
		t.Fatalf("saved %+v", b.changes)
	}
}
