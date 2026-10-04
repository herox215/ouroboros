package tui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

type sent struct {
	contextID string
	text      string
}

type fakeBackend struct {
	status        Status
	sent          []sent
	steps         []Step // reported before the reply
	reply         string
	sendErr       error
	started       int
	stopped       int
	setupCommands int

	trust     Contacts
	accepted  []string
	rejected  []string
	acceptErr error
	rejectErr error
	decided   context.Context // what a decision on a request ran with

	// fail is what fails, by the name of the request.
	fail map[string]error

	// ask, if set, answers instead of steps and reply.
	ask func(ctx context.Context, step func(Step)) (string, error)
}

func (f *fakeBackend) Status() (Status, error)     { return f.status, f.fail["status"] }
func (f *fakeBackend) Start() error                { f.started++; return f.fail["start"] }
func (f *fakeBackend) Stop() error                 { f.stopped++; return f.fail["stop"] }
func (f *fakeBackend) Contacts() (Contacts, error) { return f.trust, f.fail["contacts"] }
func (f *fakeBackend) SetupCommand() (*exec.Cmd, error) {
	f.setupCommands++
	return exec.Command("true"), f.fail["setup"]
}
func (f *fakeBackend) Ask(ctx context.Context, contextID, text string, step func(Step)) (string, error) {
	f.sent = append(f.sent, sent{contextID, text})
	if f.ask != nil {
		return f.ask(ctx, step)
	}
	for _, s := range f.steps {
		step(s)
	}
	return f.reply, f.sendErr
}
func (f *fakeBackend) Accept(ctx context.Context, publicKey string) error {
	f.decided = ctx
	f.accepted = append(f.accepted, publicKey)
	return f.acceptErr
}
func (f *fakeBackend) Reject(ctx context.Context, publicKey string) error {
	f.decided = ctx
	f.rejected = append(f.rejected, publicKey)
	return f.rejectErr
}

func newModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	b := &fakeBackend{status: Status{Configured: true, Running: true, ID: "aaa111", Name: "alpha"}, reply: "pong"}
	m := New(b, nil, nil)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.showSplash = false // dismiss the start screen; its own test is below
	m.Update(m.refreshStatus())
	return m, b
}

// commands is a model whose keys are commands rather than text for the input.
func commands(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	m, b := newModel(t)
	press(m, "esc")
	return m, b
}

func press(m *Model, keys ...string) tea.Cmd {
	var cmds []tea.Cmd
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg.Code = tea.KeyEnter
		case "esc":
			msg.Code = tea.KeyEscape
		case "tab":
			msg.Code = tea.KeyTab
		case "pgup":
			msg.Code = tea.KeyPgUp
		case "pgdown":
			msg.Code = tea.KeyPgDown
		case "up":
			msg.Code = tea.KeyUp
		case "down":
			msg.Code = tea.KeyDown
		default:
			msg.Code, msg.Text = rune(k[0]), k
		}
		_, cmd := m.Update(msg)
		cmds = append(cmds, cmd)
	}
	return cmds[len(cmds)-1]
}

// say types text into the chat and sends it.
func say(t *testing.T, m *Model, text string) tea.Cmd {
	t.Helper()
	if m.focus != focusInput {
		press(m, "enter")
	}
	m.input.SetValue(text)
	cmd := press(m, "enter")
	if cmd == nil {
		t.Fatal("sending should return a command")
	}
	return cmd
}

// deliver runs cmd and the commands that follow it until the reply is in.
func deliver(m *Model, cmd tea.Cmd) {
	for cmd != nil {
		_, cmd = m.Update(cmd())
	}
}

