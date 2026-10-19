package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// /effort and /model open a dialog in the middle of the chat: the chat stays
// behind it, faded, and the window keeps its size.
func TestDialogLiesOverTheChat(t *testing.T) {
	for _, open := range []string{"/effort", "/model", "/suggestions"} {
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

// Help uses the same centered overlay and preserves the chat beneath it.
func TestHelpOpensInDialog(t *testing.T) {
	for _, size := range [][2]int{{100, 32}, {60, 12}} {
		m, _ := newModel(t)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		fill(m, 80)
		at := m.history.YOffset()
		run(m, "/help")
		if !m.inDialog() {
			t.Fatal("help should open a dialog")
		}
		view := ansi.Strip(m.View().Content)
		if len(strings.Split(view, "\n")) != size[1] || !strings.Contains(view, "Commands") {
			t.Fatalf("dialog does not fit: %s", view)
		}
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
		if m.history.YOffset() != at {
			t.Fatal("help should hold the chat still")
		}
		press(m, "esc")
		if m.inDialog() || m.screen != screenChat {
			t.Fatal("esc should close help")
		}
	}
}

func TestDialogDoesNotWrap(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 32})
	run(m, "/help")
	view := ansi.Strip(m.helpView())
	if strings.Contains(view, "much more") || !strings.Contains(view, "…") {
		t.Fatalf("long descriptions should be truncated, not wrapped: %s", view)
	}
}

