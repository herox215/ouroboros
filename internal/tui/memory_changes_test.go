package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The changes of the archivist are shown with id, action, reason and whether
// they were undone, the one selected marked.
func TestMemoryChangesView(t *testing.T) {
	m, _ := newModel(t)
	m.screen = screenMemory
	s := &m.memories
	s.changes, s.changeLoaded = true, true
	s.changeLog = []plugin.MemoryChange{
		{ID: 2, Action: "rewrite", Reason: "shorter", At: "2026-10-26T10:00:00Z"},
		{ID: 1, Action: "merge", Reason: "the same", Undone: true, At: "2026-10-25T10:00:00Z"},
	}
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"Archivist changes · 2", "#2 rewrite", "shorter", "#1 merge", "the same", "undone", "enter undo"} {
		if !strings.Contains(view, want) {
			t.Fatalf("%q is not in the changes:\n%s", want, view)
		}
	}
}