func TestSplashShowsLogoUntilKey(t *testing.T) {
	m, _ := newModel(t)
	m.showSplash = true
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := m.View().Content
	if !strings.Contains(view, strings.TrimSpace(splashLogo(0)[1])) || !strings.Contains(view, "ouroboros") {
		t.Fatalf("splash should show the logo and the title:\n%s", view)
	}
	if strings.Contains(view, "No messages yet") {
		t.Fatal("splash should hide the normal view")
	}

	if cmd := press(m, "x"); cmd != nil {
		t.Fatal("closing the splash should not run a command")
	}
	if m.showSplash {
		t.Fatal("a key should close the splash")
	}
	if view := m.View().Content; !strings.Contains(view, "No messages yet") {
		t.Fatal("after the splash the normal view should be shown")
	}
}

func TestSplashClosesAfterTimeout(t *testing.T) {
	m, _ := newModel(t)
	m.showSplash = true
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.splashStart = time.Unix(0, 0)
	if _, cmd := m.Update(splashMsg{m.splashStart.Add(splashDuration / 2)}); cmd == nil || !m.showSplash {
		t.Fatal("the splash should keep animating until its time is up")
	}
	m.Update(splashMsg{m.splashStart.Add(splashDuration)})
	if m.showSplash {
		t.Fatal("the splash should close when its time is up")
	}
	if view := m.View().Content; !strings.Contains(view, "No messages yet") {
		t.Fatal("after the splash the normal view should be shown")
	}
}

func TestSplashFramesKeepTheirSize(t *testing.T) {
	width := ansi.StringWidth(logoLines[0])
	for at := time.Duration(0); at <= splashDuration; at += splashFrame {
		frame := splashLogo(at)
		if len(frame) != len(logoLines) {
			t.Fatalf("frame at %v has %d lines, want %d", at, len(frame), len(logoLines))
		}
		for _, l := range frame {
			if ansi.StringWidth(l) != width {
				t.Fatalf("frame at %v has a line of width %d, want %d", at, ansi.StringWidth(l), width)
			}
		}
	}
	if slices.Equal(splashLogo(0), splashLogo(splashDuration)) {
		t.Fatal("the splash should animate")
	}
}

func TestSplashCtrlCQuits(t *testing.T) {
	m, _ := newModel(t)
	m.showSplash = true
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("ctrl+c should quit")
	}
	if !m.showSplash {
		t.Fatal("ctrl+c should keep the splash and quit instead")
	}
}

func TestUnconfiguredNodeShowsLogoAndNoChat(t *testing.T) {
	m, b := newModel(t)
	b.status = Status{}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m.Update(m.refreshStatus())
	view := m.View().Content
	if !strings.Contains(view, strings.TrimSpace(logoLines[1])) || !strings.Contains(view, "Press c to set up") {
		t.Fatalf("an unconfigured node should show the logo and the setup hint:\n%s", view)
	}
	if press(m, "esc", "enter"); m.focus == focusInput || !m.noticeErr {
		t.Fatal("without a configured node there is nobody to chat with")
	}
}

func TestChatIsFocusedOnStart(t *testing.T) {
	m, _ := newModel(t)
	if m.focus != focusInput {
		t.Fatal("a configured node should open with the chat input focused")
	}
	press(m, "esc")
	m.Update(m.refreshStatus())
	if m.focus != focusCommands {
		t.Fatal("a later status refresh must not steal the focus")
	}
}

func TestViewIsTheChatOnly(t *testing.T) {
	m, _ := newModel(t)
	view := m.View().Content
	for _, want := range []string{"● online", "alpha", "No messages yet"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q", want)
		}
	}
	if strings.Contains(view, "Nodes") {
		t.Errorf("the peers have no place in the chat:\n%s", view)
	}
}

