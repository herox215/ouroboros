package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// A log of changes is kept on the memory screen alone, with the selection
// inside it, and its error too.
func TestMemoryChangesLoaded(t *testing.T) {
	m, b := newModel(t)
	s := &m.memories

	// What comes once the memory screen was left is let go.
	m.screen = screenChat
	if cmd := m.changesLoaded(changesMsg{changes: []plugin.MemoryChange{{ID: 1}}}); cmd != nil || s.changeLoaded {
		t.Fatalf("a change log was shown off the memory screen: %v, %v", cmd, s.changeLoaded)
	}

	m.screen = screenMemory
	b.memoryChanges = []plugin.MemoryChange{{ID: 2, Action: "rewrite"}, {ID: 1, Action: "merge"}}
	s.changeAt = 9
	if cmd := m.changesLoaded(changesMsg{changes: b.memoryChanges}); cmd != nil || !s.changeLoaded || len(s.changeLog) != 2 || s.changeAt != 1 {
		t.Fatalf("loaded: %v, %v, %d, %d", cmd, s.changeLoaded, len(s.changeLog), s.changeAt)
	}
	m.changesLoaded(changesMsg{err: errors.New("no log")})
	if s.changeErr == nil || !s.changeLoaded {
		t.Fatalf("the error was not kept: %v", s.changeErr)
	}
}

// Taking a change back is said, and the log is read anew, whether it worked
// or not.
func TestMemoryChangesUndone(t *testing.T) {
	m, b := newModel(t)
	b.memoryChanges = []plugin.MemoryChange{{ID: 4, Action: "rewrite"}}
	cmd := m.changeUndone(changeUndoneMsg{id: 4})
	if m.notice != "change #4 undone" || m.noticeErr || cmd == nil {
		t.Fatalf("undone: %q, %v, %v", m.notice, m.noticeErr, cmd)
	}
	if msg, ok := cmd().(changesMsg); !ok || len(msg.changes) != 1 || msg.err != nil {
		t.Fatalf("the log was not read anew: %+v", msg)
	}

	b.changeErr = errors.New("the log is gone")
	cmd = m.changeUndone(changeUndoneMsg{id: 9, err: errors.New("already undone")})
	if m.notice != "already undone" || !m.noticeErr || cmd == nil {
		t.Fatalf("refused: %q, %v, %v", m.notice, m.noticeErr, cmd)
	}
	if msg, ok := cmd().(changesMsg); !ok || msg.err == nil {
		t.Fatalf("the log was not read anew after an error: %+v", msg)
	}
}

// ctrl+r opens the log, and esc and ctrl+r close it.
func TestMemoryChangesKeyOpensAndClosesLog(t *testing.T) {
	m, b := newModel(t)
	m.screen = screenMemory
	b.memoryChanges = []plugin.MemoryChange{{ID: 2, Action: "rewrite", At: "2026-10-26T10:00:00Z"}}
	s := &m.memories

	cmd := s.handleKey(m, tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if !s.changes || s.changeAt != 0 || s.undoAsk != 0 || cmd == nil {
		t.Fatalf("ctrl+r: %v, %d, %d, %v", s.changes, s.changeAt, s.undoAsk, cmd)
	}
	msg, ok := cmd().(changesMsg)
	if !ok || len(msg.changes) != 1 || msg.err != nil {
		t.Fatalf("the changes were not read: %+v", msg)
	}
	m.Update(msg)
	if !s.changeLoaded || len(s.changeLog) != 1 {
		t.Fatalf("the changes were not shown: %v, %d", s.changeLoaded, len(s.changeLog))
	}

	if cmd := s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil || s.changes {
		t.Fatalf("esc did not close the log: %v, %v", cmd, s.changes)
	}
	s.changes = true
	if cmd := s.handleKey(m, tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}); cmd != nil || s.changes {
		t.Fatalf("ctrl+r did not close the log: %v, %v", cmd, s.changes)
	}
}

