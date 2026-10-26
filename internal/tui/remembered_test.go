package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// What the review of an answer remembered is a faint line below it, which
// /undo takes back: the last first, each once, and a line tells it.
func TestRememberedIsShownAndUndone(t *testing.T) {
	m, b := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "build it"}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "built"}, true)
	m.apply(c, Event{Kind: plugin.SessionRemembered, Text: "build: make build", Remembered: json.RawMessage(`{"a":1}`)}, true)
	m.apply(c, Event{Kind: plugin.SessionRemembered, Text: "style: short answers", Remembered: json.RawMessage(`{"b":2}`)}, true)
	m.renderHistory(false)
	view := ansi.Strip(m.history.View())
	at := strings.Index(view, "built")
	if at < 0 || !strings.Contains(view[at:], "✦ remembered: build: make build · /undo") || !strings.Contains(view, "✦ remembered: style: short answers · /undo") {
		t.Fatalf("no memory below the answer in:\n%s", view)
	}

	if cmd := run(m, "/undo"); cmd == nil {
		t.Fatal("/undo does nothing")
	}
	undo := func() {
		t.Helper()
		cmd := m.undo()
		if cmd == nil {
			t.Fatalf("nothing to take back: %q", m.notice)
		}
		m.Update(cmd())
	}
	b.undone = nil
	undo()
	undo()
	if strings.Join(b.undone, " ") != `{"b":2} {"a":1}` || m.notice != "memory taken back" {
		t.Fatalf("taken back %q, notice %q", b.undone, m.notice)
	}
	if cmd := m.undo(); cmd != nil || m.notice != "nothing remembered here to take back" {
		t.Fatalf("taken back twice: %q", m.notice)
	}
	m.renderHistory(false)
	if view := ansi.Strip(m.history.View()); !strings.Contains(view, "✦ taken back: style: short answers") || !strings.Contains(view, "✦ taken back: build: make build") {
		t.Fatalf("no line that tells it in:\n%s", view)
	}
}

// What cannot be taken back is the notice, and stays to be taken back; a
// memory without what takes it back is still one.
func TestUndoFails(t *testing.T) {
	m, b := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionRemembered, Text: "build: make build"}, true)
	if c.entries[0].remembered == nil {
		t.Fatal("no memory")
	}
	b.fail = map[string]error{"undo": errors.New("memory version conflict")}
	m.Update(m.undo()())
	if m.notice != "memory version conflict" || !m.noticeErr || len(c.undone) != 0 {
		t.Fatalf("notice %q, undone %v", m.notice, c.undone)
	}
}
