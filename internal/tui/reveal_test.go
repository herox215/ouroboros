package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"ouroboros/plugin"
)

func TestThreadAnswerReveal(t *testing.T) {
	text := strings.Repeat("Grüße 世界, fertig! ", 20) + "ENDE"
	m, _ := threadModel(t, "", nil)
	c := m.chat
	m.event(eventMsg{id: c.contextID, event: Event{Kind: plugin.SessionAnswer, Text: text}})
	r := c.entries[0].reveal
	if r == nil || r.shown == 0 || r.shown >= len(r.runes) || c.entries[0].text != text {
		t.Fatal("answer did not begin revealing with its complete text preserved")
	}
	before := ansi.Strip(m.history.GetContent())
	if strings.Contains(before, "ENDE") || !utf8.ValidString(before) {
		t.Fatalf("initial frame: %q", before)
	}
	msg := revealMsg{c, 0, r}
	for n := 0; n < 50 && c.entries[0].reveal != nil; n++ {
		m.Update(msg)
	}
	if c.entries[0].reveal != nil || !strings.Contains(ansi.Strip(m.history.GetContent()), "ENDE") {
		t.Fatal("answer did not finish revealing")
	}
	m.replay(c, []Event{{Kind: plugin.SessionAnswer, Text: text}})
	m.Update(msg) // a stale timer must not animate replayed history
	if c.entries[0].reveal != nil || !strings.Contains(ansi.Strip(m.history.GetContent()), "ENDE") {
		t.Fatal("replayed history was animated")
	}
	c.partial = "already streamed"
	m.event(eventMsg{id: c.contextID, event: Event{Kind: plugin.SessionAnswer, Text: text}})
	if c.entries[1].reveal != nil {
		t.Fatal("streamed answer was animated twice")
	}
	topic := m.conversation("topic")
	m.event(eventMsg{id: topic.contextID, event: Event{Kind: plugin.SessionAnswer, Text: text}})
	if topic.entries[0].reveal != nil {
		t.Fatal("background answer was animated")
	}
}