// The keys move through the log and stop at its ends; esc first takes the
// question to undo back.
func TestMemoryChangesKeys(t *testing.T) {
	m, _ := newModel(t)
	s := &m.memories
	s.changes = true
	for i := 0; i < 10; i++ {
		s.changeLog = append(s.changeLog, plugin.MemoryChange{ID: int64(10 - i)})
	}
	s.page = 3

	// esc takes the question back while it waits, and closes the log after.
	s.changeAt, s.undoAsk = 5, 9
	if cmd := s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil || s.undoAsk != 0 || !s.changes {
		t.Fatalf("esc took the log instead of the question: %v, %d, %v", cmd, s.undoAsk, s.changes)
	}
	if cmd := s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil || s.changes {
		t.Fatalf("esc did not close the log: %v, %v", cmd, s.changes)
	}

	s.changes, s.changeAt = true, 0
	s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyUp}) // stays on the first
	if s.changeAt != 0 {
		t.Fatalf("up from the first: %d", s.changeAt)
	}
	s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if s.changeAt != 1 {
		t.Fatalf("down: %d", s.changeAt)
	}
	s.changeAt = 5
	s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if s.changeAt != 2 {
		t.Fatalf("pgup: %d", s.changeAt)
	}
	s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if s.changeAt != 5 {
		t.Fatalf("pgdown: %d", s.changeAt)
	}
	s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if s.changeAt != 0 {
		t.Fatalf("home: %d", s.changeAt)
	}
	s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if s.changeAt != 9 {
		t.Fatalf("end: %d", s.changeAt)
	}
}

// Enter asks about a change once and takes it back the second time; an
// undone change, and a line past the log, take nothing back.
func TestMemoryChangesEnter(t *testing.T) {
	m, b := newModel(t)
	m.screen = screenMemory
	b.memoryChanges = []plugin.MemoryChange{{ID: 2, Action: "rewrite"}}
	s := &m.memories
	s.changes, s.changeLoaded = true, true
	s.changeLog = []plugin.MemoryChange{{ID: 2, Action: "rewrite"}, {ID: 1, Action: "merge", Undone: true}}

	s.changeAt = len(s.changeLog)
	if cmd := s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || s.undoAsk != 0 {
		t.Fatalf("enter past the log: %v, %d", cmd, s.undoAsk)
	}
	s.changeAt = 1
	if cmd := s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || s.undoAsk != 0 {
		t.Fatalf("enter on an undone change: %v, %d", cmd, s.undoAsk)
	}

	s.changeAt = 0
	if cmd := s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || s.undoAsk != 2 {
		t.Fatalf("the first enter should ask: %v, %d", cmd, s.undoAsk)
	}
	cmd := s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || s.undoAsk != 0 {
		t.Fatalf("the second enter should undo: %v, %d", cmd, s.undoAsk)
	}
	msg, ok := cmd().(changeUndoneMsg)
	if !ok || msg.id != 2 || msg.err != nil {
		t.Fatalf("undo: %+v", msg)
	}
	if len(b.undoneChanges) != 1 || b.undoneChanges[0] != 2 {
		t.Fatalf("taken back: %v", b.undoneChanges)
	}
	m.Update(msg)
	if m.notice != "change #2 undone" || m.noticeErr {
		t.Fatalf("after the undo: %q, %v", m.notice, m.noticeErr)
	}

	// What the node refuses is said in red.
	b.fail = map[string]error{"undoMemory": errors.New("the memory is gone")}
	s.undoAsk = 2
	cmd = s.handleKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("the second enter should undo")
	}
	msg, ok = cmd().(changeUndoneMsg)
	if !ok || msg.err == nil {
		t.Fatalf("a refused undo: %+v", msg)
	}
	m.Update(msg)
	if m.notice != "the memory is gone" || !m.noticeErr {
		t.Fatalf("the refused undo: %q, %v", m.notice, m.noticeErr)
	}
}

// The changes view says when it reads the log, when it cannot be listed,
// when there is nothing in it, and what a second enter does.
func TestMemoryChangesViews(t *testing.T) {
	m, _ := newModel(t)
	m.screen = screenMemory
	s := &m.memories
	s.changes = true

	s.changeLoaded = false
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "reading the changes…") {
		t.Fatalf("the loading view:\n%s", view)
	}

	s.changeErr = errors.New("no log")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "The changes cannot be listed: no log") {
		t.Fatalf("the error view:\n%s", view)
	}

	s.changeErr, s.changeLoaded = nil, true
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "The archivist has changed nothing yet.") {
		t.Fatalf("the empty view:\n%s", view)
	}

	s.changeLog = []plugin.MemoryChange{{ID: 7, Action: "rewrite", At: "2026-10-26T10:00:00Z"}}
	s.changeAt, s.undoAsk = 0, 7
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Enter again to undo change #7; esc takes the question back.") {
		t.Fatalf("the question to undo:\n%s", view)
	}
}
