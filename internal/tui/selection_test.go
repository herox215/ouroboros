package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// clipboardOf replaces the clipboard of the system with one of the test.
func clipboardOf(t *testing.T) *[]string {
	t.Helper()
	var copied []string
	before := writeClipboard
	writeClipboard = func(s string) error { copied = append(copied, s); return errors.New("no clipboard") }
	t.Cleanup(func() { writeClipboard = before })
	return &copied
}

// mouseAt sends a press, a drag or a release of the left button at x and y.
func mouseAt(m *Model, msg tea.Msg) tea.Cmd {
	_, cmd := m.Update(msg)
	return cmd
}

// terminalGot runs cmd, the copy of a selection, and is what it asked the
// terminal to put on the clipboard.
func terminalGot(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("the selection should be copied")
	}
	got := ""
	for _, c := range cmd().(tea.BatchMsg) {
		if msg := c(); msg != nil {
			got = fmt.Sprint(msg)
		}
	}
	return got
}

func TestSelectAndCopyTheHistory(t *testing.T) {
	copied := clipboardOf(t)
	m, _ := newModel(t)
	answered(t, m, say(t, m, "ping"))
	// The history starts at row 3, right of the border and padding: line 1
	// is "  ping", line 4 "  pong".
	if mouseAt(m, tea.MouseClickMsg{X: 4, Y: 4, Button: tea.MouseLeft}) != nil {
		t.Fatal("a press copies nothing")
	}
	mouseAt(m, tea.MouseMotionMsg{X: 5, Y: 7, Button: tea.MouseLeft})
	if !strings.Contains(m.View().Content, "\x1b[7m") {
		t.Fatal("what is selected should be marked")
	}
	cmd := mouseAt(m, tea.MouseReleaseMsg{X: 5, Y: 7, Button: tea.MouseLeft})
	mine, agent := ansi.Strip(m.entryHead(m.chat.entries[0])), ansi.Strip(m.entryHead(m.chat.entries[1]))
	want := "ping\n\n" + agent + "\n  po"
	if got := terminalGot(t, cmd); got != want {
		t.Fatalf("the terminal got %q, want %q", got, want)
	}
	if len(*copied) != 1 || (*copied)[0] != want || m.notice != "copied to the clipboard" {
		t.Fatalf("copied %q, notice %q", *copied, m.notice)
	}
	// Selected backwards, from below the history, it is the same text up to
	// the end of the last line.
	mouseAt(m, tea.MouseClickMsg{X: 50, Y: 29, Button: tea.MouseLeft})
	if m.selected != nil {
		t.Fatal("a press below the history selects nothing")
	}
	mouseAt(m, tea.MouseClickMsg{X: 4, Y: 7, Button: tea.MouseLeft})
	mouseAt(m, tea.MouseMotionMsg{X: 1, Y: 1, Button: tea.MouseLeft})
	if got := terminalGot(t, mouseAt(m, tea.MouseReleaseMsg{X: 1, Y: 1, Button: tea.MouseLeft})); got != mine+"\n  ping\n\n"+agent+"\n  p" {
		t.Fatalf("backwards %q", got)
	}
	// A key drops the selection.
	press(m, "x")
	if m.selected != nil {
		t.Fatal("a key should drop the selection")
	}
}

func TestClickSelectsNothing(t *testing.T) {
	copied := clipboardOf(t)
	m, _ := newModel(t)
	answered(t, m, say(t, m, "ping"))
	// A release without a press, and a drag, change nothing.
	if mouseAt(m, tea.MouseReleaseMsg{X: 4, Y: 4}) != nil || mouseAt(m, tea.MouseMotionMsg{X: 4, Y: 4}) != nil || m.selected != nil {
		t.Fatal("nothing is selected")
	}
	// A press on its own marks nothing: what is under the pointer does not
	// flash before the drag begins.
	mouseAt(m, tea.MouseClickMsg{X: 4, Y: 4, Button: tea.MouseLeft})
	if strings.Contains(m.View().Content, "\x1b[7m") {
		t.Fatal("a press should mark nothing before the drag")
	}
	if mouseAt(m, tea.MouseReleaseMsg{X: 4, Y: 4, Button: tea.MouseLeft}) != nil || m.selected != nil || m.pressed != nil || len(*copied) > 0 {
		t.Fatal("a click selects nothing, and keeps nothing pressed")
	}
	// Dragged away and back, it is a click as well.
	mouseAt(m, tea.MouseClickMsg{X: 4, Y: 4, Button: tea.MouseLeft})
	mouseAt(m, tea.MouseMotionMsg{X: 8, Y: 7, Button: tea.MouseLeft})
	mouseAt(m, tea.MouseMotionMsg{X: 4, Y: 4, Button: tea.MouseLeft})
	if mouseAt(m, tea.MouseReleaseMsg{X: 4, Y: 4, Button: tea.MouseLeft}) != nil || m.selected != nil || len(*copied) > 0 {
		t.Fatal("dragged back to where it began, nothing is selected")
	}
	mouseAt(m, tea.MouseClickMsg{X: 4, Y: 4, Button: tea.MouseRight})
	if m.selected != nil {
		t.Fatal("the right button selects nothing")
	}
	// Past the end of a line nothing of it is marked.
	mouseAt(m, tea.MouseClickMsg{X: 60, Y: 7, Button: tea.MouseLeft})
	mouseAt(m, tea.MouseMotionMsg{X: 70, Y: 7, Button: tea.MouseLeft})
	if strings.Contains(m.View().Content, "\x1b[7m") {
		t.Fatal("nothing should be marked past the end of a line")
	}
	// A new size wraps the lines anew, and drops the selection.
	m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	if m.selected != nil {
		t.Fatal("a new size should drop the selection")
	}
	// Not on the chat, the mouse selects nothing.
	m.screen = screenHelp
	mouseAt(m, tea.MouseClickMsg{X: 4, Y: 4, Button: tea.MouseLeft})
	if m.selected != nil {
		t.Fatal("the help is not the history")
	}
}
