package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

// Moving on from a change takes the question to undo it back.
func TestMemoryChangesMoveDropsQuestion(t *testing.T) {
	m, _ := newModel(t)
	m.screen = screenMemory
	s := &m.memories
	s.changes, s.changeLoaded = true, true
	s.changeLog = []plugin.MemoryChange{{ID: 2, Action: "rewrite"}, {ID: 1, Action: "merge"}}
	s.changesKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.undoAsk != 2 {
		t.Fatalf("asked about %d", s.undoAsk)
	}
	s.changesKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if s.undoAsk != 0 || s.changeAt != 1 {
		t.Fatalf("after down: asked %d, at %d", s.undoAsk, s.changeAt)
	}
}
