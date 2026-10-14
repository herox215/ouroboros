package tui

import (
	"testing"

	"ouroboros/plugin"
)

// While the model writes a call of a tool, of which nothing shows, the line
// of the wait says how much of it there is, also over the words it wrote
// before; the call that starts, the next round and the end take it away.
func TestCallingIsShown(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "make it cyberpunk"}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswering, Text: "Ich ergänze den Look."}, true)
	if got := m.waiting(); got != "alpha is writing" {
		t.Fatalf("writing: %q", got)
	}
	m.apply(c, Event{Kind: plugin.SessionCalling, Text: "3.2 KiB"}, true)
	if got := m.waiting(); got != "alpha is preparing an action · 3.2 KiB" {
		t.Fatalf("calling: %q", got)
	}
	m.apply(c, Event{Kind: plugin.SessionSay, Text: "Ich ergänze den Look."}, true)
	if got := m.waiting(); got != "alpha is thinking" {
		t.Fatalf("after the say: %q", got)
	}
	m.apply(c, Event{Kind: plugin.SessionCalling, Text: "512 bytes"}, true)
	m.apply(c, Event{Kind: plugin.SessionStep, Tool: "edit", Text: "editing"}, true)
	if c.calling != "" {
		t.Fatalf("after the step: %q", c.calling)
	}
	m.apply(c, Event{Kind: plugin.SessionCalling, Text: "1.0 KiB"}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "done"}, true)
	if c.calling != "" || m.waiting() != "" {
		t.Fatalf("after the end: %q, %q", c.calling, m.waiting())
	}
}
