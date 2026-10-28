package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// settle runs cmd, and what it batches, and hands the UI what comes of it.
// What the UI wants to do next is left alone.
func settle(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			settle(m, c)
		}
		return
	}
	m.Update(msg)
}

// footer is what the last line of the view says on its right, without
// colors: what there is to notice, and the keys.
func footer(m *Model) string {
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	hint := ansi.Strip(m.footerHint())
	for _, l := range lines {
		if hint != "" && strings.Contains(l, hint) {
			return hint
		}
	}
	return strings.TrimRight(lines[len(lines)-1], " ")
}

// shortRefresh makes the UI refresh at once instead of every ten seconds.
func shortRefresh(t *testing.T) {
	t.Helper()
	refreshInterval = time.Millisecond
	t.Cleanup(func() { refreshInterval = 10 * time.Second })
}

func TestInitAndTicks(t *testing.T) {
	m, b := newModel(t)
	if m.Init() == nil || m.splash.start.IsZero() {
		t.Fatal("the UI should start with the splash and its first requests")
	}
	if _, ok := splashTick()().(splashMsg); !ok {
		t.Fatal("the splash should be animated")
	}

	// Every interval the status and the requests are read anew.
	shortRefresh(t)
	msg := tick()()
	if _, ok := msg.(tickMsg); !ok {
		t.Fatalf("got %T", msg)
	}
	b.status.Name = "renamed"
	b.trust = Contacts{Suggestions: []plugin.Suggestion{{ID: "s1"}}}
	_, cmd := m.Update(msg)
	settle(m, cmd)
	if m.status.Name != "renamed" || len(m.suggestionRequests.trust.Suggestions) != 1 {
		t.Fatalf("status %+v, requests %+v", m.status, m.suggestionRequests.trust)
	}
}

// What the backend cannot do is shown, and the UI goes on.
func TestBackendErrors(t *testing.T) {
	boom := errors.New("boom")
	notice := func(t *testing.T, m *Model, want string) {
		t.Helper()
		if m.notice != want || !m.noticeErr || !strings.Contains(footer(m), want) {
			t.Fatalf("notice %q (error %v), want %q", m.notice, m.noticeErr, want)
		}
	}
	for name, test := range map[string]struct {
		fail string
		do   func(m *Model, b *fakeBackend) tea.Cmd
		want string
	}{
		"status":   {"status", func(m *Model, _ *fakeBackend) tea.Cmd { return m.refreshStatus }, "boom"},
		"contacts": {"contacts", func(m *Model, _ *fakeBackend) tea.Cmd { return m.loadContacts }, "contacts: boom"},
		"start": {"start", func(m *Model, b *fakeBackend) tea.Cmd {
			b.status.Running = false
			return m.goOnline
		}, "boom"},
	} {
		t.Run(name, func(t *testing.T) {
			m, b := newModel(t)
			b.fail = map[string]error{test.fail: boom}
			m.Update(test.do(m, b)())
			notice(t, m, test.want)
		})
	}

	// A decision that fails for another reason than the contacts says why.
	m, b := newModel(t)
	b.trust = Contacts{Suggestions: []plugin.Suggestion{{ID: "s1"}}}
	b.acceptErr = boom
	m.Update(m.loadContacts())
	run(m, "/suggestions")
	m.Update(press(m, "enter")())
	notice(t, m, "boom")
}

// What is neither a key nor an answer goes where it belongs.
func TestOtherMessages(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("0")})
	if m.light {
		t.Fatal("a black background is dark")
	}
	// Once the UI paints the terminal, it answers with the palette: a later
	// answer tells nothing of it.
	m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	if m.light || m.terminalLight {
		t.Fatal("a later answer changed the look")
	}
	if _, cmd := m.Update(m.spinner.Tick()); cmd == nil {
		t.Fatal("the spinner should keep turning")
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m.Update(struct{}{}) // for the input, which ignores it
	if _, cmd := m.Update(splashMsg{m.now()}); cmd != nil {
		t.Fatal("a splash that is closed is not animated")
	}
	// What happens in another session leaves the one shown as it is.
	m.Update(eventMsg{id: "other", event: Event{Kind: plugin.SessionMessage, Text: "hi"}})
	m.Update(eventMsg{id: "other", event: Event{Kind: plugin.SessionStep, Text: "asking beta"}})
	if m.chat.doing != "" || m.chats["other"].doing != "asking beta" {
		t.Fatalf("doing %q", m.chat.doing)
	}
	if view := New(&fakeBackend{}, nil, nil).View(); view.Content != "" {
		t.Fatalf("without a window there is nothing to draw: %q", view.Content)
	}
}

