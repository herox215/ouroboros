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

// inset is how many cells the text of the line of the history begins after
// the left edge of the conversation: what the owner wrote stands to the
// right, so its lines begin further in.
func inset(m *Model, line int) int {
	text := ansi.Strip(strings.Split(m.history.GetContent(), "\n")[line])
	return len(text) - len(strings.TrimLeft(text, " "))
}

func TestSelectAndCopyTheHistory(t *testing.T) {
	copied := clipboardOf(t)
	m, _ := newModel(t)
	answered(t, m, say(t, m, "ping"))
	// The history starts below the head, in the middle of its column: line
	// 1 is "ping", line 4 "pong", and the words of the owner begin where
	// the block of its message stands, to the right.
	left, top := m.transcriptLeft(), m.historyTop()
	at := inset(m, 1)
	if mouseAt(m, tea.MouseClickMsg{X: left + at, Y: top + 1, Button: tea.MouseLeft}) != nil {
		t.Fatal("a press copies nothing")
	}
	mouseAt(m, tea.MouseMotionMsg{X: left + 1, Y: top + 4, Button: tea.MouseLeft})
	if !strings.Contains(m.View().Content, "\x1b[7m") {
		t.Fatal("what is selected should be marked")
	}
	cmd := mouseAt(m, tea.MouseReleaseMsg{X: left + 1, Y: top + 4, Button: tea.MouseLeft})
	mine, agent := ansi.Strip(m.entryHead(m.chat.entries[0])), ansi.Strip(m.entryHead(m.chat.entries[1]))
	want := "ping\n\n" + agent + "\npo"
	if got := terminalGot(t, cmd); got != want {
		t.Fatalf("the terminal got %q, want %q", got, want)
	}
	if len(*copied) != 1 || (*copied)[0] != want || m.notice != "copied to the clipboard" {
		t.Fatalf("copied %q, notice %q", *copied, m.notice)
	}
	// Selected backwards, from below the history, it is the same text up to
	// the end of the last line.
	mouseAt(m, tea.MouseClickMsg{X: left + 20, Y: 29, Button: tea.MouseLeft})
	if m.selected != nil {
		t.Fatal("a press below the history selects nothing")
	}
	mouseAt(m, tea.MouseClickMsg{X: left, Y: top + 4, Button: tea.MouseLeft})
	mouseAt(m, tea.MouseMotionMsg{X: left + at, Y: 0, Button: tea.MouseLeft})
	// The line between stands at the left edge, so its block keeps the room
	// the message of the owner leaves there.
	if got := terminalGot(t, mouseAt(m, tea.MouseReleaseMsg{X: left + at, Y: 0, Button: tea.MouseLeft})); got != mine+"\n"+strings.Repeat(" ", at)+"ping\n\n"+agent+"\np" {
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
	x, y := m.transcriptLeft()+2, m.historyTop()+1
	// A release without a press, and a move without one, select nothing.
	if mouseAt(m, tea.MouseReleaseMsg{X: x, Y: y}) != nil || mouseAt(m, tea.MouseMotionMsg{X: x, Y: y}) == nil || m.selected != nil {
		t.Fatal("nothing is selected")
	}
	// A press on its own marks nothing: what is under the pointer does not
	// flash before the drag begins.
	mouseAt(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if strings.Contains(m.View().Content, "\x1b[7m") {
		t.Fatal("a press should mark nothing before the drag")
	}
	if mouseAt(m, tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}) != nil || m.selected != nil || m.pressed != nil || len(*copied) > 0 {
		t.Fatal("a click selects nothing, and keeps nothing pressed")
	}
	// Dragged away and back, it is a click as well.
	mouseAt(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	mouseAt(m, tea.MouseMotionMsg{X: 8, Y: 6, Button: tea.MouseLeft})
	mouseAt(m, tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft})
	if mouseAt(m, tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}) != nil || m.selected != nil || len(*copied) > 0 {
		t.Fatal("dragged back to where it began, nothing is selected")
	}
	mouseAt(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
	if m.selected != nil {
		t.Fatal("the right button selects nothing")
	}
	// Past the end of a line nothing of it is marked.
	mouseAt(m, tea.MouseClickMsg{X: x + 10, Y: y + 3, Button: tea.MouseLeft})
	mouseAt(m, tea.MouseMotionMsg{X: x + 20, Y: y + 3, Button: tea.MouseLeft})
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
	mouseAt(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
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
		from, to := m.transcriptLeft(), m.transcriptLeft()+m.history.Width()
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
	left, top := m.transcriptLeft(), m.historyTop()
	height, right := m.history.Height(), left+m.history.Width()-1
	last := top + height - 1
	want := onScreen(m, left+2, top, right, last)
	if got := terminalGot(t, dragged(m, left+2, top, right, 29)); got != want {
		t.Fatalf("dragged below the history: got %q, want %q", got, want)
	}

	// /se offers three commands above the composer: the history gives them
	// its rows.
	m.input.SetValue("/se")
	m.layout()
	if m.history.Height() >= height {
		t.Fatalf("the history is %d high with the suggestions, %d without", m.history.Height(), height)
	}
	if m.history.SetYOffset(5); m.history.AtBottom() {
		t.Fatal("the history should be scrolled up")
	}
	last = top + m.history.Height() - 1
	want = onScreen(m, left+2, top, left+4, top)
	if got := terminalGot(t, dragged(m, left+2, top, left+4, top)); got != want || got == "" {
		t.Fatalf("on the first row: got %q, want %q", got, want)
	}
	want = onScreen(m, left+2, top+1, right, last)
	if got := terminalGot(t, dragged(m, left+2, top+1, right, 29)); got != want {
		t.Fatalf("dragged onto the suggestions: got %q, want %q", got, want)
	}
	// The rows below the history are not the history.
	dragged(m, left+2, last+1, left+4, top)
	if m.selected != nil {
		t.Fatal("a press below the history selects nothing")
	}
}

// The history begins at its first row and column and ends at its last; a
// press outside of it selects nothing.
func TestTheEdgesOfTheHistory(t *testing.T) {
	clipboardOf(t)
	m, _ := newModel(t)
	answered(t, m, say(t, m, "ping"))
	left, top := m.transcriptLeft(), m.historyTop()
	right, below := left+m.history.Width(), top+m.history.Height()
	for _, at := range []struct{ x, y int }{{left, top}, {right - 1, below - 1}} {
		mouseAt(m, tea.MouseClickMsg{X: at.x, Y: at.y, Button: tea.MouseLeft})
		mouseAt(m, tea.MouseMotionMsg{X: left + 4, Y: top + 2, Button: tea.MouseLeft})
		if m.selected == nil {
			t.Fatalf("a press at %d, %d is on the history", at.x, at.y)
		}
	}
	for _, at := range []struct{ x, y int }{{left - 1, top + 2}, {right, top + 2}, {left + 2, top - 1}, {left + 2, below}} {
		mouseAt(m, tea.MouseClickMsg{X: at.x, Y: at.y, Button: tea.MouseLeft})
		mouseAt(m, tea.MouseMotionMsg{X: left + 4, Y: top + 2, Button: tea.MouseLeft})
		if m.selected != nil {
			t.Fatalf("a press at %d, %d is not on the history", at.x, at.y)
		}
	}
	// A press of the left button off the history, and one of the right
	// button on it, begin no drag.
	for _, press := range []tea.MouseClickMsg{{X: left + 2, Y: below + 2, Button: tea.MouseLeft}, {X: left + 2, Y: top + 1, Button: tea.MouseRight}} {
		mouseAt(m, press)
		mouseAt(m, tea.MouseMotionMsg{X: left + 4, Y: top + 2, Button: tea.MouseLeft})
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
		left, top := m.transcriptLeft(), m.historyTop()
		mouseAt(m, tea.MouseClickMsg{X: left + 2, Y: top + 1, Button: tea.MouseLeft})
		mouseAt(m, tea.MouseMotionMsg{X: left + 4, Y: top + 4, Button: tea.MouseLeft})
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
	left, top := m.transcriptLeft(), m.historyTop()
	// "ping" ends 4 cells into the block of the owner, whose lines begin at
	// the inset of the message; "pong" is line 4.
	at := inset(m, 1)
	mouseAt(m, tea.MouseClickMsg{X: left + at + 4, Y: top + 1, Button: tea.MouseLeft})
	mouseAt(m, tea.MouseMotionMsg{X: left + 1, Y: top + 4, Button: tea.MouseLeft})
	lines := m.highlight(strings.Split(m.history.View(), "\n"))
	marked := "\x1b[7m"
	if strings.Contains(lines[1], marked) || !strings.Contains(lines[3], marked) ||
		!strings.HasPrefix(lines[4], marked+"po\x1b[m") || !strings.HasPrefix(ansi.Strip(lines[4]), "pong") {
		t.Fatalf("marked %q", lines[:5])
	}
	agent := ansi.Strip(m.entryHead(m.chat.entries[1]))
	if got := terminalGot(t, dragged(m, left+at, top+1, left+at, top+m.history.Height()-1)); got != "ping\n\n"+agent+"\npong" {
		t.Fatalf("copied %q", got)
	}
}

// The suggestions of a command pasted while the button is down take rows of
// the history: a drag below it ends on its last row.
func TestDragWhileSuggestionsShrinkTheHistory(t *testing.T) {
	clipboardOf(t)
	m, b := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	b.reply = numbered(1000)
	answered(t, m, say(t, m, "ping"))
	m.history.GotoTop()
	left, top := m.transcriptLeft(), m.historyTop()
	height := m.history.Height()
	mouseAt(m, tea.MouseClickMsg{X: left, Y: top, Button: tea.MouseLeft})
	m.Update(tea.PasteMsg{Content: "/"})
	shown := m.history.Height()
	if shown >= height {
		t.Fatalf("%d suggestions leave the history %d of %d rows", len(m.matching("/")), shown, height)
	}
	below := top + height - 1
	mouseAt(m, tea.MouseMotionMsg{X: left, Y: below, Button: tea.MouseLeft})
	got := terminalGot(t, mouseAt(m, tea.MouseReleaseMsg{X: left, Y: below, Button: tea.MouseLeft}))
	if lines := strings.Split(got, "\n"); len(lines) != shown {
		t.Fatalf("copied %d lines of %d shown: %q", len(lines), shown, got)
	}
}
