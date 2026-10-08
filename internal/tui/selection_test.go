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

// numbered is an answer of many words, each another, so that every line of
// it is another.
func numbered(n int) string {
	words := make([]string, n)
	for i := range words {
		words[i] = fmt.Sprintf("w%d", i)
	}
	return strings.Join(words, " ")
}

// dragged presses the left button at x0 and y0, drags to x1 and y1 and
// releases it there.
func dragged(m *Model, x0, y0, x1, y1 int) tea.Cmd {
	mouseAt(m, tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
	mouseAt(m, tea.MouseMotionMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	return mouseAt(m, tea.MouseReleaseMsg{X: x1, Y: y1, Button: tea.MouseLeft})
}

// onScreen is the text of the screen from x0 at y0 to x1 at y1, both
// included, as the history shows it: each row without what is left of the
// history and without the spaces at its end.
func onScreen(m *Model, x0, y0, x1, y1 int) string {
	rows := strings.Split(view(m), "\n")
	var lines []string
	for y := y0; y <= y1; y++ {
		from, to := historyLeft, historyLeft+m.history.Width()
		if y == y0 {
			from = x0
		}
		if y == y1 {
			to = x1 + 1
		}
		lines = append(lines, strings.TrimRight(ansi.Cut(rows[y], from, to), " "))
	}
	return strings.Join(lines, "\n")
}

// What is copied is what the screen shows where the mouse went, also with
// the history scrolled and the suggestions of a command taking rows of it.
// Dragged below the history, the selection ends at its last row shown.
func TestSelectionIsWhatTheScreenShows(t *testing.T) {
	clipboardOf(t)
	m, b := newModel(t)
	b.reply = numbered(1000)
	answered(t, m, say(t, m, "ping"))
	m.history.GotoTop()
	height, right := m.history.Height(), historyLeft+m.history.Width()-1
	last := historyTop + height - 1
	want := onScreen(m, 4, historyTop, right, last)
	if got := terminalGot(t, dragged(m, 4, historyTop, right, 29)); got != want {
		t.Fatalf("dragged below the history: got %q, want %q", got, want)
	}

	// /se offers three commands, in three rows the history gives up.
	m.input.SetValue("/se")
	hidden := len(m.suggestionLines(m.chatHeight() - 3))
	if hidden != 3 {
		t.Fatalf("%d suggestions", hidden)
	}
	if m.history.SetYOffset(5); m.history.AtBottom() {
		t.Fatal("the history should be scrolled up")
	}
	last = historyTop + height - hidden - 1
	want = onScreen(m, 4, historyTop, 6, historyTop)
	if got := terminalGot(t, dragged(m, 4, historyTop, 6, historyTop)); got != want || !strings.HasPrefix(got, "w") {
		t.Fatalf("on the first row: got %q, want %q", got, want)
	}
	want = onScreen(m, 4, historyTop+1, right, last)
	if got := terminalGot(t, dragged(m, 4, historyTop+1, right, 29)); got != want {
		t.Fatalf("dragged onto the suggestions: got %q, want %q", got, want)
	}
	// The rows of the suggestions are not the history.
	dragged(m, 4, last+1, 6, historyTop)
	if m.selected != nil {
		t.Fatal("a press on the suggestions selects nothing")
	}
}

// The history begins at its first row and column and ends before the
// border; a press outside of it selects nothing.
func TestTheEdgesOfTheHistory(t *testing.T) {
	clipboardOf(t)
	m, _ := newModel(t)
	answered(t, m, say(t, m, "ping"))
	right, below := historyLeft+m.history.Width(), historyTop+m.history.Height()
	for _, at := range []struct{ x, y int }{{historyLeft, historyTop}, {right - 1, below - 1}} {
		mouseAt(m, tea.MouseClickMsg{X: at.x, Y: at.y, Button: tea.MouseLeft})
		mouseAt(m, tea.MouseMotionMsg{X: 6, Y: 5, Button: tea.MouseLeft})
		if m.selected == nil {
			t.Fatalf("a press at %d, %d is on the history", at.x, at.y)
		}
	}
	for _, at := range []struct{ x, y int }{{historyLeft - 1, 4}, {right, 4}, {4, historyTop - 1}, {4, below}} {
		mouseAt(m, tea.MouseClickMsg{X: at.x, Y: at.y, Button: tea.MouseLeft})
		mouseAt(m, tea.MouseMotionMsg{X: 6, Y: 5, Button: tea.MouseLeft})
		if m.selected != nil {
			t.Fatalf("a press at %d, %d is not on the history", at.x, at.y)
		}
	}
	// A press of the left button off the history, and one of the right
	// button on it, begin no drag.
	for _, press := range []tea.MouseClickMsg{{X: 4, Y: below + 2, Button: tea.MouseLeft}, {X: 4, Y: 4, Button: tea.MouseRight}} {
		mouseAt(m, press)
		mouseAt(m, tea.MouseMotionMsg{X: 6, Y: 5, Button: tea.MouseLeft})
		if m.selected != nil {
			t.Fatalf("%+v begins no drag", press)
		}
	}
}

// Over the setup, the splash, or a node that is not set up, the mouse
// selects nothing.
func TestNoSelectionOffTheChat(t *testing.T) {
	for name, set := range map[string]func(*Model, *fakeBackend){
		"setup":  func(m *Model, _ *fakeBackend) { run(m, "/setup") },
		"splash": func(m *Model, _ *fakeBackend) { m.splash.show = true },
		"not set up": func(m *Model, b *fakeBackend) {
			b.status = Status{}
			m.Update(m.refreshStatus())
		},
	} {
		m, b := newModel(t)
		answered(t, m, say(t, m, "ping"))
		set(m, b)
		if m.screen != screenChat {
			t.Fatalf("%s: the screen %v", name, m.screen)
		}
		mouseAt(m, tea.MouseClickMsg{X: 4, Y: 4, Button: tea.MouseLeft})
		mouseAt(m, tea.MouseMotionMsg{X: 6, Y: 7, Button: tea.MouseLeft})
		if m.pressed != nil || m.selected != nil {
			t.Fatalf("%s: the mouse should select nothing", name)
		}
	}
}

// Of a selection from the end of a line, that line has nothing marked and
// the ones below it are; the last only up to where the mouse is. What lies
// beyond the end of the history is not copied.
func TestMarkedFromTheEndOfALine(t *testing.T) {
	clipboardOf(t)
	m, _ := newModel(t)
	answered(t, m, say(t, m, "ping"))
	// "  ping" ends at column 6 of line 1, "  pong" is line 4.
	mouseAt(m, tea.MouseClickMsg{X: 8, Y: 4, Button: tea.MouseLeft})
	mouseAt(m, tea.MouseMotionMsg{X: 5, Y: 7, Button: tea.MouseLeft})
	lines := m.highlight(strings.Split(m.history.View(), "\n"))
	marked := "\x1b[7m"
	if strings.Contains(lines[1], marked) || !strings.Contains(lines[3], marked) ||
		!strings.HasPrefix(lines[4], marked+"  po\x1b[m") || !strings.HasPrefix(ansi.Strip(lines[4]), "  pong") {
		t.Fatalf("marked %q", lines[:5])
	}
	agent := ansi.Strip(m.entryHead(m.chat.entries[1]))
	if got := terminalGot(t, dragged(m, 4, 4, 4, historyTop+m.history.Height()-1)); got != "ping\n\n"+agent+"\n  pong" {
		t.Fatalf("copied %q", got)
	}
}

// The suggestions of a command pasted while the button is down can hide all
// of the history: the drag then ends on the line below the rows they hide.
func TestDragWhileSuggestionsHideTheHistory(t *testing.T) {
	clipboardOf(t)
	m, b := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	b.reply = numbered(1000)
	answered(t, m, say(t, m, "ping"))
	m.history.GotoTop()
	mouseAt(m, tea.MouseClickMsg{X: 4, Y: historyTop, Button: tea.MouseLeft})
	m.Update(tea.PasteMsg{Content: "/"})
	hidden := len(m.suggestionLines(m.chatHeight() - 3))
	if hidden <= m.history.Height() {
		t.Fatalf("%d suggestions hide %d rows of %d", len(m.matching("/")), hidden, m.history.Height())
	}
	mouseAt(m, tea.MouseMotionMsg{X: 4, Y: historyTop, Button: tea.MouseLeft})
	got := terminalGot(t, mouseAt(m, tea.MouseReleaseMsg{X: 4, Y: historyTop, Button: tea.MouseLeft}))
	if lines := strings.Split(got, "\n"); len(lines) != hidden+1 {
		t.Fatalf("copied %d lines for %d hidden: %q", len(lines), hidden, got)
	}
}
