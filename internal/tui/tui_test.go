package tui

import (
	"context"
	"errors"
	"fmt"
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
	status  Status
	sent    []sent
	steps   []Step // reported before the reply
	reply   string
	sendErr error
	started int
	stopped int
	setups  int        // how often the setup was opened
	setup   *fakeSetup // what it opens; one that knows the fake endpoint if nil

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
func (f *fakeBackend) Setup() (Setup, error) {
	f.setups++
	if err := f.fail["setup"]; err != nil {
		return nil, err
	}
	if f.setup == nil {
		f.setup = newFakeSetup()
	}
	return f.setup, nil
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
func (f *fakeBackend) AcceptProposal(id string) error {
	f.accepted = append(f.accepted, "proposal "+id)
	return f.acceptErr
}
func (f *fakeBackend) RefuseProposal(id string) error {
	f.rejected = append(f.rejected, "proposal "+id)
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

// run types the command line into the input and sends it.
func run(m *Model, line string) tea.Cmd {
	m.input.SetValue(line)
	return press(m, "enter")
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
		case "left":
			msg.Code = tea.KeyLeft
		case "right":
			msg.Code = tea.KeyRight
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
	m.splashElapsed = splashDuration - fadeOut
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{bigTitle[0], tagline, skipHint} {
		if !strings.Contains(view, want) {
			t.Fatalf("splash should show %q:\n%s", want, view)
		}
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
		frame := drawLogo(splashGrown(at), at >= biteAt)
		if len(frame) != len(logoLines) {
			t.Fatalf("frame at %v has %d lines, want %d", at, len(frame), len(logoLines))
		}
		for _, l := range frame {
			if ansi.StringWidth(l) != width {
				t.Fatalf("frame at %v has a line of width %d, want %d", at, ansi.StringWidth(l), width)
			}
		}
	}
	if slices.Equal(drawLogo(splashGrown(0), false), drawLogo(splashGrown(splashDuration), true)) {
		t.Fatal("the splash should animate")
	}
}

func TestSplashSceneFillsTheScreen(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {40, 24}, {12, 4}} {
		for _, light := range []bool{false, true} {
			w, h := size[0], size[1]
			seen := map[string]bool{}
			for at := time.Duration(0); at <= splashDuration; at += splashFrame {
				frame := drawSplash(w, h, at, at, light)
				seen[frame] = true
				lines := strings.Split(frame, "\n")
				if len(lines) != h {
					t.Fatalf("%dx%d at %v: %d lines", w, h, at, len(lines))
				}
				for _, l := range lines {
					if ansi.StringWidth(l) != w {
						t.Fatalf("%dx%d at %v: a line of width %d:\n%s", w, h, at, ansi.StringWidth(l), ansi.Strip(frame))
					}
				}
			}
			if len(seen) < 50 {
				t.Fatalf("%dx%d: only %d different frames, the splash should keep moving", w, h, len(seen))
			}
		}
	}
	// A narrow screen gets the title in plain letters.
	if end := ansi.Strip(drawSplash(40, 24, splashDuration-fadeOut, splashDuration-fadeOut, false)); !strings.Contains(end, titleText) || strings.Contains(end, bigTitle[0]) {
		t.Fatalf("narrow splash:\n%s", end)
	}
	// The colors follow the background.
	if drawSplash(100, 30, biteAt, biteAt, false) == drawSplash(100, 30, biteAt, biteAt, true) {
		t.Fatal("a light background should get other colors")
	}
}

func TestSplashTagIsTyped(t *testing.T) {
	half := ansi.Strip(drawSplash(100, 30, taglineAt+typeSpeed*6, taglineAt+typeSpeed*6, false))
	if !strings.Contains(half, "a tiny▌") || strings.Contains(half, tagline) {
		t.Fatalf("the tagline should be typed letter by letter:\n%s", half)
	}
	if early := ansi.Strip(drawSplash(100, 30, titleAt-scrambleFor, titleAt-scrambleFor, false)); strings.Contains(early, "▀▄▄▀") {
		t.Fatalf("the title should not be there before it decodes:\n%s", early)
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
	if !strings.Contains(view, strings.TrimSpace(logoLines[1])) || !strings.Contains(view, "Type /setup to set up") {
		t.Fatalf("an unconfigured node should show the logo and the setup hint:\n%s", view)
	}
	if run(m, "ping"); len(m.chat.entries) != 0 || m.notice != "set up the node first: /setup" || !m.noticeErr {
		t.Fatal("without a configured node there is nobody to chat with")
	}
	// Commands work all the same.
	if run(m, "/setup"); m.onboard == nil || !m.onboard.first {
		t.Fatal("/setup should open the setup")
	}
}

func TestChatIsFocusedOnStart(t *testing.T) {
	m, _ := newModel(t)
	if !m.input.Focused() {
		t.Fatal("the UI should open with the chat input focused")
	}
	if press(m, "h", "i"); m.input.Value() != "hi" {
		t.Fatalf("typed %q", m.input.Value())
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
	m, b := newModel(t)
	b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "pk1", Name: "gamma", Addr: "1.2.3.4:9"}}}
	m.Update(m.loadContacts())
	if view := m.View().Content; !strings.Contains(view, "✉ 1") {
		t.Fatalf("header should show the pending count:\n%s", view)
	}

	run(m, "/requests")
	if m.screen != screenRequests || m.input.Value() != "" {
		t.Fatal("/requests should open the requests screen")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "gamma") || !strings.Contains(view, "wants to chat") || !strings.Contains(view, "[accept]  reject") {
		t.Fatalf("requests view:\n%s", view)
	}

	cmd := press(m, "enter")
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
	m, b := newModel(t)
	b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "pk1", Name: "gamma"}}}
	m.Update(m.loadContacts())
	run(m, "/requests")
	press(m, "right")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, " accept  [reject]") {
		t.Fatalf("reject should be selected:\n%s", view)
	}
	cmd := press(m, "enter")
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
	run(m, "/new")
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

	run(m, "/new")
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
		"/quit":                func(m *Model) tea.Cmd { return run(m, "/quit") },
		"ctrl+c":               func(m *Model) tea.Cmd { _, cmd := m.Update(ctrlC); return cmd },
		"ctrl+c on requests":   func(m *Model) tea.Cmd { run(m, "/requests"); _, cmd := m.Update(ctrlC); return cmd },
		"ctrl+c on the help":   func(m *Model) tea.Cmd { run(m, "/help"); _, cmd := m.Update(ctrlC); return cmd },
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
	for _, key := range []string{"left", "right"} {
		m, b := newModel(t)
		b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "pk1", Name: "gamma"}}}
		m.Update(m.loadContacts())
		run(m, "/requests")
		press(m, key, "enter")()
		if b.decided == nil || b.decided.Err() != nil {
			t.Fatalf("%s: a decision should run with a live context", key)
		}
		m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
		if b.decided.Err() == nil {
			t.Fatalf("%s: quitting should cancel a running decision", key)
		}
	}
}

