package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The terminal shows no picture, and says so where it was shown: an agent that
// reports success is not the only one to know it.
func TestPictureIsShownAsALine(t *testing.T) {
	m, _ := newModel(t)
	// Wide enough for the whole name on one line.
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	m.layout()
	c := m.chat
	name := strings.Repeat("a", 64) + ".png"
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "build it"}, true)
	m.apply(c, Event{Kind: plugin.SessionPicture, Text: name}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "built"}, true)
	m.renderHistory(false)
	view := ansi.Strip(m.history.View())
	at := strings.Index(view, "build it")
	if at < 0 || !strings.Contains(view[at:], "[picture: "+name+"]") {
		t.Fatalf("no line for the picture in:\n%s", view)
	}
	if !c.entries[1].picture || c.entries[1].asks() {
		t.Fatalf("entries %+v", c.entries)
	}
}
