package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"ouroboros/plugin"
)

func TestThreadVisibleWindow(t *testing.T) {
	m, _ := threadModel(t, "", nil)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	c := m.chat
	m.replay(c, []Event{
		{Kind: plugin.SessionMessage, Text: "yesterday", At: now.Add(-24 * time.Hour)},
		{Kind: plugin.SessionAnswer, Text: "old answer", At: now.Add(-7 * time.Hour)},
		{Kind: plugin.SessionMessage, Text: "boundary", At: now.Add(-6 * time.Hour)},
		{Kind: plugin.SessionAnswer, Text: "recent answer", At: now.Add(-time.Hour)},
	})
	shown := ansi.Strip(m.history.GetContent())
	if strings.Contains(shown, "yesterday") || strings.Contains(shown, "old answer") || !strings.Contains(shown, "boundary") || !strings.Contains(shown, "recent answer") {
		t.Fatalf("window: %s", shown)
	}
	now = now.Add(2 * time.Hour)
	m.Update(tickMsg{})
	shown = ansi.Strip(m.history.GetContent())
	if strings.Contains(shown, "boundary") || !strings.Contains(shown, "recent answer") {
		t.Fatalf("rolling window: %s", shown)
	}
	now = now.Add(24 * time.Hour)
	m.Update(tickMsg{})
	if len(c.entries) != 4 || m.history.GetContent() != emptyChat(m.agentName(), m.history.Width(), m.history.Height()) {
		t.Fatal("expired thread was not empty or archived entries were changed")
	}
	topic := m.conversation("topic")
	m.chat = topic
	m.replay(topic, []Event{{Kind: plugin.SessionAnswer, Text: "old topic", At: now.Add(-24 * time.Hour)}})
	if !strings.Contains(ansi.Strip(m.history.GetContent()), "old topic") {
		t.Fatal("ordinary session history was hidden")
	}
}