func TestStartAndStop(t *testing.T) {
	m, b := newModel(t)
	if run(m, "/start"); b.started != 0 || m.notice != "the node is running" {
		t.Fatalf("a running node is not started again: %q", m.notice)
	}
	m.Update(run(m, "/stop")())
	if b.stopped != 1 || m.notice != "node stopped" {
		t.Fatal("/stop should stop a running node")
	}
	m.status.Running = false
	if run(m, "/stop"); b.stopped != 1 || m.notice != "the node is not running" {
		t.Fatalf("a stopped node is not stopped again: %q", m.notice)
	}
	m.Update(run(m, "/start")())
	if b.started != 1 || m.notice != "node started" {
		t.Fatal("/start should start a stopped node")
	}
	m.status.Configured = false
	if run(m, "/start"); b.setups != 1 || m.onboard == nil || !m.onboard.first {
		t.Fatal("/start should open the setup for an unconfigured node")
	}
}

// The node keeps running while it is set up again: it is restarted with
// what the setup saved.
func TestSetupKeepsTheNodeRunning(t *testing.T) {
	m, b := newModel(t)
	if run(m, "/setup"); b.setups != 1 || m.onboard == nil || m.onboard.first || b.stopped != 0 {
		t.Fatalf("setups %d, stopped %d", b.setups, b.stopped)
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
		m.setup() // and so must the setup
		m.View()
		m.closeSetup()
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
		"left":  "accepting failed – the contact could not be saved",
		"right": "rejecting failed – the contact could not be saved",
	} {
		m, b := newModel(t)
		b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "pk1", Name: "gamma"}}}
		b.acceptErr = fmt.Errorf("%w: open contacts.json.lock: permission denied", plugin.ErrNotSaved)
		b.rejectErr = b.acceptErr
		m.Update(m.loadContacts())
		run(m, "/requests")

		_, refresh := m.Update(press(m, key, "enter")())
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
		m.Update(press(m, "enter")())
		if len(b.accepted)+len(b.rejected) != 2 || m.noticeErr || !strings.HasPrefix(m.notice, "request ") {
			t.Fatalf("%s: retry: accepted %v, rejected %v, notice %q", key, b.accepted, b.rejected, m.notice)
		}
	}
}

// What the agent proposes is shown as that, with the key that would be
// asked, and accepting it sends the request: it accepts nobody.
func TestProposedConnection(t *testing.T) {
	m, b := newModel(t)
	b.trust = Contacts{Incoming: []plugin.Request{
		{PublicKey: "0123456789abcdef", Name: "luna", Addr: "1.2.3.4:9", Proposed: true},
		{PublicKey: "pk2", Name: "gamma"},
	}}
	m.Update(m.loadContacts())
	run(m, "/requests")
	view := m.View().Content
	if !strings.Contains(view, "your agent wants to connect (0123456789…)") || strings.Count(view, "wants to chat") != 1 {
		t.Fatalf("requests view:\n%s", view)
	}
	m.Update(press(m, "enter")())
	if len(b.accepted) != 1 || b.accepted[0] != "0123456789abcdef" {
		t.Fatalf("accepted %v", b.accepted)
	}
	if view := m.View().Content; !strings.Contains(view, "request sent") || strings.Contains(view, "request accepted") {
		t.Fatalf("after agreeing to a proposal:\n%s", view)
	}
	// Declined, it is a request that was declined like any other.
	m.Update(press(m, "right", "enter")())
	if view := m.View().Content; !strings.Contains(view, "request declined") {
		t.Fatalf("after declining a proposal:\n%s", view)
	}
	// The request of a peer is accepted, not sent.
	press(m, "down")
	if m.choice != accept {
		t.Fatal("another entry is accepted unless reject is selected for it")
	}
	m.Update(press(m, "enter")())
	if view := m.View().Content; !strings.Contains(view, "request accepted") {
		t.Fatalf("after accepting a request:\n%s", view)
	}
}