func TestChat(t *testing.T) {
	m, b := newModel(t) // the agent needs no other node
	cmd := say(t, m, "  ping  ")
	if !m.chat.pending || m.input.Value() != "" {
		t.Fatal("message should be pending and the input cleared")
	}
	if !strings.Contains(m.View().Content, "alpha is thinking") {
		t.Fatal("a pending answer should be announced")
	}
	m.input.SetValue("again")
	if press(m, "enter") != nil || len(m.chat.entries) != 1 {
		t.Fatal("a second message must wait for the reply")
	}

	deliver(m, cmd)
	c := m.chat
	if len(b.sent) != 1 || b.sent[0] != (sent{c.contextID, "ping"}) {
		t.Fatalf("sent %+v", b.sent)
	}
	if c.pending || len(c.entries) != 2 || c.entries[1].text != "pong" || c.entries[1].mine {
		t.Fatalf("entries %+v", c.entries)
	}
	if view := m.View().Content; !strings.Contains(view, "ping") || !strings.Contains(view, "pong") {
		t.Fatal("history should show both messages")
	}

	// The conversation keeps its context.
	deliver(m, say(t, m, "more"))
	if b.sent[1].contextID != c.contextID {
		t.Fatal("context changed within a conversation")
	}
}

func TestChatKeepsTheAgentsTalkToPeersOutOfTheHistory(t *testing.T) {
	m, b := newModel(t)
	b.steps = []Step{{Text: "asking beta"}, {Text: "asking beta", Done: true}}
	b.reply = "beta says it is noon"
	cmd := say(t, m, "ask beta for the time")

	_, cmd = m.Update(cmd()) // the question to beta is on its way
	if m.chat.doing != "asking beta" || !m.chat.pending || !strings.Contains(m.View().Content, "asking beta…") {
		t.Fatalf("the call to beta should be announced: %+v", m.chat)
	}
	_, cmd = m.Update(cmd()) // beta answered, the agent thinks again
	if m.chat.doing != "" || !strings.Contains(m.View().Content, "alpha is thinking") {
		t.Fatalf("after the peer's answer the agent thinks: %+v", m.chat)
	}
	deliver(m, cmd)

	c := m.chat
	if c.pending || len(c.entries) != 2 || c.entries[1].text != "beta says it is noon" {
		t.Fatalf("only the user and the agent belong in the history: %+v", c.entries)
	}
}

func TestSendError(t *testing.T) {
	m, b := newModel(t)
	b.sendErr = errors.New("connection refused")
	deliver(m, say(t, m, "ping"))
	e := m.chat.entries[1]
	if !e.failed || e.text != "connection refused" {
		t.Fatalf("entry %+v", e)
	}
}

func TestConnectionRequests(t *testing.T) {
	m, b := commands(t)
	b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "pk1", Name: "gamma", Addr: "1.2.3.4:9"}}}
	m.Update(m.loadContacts())
	if view := m.View().Content; !strings.Contains(view, "✉ 1") {
		t.Fatalf("header should show the pending count:\n%s", view)
	}

	press(m, "R")
	if m.screen != screenRequests {
		t.Fatal("R should open the requests screen")
	}
	if view := m.View().Content; !strings.Contains(view, "gamma") || !strings.Contains(view, "wants to chat") {
		t.Fatalf("requests view:\n%s", view)
	}

	cmd := press(m, "a")
	if cmd == nil {
		t.Fatal("accept should return a command")
	}
	m.Update(cmd())
	if len(b.accepted) != 1 || b.accepted[0] != "pk1" {
		t.Fatalf("accepted %v", b.accepted)
	}

	press(m, "esc")
	if m.screen != screenChat {
		t.Fatal("esc should go back to the chat")
	}
}

func TestRejectRequest(t *testing.T) {
	m, b := commands(t)
	b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "pk1", Name: "gamma"}}}
	m.Update(m.loadContacts())
	press(m, "R")
	cmd := press(m, "x")
	if cmd == nil {
		t.Fatal("reject should return a command")
	}
	m.Update(cmd())
	if len(b.rejected) != 1 || b.rejected[0] != "pk1" {
		t.Fatalf("rejected %v", b.rejected)
	}
}

