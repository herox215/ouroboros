package tui

import (
	"errors"
	"os/exec"
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

// footer is the last line of the view, without colors.
func footer(m *Model) string {
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	return lines[len(lines)-1]
}

// shortRefresh makes the UI refresh at once instead of every ten seconds.
func shortRefresh(t *testing.T) {
	t.Helper()
	refreshInterval = time.Millisecond
	t.Cleanup(func() { refreshInterval = 10 * time.Second })
}

func TestInitAndTicks(t *testing.T) {
	m, b := newModel(t)
	if m.Init() == nil || m.splashStart.IsZero() {
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
	b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "pk1"}}}
	_, cmd := m.Update(msg)
	settle(m, cmd)
	if m.status.Name != "renamed" || len(m.trust.Incoming) != 1 {
		t.Fatalf("status %+v, requests %+v", m.status, m.trust)
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
		"stop for the setup": {"stop", func(m *Model, _ *fakeBackend) tea.Cmd { return press(m, "c") }, "boom"},
		"setup":              {"setup", func(m *Model, _ *fakeBackend) tea.Cmd { return press(m, "c") }, "boom"},
	} {
		t.Run(name, func(t *testing.T) {
			m, b := commands(t)
			b.fail = map[string]error{test.fail: boom}
			m.Update(test.do(m, b)())
			notice(t, m, test.want)
		})
	}

	// A decision that fails for another reason than the contacts says why.
	m, b := commands(t)
	b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "pk1"}}}
	b.acceptErr = boom
	m.Update(m.loadContacts())
	press(m, "R")
	m.Update(press(m, "a")())
	notice(t, m, "boom")
}

func TestSetup(t *testing.T) {
	m, _ := commands(t)
	if _, cmd := m.Update(setupMsg{exec.Command("true")}); cmd == nil {
		t.Fatal("the setup should be run with the terminal handed over")
	}
	failed := errors.New("exit status 1")
	if msg := setupDone(failed).(actionMsg); msg.err != failed {
		t.Fatalf("got %+v", msg)
	}
	if _, refresh := m.Update(setupDone(nil)); refresh == nil || m.notice != "node configured and started" || m.noticeErr {
		t.Fatalf("notice %q", m.notice)
	}
}

// What is neither a key nor an answer goes where it belongs.
func TestOtherMessages(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("0")})
	if m.light {
		t.Fatal("a black background is dark")
	}
	m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	if !m.light {
		t.Fatal("a white background is light")
	}
	if _, cmd := m.Update(m.spinner.Tick()); cmd == nil {
		t.Fatal("the spinner should keep turning")
	}
	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m.Update(struct{}{}) // for the input, which ignores it
	if _, cmd := m.Update(splashMsg{m.now()}); cmd != nil {
		t.Fatal("a splash that is closed is not animated")
	}
	// A step of a conversation that was reset is dropped.
	m.step(stepMsg{contextID: "old", step: Step{Text: "asking beta"}})
	if m.chat.doing != "" {
		t.Fatalf("doing %q", m.chat.doing)
	}
	if view := New(&fakeBackend{}, nil, nil).View(); view.Content != "" {
		t.Fatalf("without a window there is nothing to draw: %q", view.Content)
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
	if press(m, "tab"); m.focus != focusCommands {
		t.Fatal("tab should leave the input")
	}
	if press(m, "tab"); m.focus != focusInput {
		t.Fatal("tab should focus the input")
	}
	if press(m, "pgup", "pgdown") != nil {
		t.Fatal("scrolling runs nothing")
	}
}

func TestRequestsScreen(t *testing.T) {
	m, b := commands(t)
	press(m, "R")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "No pending requests.") {
		t.Fatalf("view:\n%s", view)
	}
	if press(m, "down"); m.reqSel != 0 || press(m, "a") != nil || press(m, "x") != nil {
		t.Fatal("without a request there is nothing to select or decide")
	}

	b.trust = Contacts{
		Incoming: []plugin.Request{{PublicKey: "0123456789abcdef"}, {PublicKey: "pk-short", Name: "gamma"}},
		Outgoing: []plugin.Request{{PublicKey: "pk-out", Name: "delta"}},
	}
	m.Update(m.loadContacts())
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"0123456789…", "gamma", "Waiting for the other side:", "delta (pk-out)"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	for _, step := range []struct {
		key  string
		want int
	}{{"down", 1}, {"j", 1}, {"up", 0}, {"k", 0}, {"j", 1}} {
		if press(m, step.key); m.reqSel != step.want {
			t.Fatalf("after %s: selected %d, want %d", step.key, m.reqSel, step.want)
		}
	}
	press(m, "a")()
	if len(b.accepted) != 1 || b.accepted[0] != "pk-short" {
		t.Fatalf("accepted %v", b.accepted)
	}
}

func TestHeaderAndSmallWindows(t *testing.T) {
	m, b := newModel(t)
	b.status.Running = false
	m.Update(m.refreshStatus())
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "○ offline") || !strings.Contains(view, "press s to start") {
		t.Fatalf("view:\n%s", view)
	}
	// A window too small for the logo shows the hint alone.
	b.status = Status{}
	m.Update(m.refreshStatus())
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 10})
	if view := m.View().Content; !strings.Contains(view, "Press c to set up") || strings.Contains(view, strings.TrimSpace(logoLines[1])) {
		t.Fatalf("view:\n%s", view)
	}
}
