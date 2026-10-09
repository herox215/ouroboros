package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// /effort and /model open a dialog in the middle of the chat: the chat stays
// behind it, faded, and the window keeps its size.
func TestDialogLiesOverTheChat(t *testing.T) {
	for _, open := range []string{"/effort", "/model"} {
		m, b := newModel(t)
		b.offers = []Models{local("gpt-4o", "gpt-4o-mini")}
		answered(t, m, say(t, m, "ping"))
		deliver(m, run(m, open))
		if !m.inDialog() {
			t.Fatalf("%s should open a dialog: screen %v", open, m.screen)
		}
		view := m.View().Content
		lines := strings.Split(view, "\n")
		if len(lines) != m.height {
			t.Fatalf("%s: the window is %d lines high, the view %d:\n%s", open, m.height, len(lines), ansi.Strip(view))
		}
		for i, l := range lines {
			if lipgloss.Width(l) > m.width {
				t.Errorf("%s: line %d is wider than the window: %q", open, i, ansi.Strip(l))
			}
		}

		// The chat is behind it, in the color of a faded line only.
		fade := lipgloss.NewStyle().Foreground(fadeColor(false)).Render("x")
		fade = fade[:strings.Index(fade, "x")]
		for _, want := range []string{"ping", "you · "} {
			at := lineWith(lines, want)
			if at < 0 {
				t.Fatalf("%s: the chat should show behind the dialog: %q\n%s", open, want, ansi.Strip(view))
			}
			if l := lines[at]; !strings.HasPrefix(l, fade) || strings.Contains(l, "\x1b[1") || strings.Contains(l, "\x1b[36") {
				t.Errorf("%s: the chat should be faded behind the dialog: %q", open, l)
			}
		}

		// The dialog is in the middle of the body, in the accent color.
		w, _ := m.dialogSize()
		x := (m.width - w) / 2
		top, bottom := -1, -1
		for i, l := range lines {
			switch r := []rune(ansi.Strip(l)); {
			case len(r) > x && r[x] == '╭' && top < 0 && i > 1:
				top = i
			case len(r) > x && r[x] == '╰' && top >= 0 && bottom < 0:
				bottom = i
			}
		}
		if top < 0 || bottom < 0 {
			t.Fatalf("%s: the dialog should begin at column %d:\n%s", open, x, ansi.Strip(view))
		}
		above, below := top-1, m.bodyHeight()-bottom // the header is above the body
		if d := above - below; d < -1 || d > 1 {
			t.Errorf("%s: the dialog should be in the middle: %d lines above it, %d below:\n%s", open, above, below, ansi.Strip(view))
		}
		if !strings.Contains(lines[top], "\x1b[36m╭") {
			t.Errorf("%s: the dialog should be framed in the accent color: %q", open, lines[top])
		}

		// Esc closes it, and the chat is as it was.
		press(m, "esc")
		if m.inDialog() || !strings.Contains(m.View().Content, "\x1b[1;36myou") {
			t.Errorf("%s: esc should close the dialog: screen %v", open, m.screen)
		}
	}
}

// lineWith is the line of lines that contains s, once stripped, or -1.
func lineWith(lines []string, s string) int {
	for i, l := range lines {
		if strings.Contains(ansi.Strip(l), s) {
			return i
		}
	}
	return -1
}

// The chat behind a dialog stays where it is when the wheel turns.
func TestDialogHoldsTheChatStill(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 80)
	run(m, "/effort")
	at := m.history.YOffset()
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.history.YOffset() != at {
		t.Fatalf("the chat moved behind the dialog: from %d to %d", at, m.history.YOffset())
	}
	press(m, "esc")
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.history.YOffset() == at {
		t.Fatal("without the dialog the wheel should scroll the chat")
	}
}

// A dialog on a window too low for it is cut off inside its border.
func TestDialogFitsALowWindow(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 12})
	run(m, "/effort")
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	if len(lines) != 12 {
		t.Fatalf("the view should be as high as the window: %d", len(lines))
	}
	w, _ := m.dialogSize()
	x := (m.width - w) / 2
	bottom := -1
	for i, l := range lines {
		if r := []rune(l); len(r) > x && r[x] == '╰' {
			bottom = i
		}
	}
	if bottom < 0 || bottom > m.bodyHeight() || lineWith(lines, "Effort") < 0 {
		t.Fatalf("the dialog should keep its title and its border:\n%s", strings.Join(lines, "\n"))
	}
}