// ctrl+k and /keys open the dialog that lists the keys of the chat.
func TestKeysOpenInDialog(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	m.input.SetValue("typed")
	m.Update(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	if m.screen != screenKeys || !m.inDialog() || m.input.Value() != "typed" {
		t.Fatalf("ctrl+k should open the keys and keep what is typed: screen %v, input %q", m.screen, m.input.Value())
	}
	view := ansi.Strip(m.View().Content)
	for _, k := range m.keybindings() {
		if !strings.Contains(view, k.keys) || !strings.Contains(view, k.help) {
			t.Fatalf("the dialog lacks %q:\n%s", k.keys, view)
		}
	}
	if strings.Contains(view, "shift+tab") {
		t.Fatal("shift+tab is a key of a UI of several profiles only")
	}
	if help := strings.TrimSpace(footer(m)); help != "esc back" {
		t.Fatalf("footer %q", help)
	}
	if press(m, "x"); m.screen != screenKeys || m.input.Value() != "typed" {
		t.Fatal("only esc leaves the keys")
	}
	if press(m, "esc"); m.screen != screenChat {
		t.Fatal("esc should close the keys")
	}

	m.input.Reset()
	if run(m, "/keys"); m.screen != screenKeys {
		t.Fatal("/keys should open the keys")
	}
	m.profileOf = 2
	if !strings.Contains(ansi.Strip(m.keysView()), "shift+tab") {
		t.Fatal("several profiles: shift+tab shows the next")
	}
}

// ctrl+s and /session list the conversations in a dialog over the chat, like
// the model and the settings: what is typed stays, and the chat stays behind.
func TestSessionsOpenInDialog(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	m.chat.title = "about the cache"
	m.chat.add(entry{mine: true, text: "how does the cache work?", at: m.now()})
	m.input.SetValue("typed")
	at := m.history.YOffset()
	m.Update(ctrlS)
	if !m.inDialog() || m.input.Value() != "typed" {
		t.Fatalf("ctrl+s should open the conversations as a dialog and keep what is typed: screen %v, input %q", m.screen, m.input.Value())
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "╭") || !strings.Contains(view, "Sessions") || !strings.Contains(view, "▸ about the cache  · shown") {
		t.Fatalf("the conversations should be a dialog over the chat:\n%s", view)
	}
	if footer(m) == "" {
		t.Fatal("the footer should say what the dialog does")
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.history.YOffset() != at {
		t.Fatal("the dialog should hold the chat still")
	}
	if press(m, "esc"); m.screen != screenChat || m.inDialog() {
		t.Fatal("esc should close the conversations")
	}
}

// A dialog is as wide as its size says, whatever it is given: a line that is
// too long is cut, not wrapped.
func TestDialogCutsLongLines(t *testing.T) {
	m, _ := newModel(t)
	m.screen = screenKeys
	w, _ := m.dialogSize()
	rows := strings.Split(ansi.Strip(m.dialog([]string{strings.Repeat("x", 2*w), "short"})), "\n")
	if len(rows) != 4 {
		t.Fatalf("%d rows:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	for _, row := range rows {
		if lipgloss.Width(row) != w {
			t.Fatalf("a row of %d cells in a dialog of %d: %q", lipgloss.Width(row), w, row)
		}
	}
	if want := "│ " + strings.Repeat("x", w-5) + "… │"; rows[1] != want {
		t.Fatalf("the long line: %q, want %q", rows[1], want)
	}
}

// A UI of one profile names no other: its header counts none, and the keys
// list none to show next.
func TestOneProfileIsNotCounted(t *testing.T) {
	m, _ := newModel(t)
	for of, counted := range map[int]bool{0: false, 1: false, 2: true} {
		m.profileAt, m.profileOf = 1, of
		if got := strings.Contains(ansi.Strip(m.headerView()), fmt.Sprintf("[1/%d]", of)); got != counted {
			t.Errorf("of %d profiles: the header counts them: %v", of, got)
		}
		if got := strings.Contains(ansi.Strip(m.keysView()), "shift+tab"); got != counted {
			t.Errorf("of %d profiles: the keys list shift+tab: %v", of, got)
		}
	}
}

// Beside a dialog, a line of wide letters stays as it is: the dialog lies
// over it where it belongs, the chat goes on right of it up to its border,
// and the window keeps its size.
func TestDialogOverWideLetters(t *testing.T) {
	m, _ := newModel(t)
	for range 30 {
		m.chat.add(entry{text: strings.Repeat("两个", 40)})
	}
	m.renderHistory(true)
	run(m, "/effort")
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	if len(lines) != m.height {
		t.Fatalf("the window is %d lines high, the view %d", m.height, len(lines))
	}
	w, _ := m.dialogSize()
	x := (m.width - w) / 2
	framed, beside := 0, 0
	for i, l := range lines {
		if ansi.StringWidth(l) > m.width {
			t.Errorf("line %d is wider than the window: %q", i, l)
		}
		if strings.ContainsAny(ansi.Cut(l, 0, x), "两个") && ansi.Cut(l, x, x+1) == "│" && ansi.Cut(l, x+w-1, x+w) == "│" {
			framed++
			right := ansi.Cut(l, x+w, m.width)
			if strings.ContainsAny(right, "两个") {
				beside++
			}
			if !strings.HasSuffix(right, "│") {
				t.Errorf("line %d lost the border of the chat: %q", i, l)
			}
		}
	}
	if beside == 0 {
		t.Errorf("the chat right of the dialog is lost:\n%s", strings.Join(lines, "\n"))
	}
	if framed == 0 {
		t.Fatalf("the dialog should lie over the wide letters:\n%s", strings.Join(lines, "\n"))
	}
}

// What a dialog covers of the chat is blank behind it, and only that: the
// chat shows neither through it nor not around it.
func TestDialogCoversTheChat(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 60})
	m.chat.add(entry{text: strings.Repeat("% ", 2000)})
	m.renderHistory(true)
	run(m, "/effort")
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	w, _ := m.dialogSize()
	x := (m.width - w) / 2
	top, bottom := -1, -1
	for i, l := range lines {
		switch ansi.Cut(l, x, x+1) {
		case "╭":
			top = i
		case "╰":
			bottom = i
		}
	}
	if top < 0 || bottom <= top || bottom+1 >= len(lines) {
		t.Fatalf("the dialog from %d to %d:\n%s", top, bottom, strings.Join(lines, "\n"))
	}
	for i := top; i <= bottom; i++ {
		if strings.Contains(ansi.Cut(lines[i], x, x+w), "%") {
			t.Errorf("the chat shows through line %d of the dialog: %q", i, lines[i])
		}
		if !strings.Contains(ansi.Cut(lines[i], 0, x), "%") || !strings.Contains(ansi.Cut(lines[i], x+w, m.width), "%") {
			t.Errorf("the chat is blank beside line %d of the dialog: %q", i, lines[i])
		}
	}
	for _, i := range []int{top - 1, bottom + 1} {
		if !strings.Contains(ansi.Cut(lines[i], x, x+w), "%") {
			t.Errorf("the chat is blank at line %d, above or below the dialog: %q", i, lines[i])
		}
	}
}