// The input never paints its cursor line: a background there shows as a black
// bar on the line that is being typed, in both themes.
func TestInputCursorLineHasNoBackground(t *testing.T) {
	for _, light := range []bool{false, true} {
		s := inputStyles(light)
		focused, blurred := s.Focused.CursorLine.GetBackground(), s.Blurred.CursorLine.GetBackground()
		if focused != (lipgloss.NoColor{}) || blurred != (lipgloss.NoColor{}) {
			t.Fatalf("light %v: the cursor line must have no background, got %v and %v", light, focused, blurred)
		}
	}
}

func TestChatKeys(t *testing.T) {
	m, _ := newModel(t)
	if press(m, "h", "i"); m.input.Value() != "hi" {
		t.Fatalf("typed %q", m.input.Value())
	}
	m.input.SetValue("   ")
	if press(m, "enter") != nil || len(m.chat.entries) != 0 {
		t.Fatal("nothing is not a message")
	}
	if press(m, "pgup", "pgdown", "esc") != nil || m.input.Value() != "   " {
		t.Fatal("scrolling and esc run nothing")
	}
}

func TestHeaderAndSmallWindows(t *testing.T) {
	m, b := newModel(t)
	b.status.Running = false
	m.Update(m.refreshStatus())
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "online") || strings.Contains(view, "offline") || !strings.Contains(view, "type /start") {
		t.Fatalf("view:\n%s", view)
	}
	// A window too small for the logo shows the hint alone.
	b.status = Status{}
	m.Update(m.refreshStatus())
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 10})
	if view := m.View().Content; !strings.Contains(view, "Type /setup to set up") || strings.Contains(view, strings.TrimSpace(logoLines[1])) {
		t.Fatalf("view:\n%s", view)
	}
}

// A line that only looks like a task, without its number or what it does,
// is shown as it is.
func TestTaskPartsOfOddLines(t *testing.T) {
	for _, line := range []string{"task 3 without a dot", "task three · read the tests", "no task at all"} {
		if n, what := taskParts(line); n != 0 || what != line {
			t.Fatalf("%q is task %d, %q", line, n, what)
		}
	}
	if n, what := taskParts("task 3 · read the tests"); n != 3 || what != "read the tests" {
		t.Fatalf("a task is %d, %q", n, what)
	}
}

// Text that is no label stands for no file, and a conversation without a
// message of the owner has no exchange an entry answers.
func TestNoLabelAndNoExchange(t *testing.T) {
	m, _ := newModel(t)
	if path, ok := m.chat.attachment("no label"); ok || path != "" {
		t.Fatalf("no label stands for %q", path)
	}
	m.chat.entries = []entry{{text: "a note of the node"}}
	if i := m.chat.exchangeOf(0); i != -1 {
		t.Fatalf("the note answers %d", i)
	}
}

// A window with no room above the composer shows no question there.
func TestQuestionWithoutRoom(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 4})
	if m.panelHeight() != 0 {
		t.Fatalf("the panel has %d rows", m.panelHeight())
	}
	if lines, hits := m.questionLines(&asking{question: "Which database?", options: []string{"SQLite"}}, 40); lines != nil || hits != nil {
		t.Fatalf("a question without room: %q", lines)
	}
}

// A message that waits for the node keeps the keys at the right of the line
// below, after what it says of the wait.
func TestWaitingMessageKeepsTheKeys(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	m.jobsShown = true
	m.chat.awaiting = &awaitingSend{text: "hello", typed: "hello", since: m.now()}
	if hint := ansi.Strip(m.footerHint()); !strings.Contains(hint, "goes out when it is back  │  ") || !strings.Contains(hint, jobsKey+" hide") {
		t.Fatalf("the line says %q", hint)
	}
}

// A click on the chat takes a sixel that was drawn away with the screen
// drawn anew, and does what the click does too.
func TestClickTakesTheSixelAway(t *testing.T) {
	m, _ := newModel(t)
	m.hover = hover{x: 1, y: 1, view: "a picture", sixel: "sixel", drawn: true}
	cmd := m.mouse(tea.MouseClickMsg{X: 5, Y: 5, Button: tea.MouseLeft})
	if cmd == nil || m.hover.drawn {
		t.Fatal("the sixel stays")
	}
}