func TestNewConversationDropsLateReply(t *testing.T) {
	m, b := newModel(t)
	b.steps = []Step{{Text: "asking beta"}}
	cmd := say(t, m, "ping")
	old := m.chat.contextID
	press(m, "esc", "n")
	deliver(m, cmd)
	c := m.chat
	if c.contextID == old || len(c.entries) != 0 || c.pending || c.doing != "" {
		t.Fatalf("conversation not reset: %+v", c)
	}
}

// askUntilCancelled makes b answer the way a real backend does when it is
// stopped: it asks beta, works until its context ends and fails with that.
// The returned channel is closed once the answer has given up.
func askUntilCancelled(b *fakeBackend) <-chan struct{} {
	stopped := make(chan struct{})
	b.ask = func(ctx context.Context, step func(Step)) (string, error) {
		step(Step{Text: "asking beta"})
		<-ctx.Done()
		step(Step{Text: "asking beta", Done: true}) // nobody listens any more; must not block
		close(stopped)
		return "", ctx.Err()
	}
	return stopped
}

// awaitStop fails the test unless the pending answer gives up soon.
func awaitStop(t *testing.T, stopped <-chan struct{}) {
	t.Helper()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the pending answer should be cancelled")
	}
}

func TestNewConversationCancelsThePendingAnswer(t *testing.T) {
	m, b := newModel(t)
	stopped := askUntilCancelled(b)
	_, cmd := m.Update(say(t, m, "ping")()) // the question to beta is on its way
	if m.chat.doing != "asking beta" {
		t.Fatalf("the answer should be on its way: %+v", m.chat)
	}

	press(m, "esc", "n")
	awaitStop(t, stopped)
	deliver(m, cmd)
	c := m.chat
	if len(c.entries) != 0 || c.pending || c.doing != "" {
		t.Fatalf("a cancelled answer must not land in the new conversation: %+v", c)
	}
	if view := m.View().Content; strings.Contains(view, context.Canceled.Error()) {
		t.Fatalf("a cancelled answer is no error to show:\n%s", view)
	}

	// The new conversation answers as usual.
	b.ask = nil
	deliver(m, say(t, m, "more"))
	if c := m.chat; c.pending || len(c.entries) != 2 || c.entries[1].text != "pong" || c.entries[1].failed {
		t.Fatalf("entries %+v", c.entries)
	}
}

func TestQuitCancelsThePendingAnswer(t *testing.T) {
	ctrlC := tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})
	for name, quit := range map[string]func(*Model) tea.Cmd{
		"q":                    func(m *Model) tea.Cmd { return press(m, "esc", "q") },
		"ctrl+c":               func(m *Model) tea.Cmd { _, cmd := m.Update(ctrlC); return cmd },
		"ctrl+c on requests":   func(m *Model) tea.Cmd { press(m, "esc", "R"); _, cmd := m.Update(ctrlC); return cmd },
		"ctrl+c on the splash": func(m *Model) tea.Cmd { m.showSplash = true; _, cmd := m.Update(ctrlC); return cmd },
	} {
		t.Run(name, func(t *testing.T) {
			m, b := newModel(t)
			stopped := askUntilCancelled(b)
			_, cmd := m.Update(say(t, m, "ping")())

			if _, ok := quit(m)().(tea.QuitMsg); !ok {
				t.Fatal("the UI should quit")
			}
			awaitStop(t, stopped)
			deliver(m, cmd) // the reply may still arrive before the UI is gone
			if c := m.chat; len(c.entries) != 1 || !c.entries[0].mine {
				t.Fatalf("a cancelled answer is no error to show: %+v", c.entries)
			}
		})
	}
}

func TestQuitCancelsRequestDecisions(t *testing.T) {
	for _, key := range []string{"a", "x"} {
		m, b := commands(t)
		b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "pk1", Name: "gamma"}}}
		m.Update(m.loadContacts())
		press(m, "R")
		press(m, key)()
		if b.decided == nil || b.decided.Err() != nil {
			t.Fatalf("%s: a decision should run with a live context", key)
		}
		m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
		if b.decided.Err() == nil {
			t.Fatalf("%s: quitting should cancel a running decision", key)
		}
	}
}