// What the agent proposes for the configuration waits with the connection
// requests, and is decided like them: the owner sees what the field is and
// what it would be, as the node says, and what the agent says about it.
func TestProposals(t *testing.T) {
	m, b := newModel(t)
	at := time.Date(2026, 10, 4, 15, 4, 0, 0, time.Local)
	b.trust = Contacts{
		Incoming: []plugin.Request{{PublicKey: "pk1", Name: "gamma"}},
		Proposals: []plugin.Proposal{
			{ID: "p1", Field: "tools.a2a", Now: `[]`, Then: `["list_peers"]`, Reason: "peers should see who is online", At: at},
			{ID: "p2", Field: "name", Now: `"alpha"`, Then: `"omega"`, Reason: "a better name", At: at, Stale: true},
		},
	}
	m.Update(m.loadContacts())
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "✉ 3") {
		t.Fatalf("the header should count requests and proposals:\n%s", view)
	}
	run(m, "/requests")
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"› gamma", "Changes of the configuration your agent proposes",
		"  tools.a2a  · 15:04", "now:  []", `then: ["list_peers"]`, "your agent says: peers should see who is online",
		`now:  "alpha"`, `then: "omega"`, "the configuration was changed since: accepting drops this without applying it",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("the view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "✉") || strings.Count(view, "the configuration was changed since") != 1 || strings.Contains(view, "No pending requests") {
		t.Fatalf("the view:\n%s", view)
	}

	// Down to the first proposal, and accept it.
	press(m, "j")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "› tools.a2a") || strings.Contains(view, "› gamma") {
		t.Fatalf("the first proposal should be selected:\n%s", view)
	}
	m.Update(press(m, "enter")())
	if len(b.accepted) != 1 || b.accepted[0] != "proposal p1" || m.notice != "tools.a2a changed" || m.noticeErr {
		t.Fatalf("accepted %v, notice %q", b.accepted, m.notice)
	}
	// Further down to the second, which is the last, and refuse it.
	press(m, "j", "j", "j")
	if m.reqSel != 2 {
		t.Fatalf("selected %d", m.reqSel)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "› name") || strings.Contains(view, "› tools.a2a") {
		t.Fatalf("the second proposal should be selected:\n%s", view)
	}
	m.Update(press(m, "right", "enter")())
	if len(b.rejected) != 1 || b.rejected[0] != "proposal p2" || m.notice != "proposal refused, name stays as it is" || m.noticeErr {
		t.Fatalf("refused %v, notice %q", b.rejected, m.notice)
	}
	// The request above them is still a request.
	press(m, "k", "k", "k")
	m.Update(press(m, "enter")())
	if len(b.accepted) != 2 || b.accepted[1] != "pk1" {
		t.Fatalf("accepted %v", b.accepted)
	}

	// A decision that fails says why, and changes nothing here.
	press(m, "j")
	b.acceptErr = plugin.ErrStale
	m.Update(press(m, "enter")())
	if m.notice != plugin.ErrStale.Error() || !m.noticeErr {
		t.Fatalf("a stale proposal: %q", m.notice)
	}
	b.rejectErr = errors.New("disk full")
	m.Update(press(m, "right", "enter")())
	if m.notice != "disk full" || !m.noticeErr {
		t.Fatalf("a refusal that fails: %q", m.notice)
	}

	// Proposals alone: nothing but them to select, and a new one is counted.
	b.trust = Contacts{Proposals: b.trust.Proposals[:1]}
	m.Update(m.loadContacts())
	if m.reqSel != 0 {
		t.Fatalf("selected %d of one entry", m.reqSel)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "No pending requests.") || !strings.Contains(view, "› tools.a2a") {
		t.Fatalf("proposals alone:\n%s", view)
	}
	press(m, "esc")
	b.trust = Contacts{Proposals: []plugin.Proposal{{ID: "p1"}, {ID: "p3"}, {ID: "p4"}}}
	m.Update(m.loadContacts())
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "✉ 2") {
		t.Fatalf("new proposals should be counted:\n%s", view)
	}
	// With nothing waiting there is nothing to decide.
	b.trust = Contacts{}
	m.Update(m.loadContacts())
	run(m, "/requests")
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "Changes of the configuration") || strings.Contains(view, "[accept]") {
		t.Fatalf("nothing is proposed:\n%s", view)
	}
	if cmd := press(m, "enter"); cmd != nil {
		t.Fatal("accepting nothing should do nothing")
	}
}