func TestToggleNode(t *testing.T) {
	m, b := commands(t)
	m.Update(press(m, "s")())
	if b.stopped != 1 {
		t.Fatal("s should stop a running node")
	}
	m.status.Running = false
	m.Update(press(m, "s")())
	if b.started != 1 {
		t.Fatal("s should start a stopped node")
	}
	m.status.Configured = false
	if msg := press(m, "s")(); b.setupCommands != 1 {
		t.Fatalf("s should run the setup for an unconfigured node, got %T", msg)
	}
}

func TestSetupStopsNodeFirst(t *testing.T) {
	m, b := commands(t)
	msg := press(m, "c")()
	if _, ok := msg.(setupMsg); !ok || b.stopped != 1 || b.setupCommands != 1 {
		t.Fatalf("got %T, stopped %d", msg, b.stopped)
	}
}

func TestTinyWindow(t *testing.T) {
	m, _ := newModel(t)
	for _, size := range [][2]int{{1, 1}, {20, 5}, {40, 8}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.showSplash = true
		m.View() // the splash must survive tiny windows
		m.showSplash = false
		m.View()
	}
}

func TestOpeningTheUIStartsTheNode(t *testing.T) {
	m, b := newModel(t)
	if m.Update(m.goOnline()); b.started != 0 {
		t.Fatal("a running node is not started again")
	}
	b.status.Running = false
	if m.Update(m.goOnline()); b.started != 1 {
		t.Fatal("a stopped node should be started with the UI")
	}
	b.status.Configured = false
	if m.Update(m.goOnline()); b.started != 1 || m.noticeErr {
		t.Fatal("a node that is not configured cannot be started and is no error")
	}
}

// A decision that could not be stored is shown as failed, not as done, and
// the request stays where it is, to be decided again.
func TestDecisionThatIsNotSaved(t *testing.T) {
	for key, want := range map[string]string{
		"a": "accepting failed – the contact could not be saved",
		"x": "rejecting failed – the contact could not be saved",
	} {
		m, b := commands(t)
		b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "pk1", Name: "gamma"}}}
		b.acceptErr = fmt.Errorf("%w: open contacts.json.lock: permission denied", plugin.ErrNotSaved)
		b.rejectErr = b.acceptErr
		m.Update(m.loadContacts())
		press(m, "R")

		_, refresh := m.Update(press(m, key)())
		if m.notice != want || !m.noticeErr {
			t.Fatalf("%s: notice %q (error %v)", key, m.notice, m.noticeErr)
		}
		view := ansi.Strip(m.View().Content)
		if !strings.Contains(view, want) || strings.Contains(view, "request accepted") || strings.Contains(view, "request declined") || strings.Contains(view, "permission denied") {
			t.Fatalf("%s: view:\n%s", key, view)
		}
		// The list is read anew and still holds the request.
		if refresh == nil {
			t.Fatalf("%s: a failed decision should reload the requests", key)
		}
		m.Update(m.loadContacts())
		if m.screen != screenRequests || len(m.trust.Incoming) != 1 || !strings.Contains(ansi.Strip(m.View().Content), "gamma") {
			t.Fatalf("%s: the request is gone:\n%s", key, ansi.Strip(m.View().Content))
		}

		// The second try goes through.
		b.acceptErr, b.rejectErr = nil, nil
		m.Update(press(m, key)())
		if len(b.accepted)+len(b.rejected) != 2 || m.noticeErr || !strings.HasPrefix(m.notice, "request ") {
			t.Fatalf("%s: retry: accepted %v, rejected %v, notice %q", key, b.accepted, b.rejected, m.notice)
		}
	}
}
