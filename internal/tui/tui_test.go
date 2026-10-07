package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/session"
	"ouroboros/plugin"
)

type sent struct {
	contextID string
	text      string
}

// Step is a call of a tool as the fake agent reports it: Text is what the UI
// shows of it.
type Step struct {
	Text string
	Done bool
}

// fakeAgent is the agent of the sessions of a fakeBackend: it answers as it
// is told.
type fakeAgent struct {
	steps       []Step // reported after says, before the reply
	says        []string
	reply       string
	err         error               // what the answer fails with
	contextSize int                 // how long the context of every session is
	cache       plugin.SessionCache // what the provider cached of every session

	// ask, if set, answers instead of steps, says and reply.
	ask func(ctx context.Context, step func(Step), say func(string)) (string, error)
}

func (a *fakeAgent) Ready() error { return nil }
func (a *fakeAgent) Run(ctx context.Context, _, _ string, observe func(assistant.Step), say func(string)) (string, error) {
	step := func(s Step) { observe(assistant.Step{Tool: s.Text, Done: s.Done}) }
	if a.ask != nil {
		return a.ask(ctx, step, say)
	}
	for _, s := range a.says {
		say(s)
	}
	for _, s := range a.steps {
		step(s)
	}
	return a.reply, a.err
}
func (a *fakeAgent) Context(string) int                          { return a.contextSize }
func (a *fakeAgent) Cache(string) assistant.Cache                { return a.cache }
func (a *fakeAgent) Contents(string) (assistant.Contents, error) { return assistant.Contents{}, nil }
func (a *fakeAgent) Inspection(string) (agent.Inspection, bool)  { return agent.Inspection{}, false }
func (a *fakeAgent) Conversation(string) json.RawMessage         { return nil }
func (a *fakeAgent) Resume(string, json.RawMessage) error        { return nil }
func (a *fakeAgent) Forget(string)                               {}

// fakeBackend is what the UI controls in the tests. Its sessions are those
// of the node, in this process: their answers come from the fakeAgent.
type fakeBackend struct {
	*fakeAgent
	sessions *session.Service

	status   Status
	started  int
	stopped  int
	setups   int        // how often the setup was opened
	setup    *fakeSetup // what it opens; one that knows the fake endpoint if nil
	contents []Message  // what the context of every conversation is

	trust     Contacts
	accepted  []string
	rejected  []string
	acceptErr error
	rejectErr error
	decided   context.Context // what a decision on a request ran with

	// offers are what the provider answers with, for /model; modelsErr is
	// why not. set are the models the owner saved.
	offers    Models
	modelsErr error
	set       []string

	// fail is what fails, by the name of the request.
	fail map[string]error
	// debug is what SetDebug was called with, in order.
	debug []bool

	// settings are what /settings shows; changes what the owner changed
	// there, and done what a change answers with.
	settings Settings
	changes  []Setting
	done     string

	mu   sync.Mutex
	sent []sent // the messages asked for

	peers []plugin.SessionInfo // the conversations of peers
	read  map[string][]Event   // what happened in them
}

func (f *fakeBackend) Settings() (Settings, error) { return f.settings, f.fail["settings"] }
func (f *fakeBackend) ChangeSetting(s Setting) (string, error) {
	f.changes = append(f.changes, s)
	return f.done, f.fail["change"]
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
func (f *fakeBackend) Ask(ctx context.Context, id, text string) error {
	f.mu.Lock()
	f.sent = append(f.sent, sent{id, text})
	f.mu.Unlock()
	if err := f.fail["ask"]; err != nil {
		return err
	}
	return f.sessions.Ask(ctx, id, text)
}

// messages are the messages asked for so far.
func (f *fakeBackend) messages() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// errNoNode is what a fakeBackend without sessions says to what needs them.
var errNoNode = errors.New("the node is not running")

func (f *fakeBackend) Watch(ctx context.Context, id string) ([]Event, <-chan Event, error) {
	if err := f.fail["watch"]; err != nil {
		return nil, nil, err
	}
	if f.sessions == nil {
		return nil, nil, errNoNode
	}
	past, live, err := f.sessions.Watch(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	shown := make([]Event, len(past))
	for i, e := range past {
		shown[i] = shownEvent(e)
	}
	out := make(chan Event)
	go func() {
		defer close(out)
		for e := range live {
			out <- shownEvent(e)
		}
	}()
	return shown, out, nil
}

// shownEvent is e as the UI shows it: what the fake agent calls a tool is
// what is shown of it.
func shownEvent(e plugin.SessionEvent) Event {
	text := e.Text
	if e.Kind == plugin.SessionStep {
		text = e.Tool
	}
	return Event{Kind: e.Kind, Text: text, Done: e.Done, At: e.At, Context: e.Context, Cache: e.Cache}
}

func (f *fakeBackend) Cancel(ctx context.Context, id string) error {
	if err := f.fail["cancel"]; err != nil {
		return err
	}
	return f.sessions.Cancel(ctx, id)
}
func (f *fakeBackend) Sessions(ctx context.Context) ([]plugin.SessionInfo, error) {
	if err := f.fail["sessions"]; err != nil {
		return nil, err
	}
	if f.sessions == nil {
		return nil, errNoNode
	}
	return f.sessions.List(ctx)
}
func (f *fakeBackend) Delete(ctx context.Context, id string) error {
	if err := f.fail["delete"]; err != nil {
		return err
	}
	if i := slices.IndexFunc(f.peers, func(p plugin.SessionInfo) bool { return p.ID == id }); i >= 0 {
		f.peers = slices.Delete(f.peers, i, i+1)
		return nil
	}
	return f.sessions.Delete(ctx, id)
}
func (f *fakeBackend) Peers(ctx context.Context) ([]plugin.SessionInfo, error) {
	if err := f.fail["peers"]; err != nil {
		return nil, err
	}
	return f.peers, nil
}
func (f *fakeBackend) Read(ctx context.Context, id string) ([]Event, error) {
	if err := f.fail["read"]; err != nil {
		return nil, err
	}
	return f.read[id], nil
}
func (f *fakeBackend) Contents(context.Context, string) ([]Message, error) {
	return f.contents, f.fail["contents"]
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
func (f *fakeBackend) Models(ctx context.Context) (Models, error) { return f.offers, f.modelsErr }
func (f *fakeBackend) SetModel(model string) error {
	f.set = append(f.set, model)
	return f.fail["setmodel"]
}

func (f *fakeBackend) SetDebug(on bool) error {
	f.debug = append(f.debug, on)
	return f.fail["setdebug"]
}

// newBackend is a fakeBackend whose agent answers "pong", until the test ends.
func newBackend(t *testing.T) *fakeBackend {
	t.Helper()
	a := &fakeAgent{reply: "pong"}
	b := &fakeBackend{fakeAgent: a, sessions: session.New(a), status: Status{Configured: true, Running: true, ID: "aaa111", Name: "alpha"}}
	t.Cleanup(b.sessions.Close)
	return b
}

func newModel(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	b := newBackend(t)
	return newModelOn(t, b), b
}

// newModelOn is the UI of b in a window of 100×30, without the splash.
func newModelOn(t *testing.T, b *fakeBackend) *Model {
	t.Helper()
	m := New(b, nil, nil)
	t.Cleanup(m.quit)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.showSplash = false // dismiss the start screen; its own test is below
	m.Update(m.refreshStatus())
	return m
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

// deliver runs cmd and the commands that follow it until none is left.
func deliver(m *Model, cmd tea.Cmd) {
	for cmd != nil {
		_, cmd = m.Update(cmd())
	}
}

// drivers run the commands of the UIs of the tests, by UI.
var drivers sync.Map

// drive runs cmd and what follows of it as the program does, until done
// reports true. A command that does not come back at once, like the wait for
// what happens next in a session, goes on waiting: what it brings is handed
// to the UI in a later drive. Ticks are left out.
func drive(t *testing.T, m *Model, cmd tea.Cmd, done func() bool) {
	t.Helper()
	v, _ := drivers.LoadOrStore(m, make(chan tea.Msg, 1024))
	msgs := v.(chan tea.Msg)
	start := func(c tea.Cmd) {
		if c != nil {
			go func() { msgs <- c() }()
		}
	}
	start(cmd)
	deadline := time.After(5 * time.Second)
	for !done() {
		select {
		case msg := <-msgs:
			switch msg := msg.(type) {
			case tea.BatchMsg:
				for _, c := range msg {
					start(c)
				}
			case nil, tickMsg, rewatchMsg, splashMsg:
			default:
				_, c := m.Update(msg)
				start(c)
			}
		case <-deadline:
			t.Fatalf("the UI did not get there: %+v", m.chat)
		}
	}
}

// answered drives the UI until the answer to what cmd sent is in.
func answered(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	c := m.chat
	n := len(c.entries)
	drive(t, m, cmd, func() bool { return !c.pending && len(c.entries) > n+1 })
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

// The model is shown with the provider that answers with it before it.
func TestViewShowsTheProviderOfTheModel(t *testing.T) {
	m, b := newModel(t)
	b.status.Provider = "iGate"
	b.status.Model = "brain"
	m.Update(m.refreshStatus())
	view := m.View().Content
	if !strings.Contains(view, "iGate / brain") {
		t.Errorf("view lacks \"iGate / brain\":\n%s", view)
	}
	b.status.Provider = ""
	m.Update(m.refreshStatus())
	if strings.Contains(m.View().Content, "iGate") {
		t.Errorf("a node without a provider shows none:\n%s", m.View().Content)
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
	if press(m, "enter") != nil || m.notice != "wait for the reply first" {
		t.Fatal("a second message must wait for the reply")
	}

	answered(t, m, cmd)
	c := m.chat
	if got := b.messages(); len(got) != 1 || got[0] != (sent{c.contextID, "ping"}) {
		t.Fatalf("sent %+v", got)
	}
	if c.pending || len(c.entries) != 2 || c.entries[0].text != "ping" || !c.entries[0].mine || c.entries[1].text != "pong" || c.entries[1].mine {
		t.Fatalf("entries %+v", c.entries)
	}
	if view := m.View().Content; !strings.Contains(view, "ping") || !strings.Contains(view, "pong") {
		t.Fatal("history should show both messages")
	}

	// The conversation keeps its context.
	answered(t, m, say(t, m, "more"))
	if b.messages()[1].contextID != c.contextID {
		t.Fatal("context changed within a conversation")
	}
}

func TestChatShowsTheLoopToTheAnswer(t *testing.T) {
	m, b := newModel(t)
	gate := make(chan struct{})
	b.ask = func(_ context.Context, step func(Step), say func(string)) (string, error) {
		say("I will ask beta")
		step(Step{Text: "asking beta"})
		<-gate
		step(Step{Text: "asking beta", Done: true})
		return "beta says it is noon", nil
	}
	// The words of the agent are in the chat, and the call to beta is
	// announced and still running.
	drive(t, m, say(t, m, "ask beta for the time"), func() bool { return m.chat.doing == "asking beta" })
	if !m.chat.pending || !strings.Contains(m.View().Content, "I will ask beta") || !strings.Contains(m.View().Content, "asking beta") {
		t.Fatalf("what the agent says on the way belongs in the chat:\n%s", m.View().Content)
	}
	close(gate) // beta answered, the agent thinks again and answers
	drive(t, m, nil, func() bool { return !m.chat.pending })

	c := m.chat
	// The call these words belong to goes below them.
	want := []entry{{mine: true, text: "ask beta for the time"},
		{text: "I will ask beta", calls: []call{{"asking beta", true}}}, {text: "beta says it is noon"}}
	if !sameEntries(c.entries, want) {
		t.Fatalf("the loop belongs in the history: %+v", c.entries)
	}
	view := trimmedLines(m)
	// Once the answer is in, its calls are summed up below the words.
	for _, want := range []string{"I will ask beta\n│   ↳ 1 tool call: asking beta", "beta says it is noon"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the history should show %q:\n%s", want, view)
		}
	}
}

// An answer that fails says why; a message the node does not take goes back
// into the input, with why in the footer.
func TestSendError(t *testing.T) {
	m, b := newModel(t)
	b.err = errors.New("connection refused")
	answered(t, m, say(t, m, "ping"))
	if e := m.chat.entries[1]; !e.failed || e.text != "connection refused" {
		t.Fatalf("entry %+v", e)
	}
	b.fail = map[string]error{"ask": errors.New("the node is not running, type /start")}
	drive(t, m, say(t, m, "again"), func() bool { return m.noticeErr })
	if m.chat.pending || len(m.chat.entries) != 2 || m.input.Value() != "again" || m.notice != "the node is not running, type /start" {
		t.Fatalf("pending %v, entries %+v, input %q, notice %q", m.chat.pending, m.chat.entries, m.input.Value(), m.notice)
	}
}

func TestConnectionRequests(t *testing.T) {
	m, b := newModel(t)
	b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "pk1", Name: "gamma", Addr: "1.2.3.4:9"}}}
	m.Update(m.loadContacts())
	if view := m.View().Content; !strings.Contains(view, "✉ 1") {
		t.Fatalf("header should show the pending count:\n%s", view)
	}

	run(m, "/pending")
	if m.screen != screenRequests || m.input.Value() != "" {
		t.Fatal("/pending should open the requests screen")
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
	run(m, "/pending")
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

// Every round of the loop has its entry, a round the agent said nothing of
// too, and every call of a round its line, which says once the call ended
// what became of it. The calls a script runs end before the script does.
// Another UI that shows the session sees the same.
func TestChatShowsEveryCallOfTheLoop(t *testing.T) {
	m, b := newModel(t)
	written, scripted := make(chan struct{}), make(chan struct{})
	b.ask = func(_ context.Context, step func(Step), say func(string)) (string, error) {
		say("I read both")
		step(Step{Text: "reading a.go"})
		step(Step{Text: "reading a.go ✓ 3ms", Done: true})
		step(Step{Text: "reading b.go"})
		step(Step{Text: "reading b.go ✗ no such file", Done: true})
		say("")
		step(Step{Text: "running a script"})
		step(Step{Text: "writing c.go"})
		<-written
		step(Step{Text: "writing c.go ✓ 1ms", Done: true})
		<-scripted
		step(Step{Text: "running a script ✓ 9ms", Done: true})
		// A call that ended without having started changes nothing.
		step(Step{Text: "listing ~ ✓ 1ms", Done: true})
		return "", nil
	}
	drive(t, m, say(t, m, "fix it"), func() bool { return m.chat.doing == "writing c.go" })
	if !strings.Contains(ansi.Strip(m.View().Content), "· writing c.go …") {
		t.Fatalf("the call the agent waits for should be marked: %+v\n%s", m.chat, ansi.Strip(m.View().Content))
	}
	close(written)
	drive(t, m, nil, func() bool { return m.chat.doing == "running a script" }) // the script still runs
	close(scripted)
	drive(t, m, nil, func() bool { return !m.chat.pending })

	want := []entry{{mine: true, text: "fix it"},
		{text: "I read both", calls: []call{{"reading a.go ✓ 3ms", true}, {"reading b.go ✗ no such file", true}}},
		{calls: []call{{"running a script ✓ 9ms", true}, {"writing c.go ✓ 1ms", true}}},
		{text: ""}}
	if !sameEntries(m.chat.entries, want) {
		t.Fatalf("the loop in the history: %+v", m.chat.entries)
	}
	view := ansi.Strip(m.View().Content)
	// Once the answer is in, the calls of all its rounds are one line.
	if !strings.Contains(trimmedLines(m), "I read both\n│   ↳ 4 tool calls, 1 failed: reading a.go, reading b.go, running a script, writing c.go\n") ||
		strings.Contains(view, "↳ ·") {
		t.Fatalf("the calls should be summed up:\n%s", view)
	}
	if strings.Contains(view, " …") {
		t.Fatalf("no call is waited for any more:\n%s", view)
	}

	// Another UI that shows the session gets what happened in it.
	other := newModelOn(t, b)
	other.Resume(m.Current())
	drive(t, other, other.watch(other.chat), func() bool { return len(other.chat.entries) == len(want) })
	if !sameEntries(other.chat.entries, want) {
		t.Fatalf("the other UI shows %+v", other.chat.entries)
	}
}

// trimmedLines is the view of m without colors, every line without the
// spaces and the border at its end.
func trimmedLines(m *Model) string {
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(strings.TrimRight(strings.TrimRight(l, " "), "│"), " ")
	}
	return strings.Join(lines, "\n")
}

// sameEntries reports whether got are the entries of want, the time aside.
func sameEntries(got, want []entry) bool {
	return slices.EqualFunc(got, want, func(g, w entry) bool {
		return g.mine == w.mine && g.text == w.text && g.failed == w.failed && slices.Equal(g.calls, w.calls)
	})
}

// A new conversation leaves the one before going on: its answer comes in
// there, and the footer says so.
func TestNewConversationLeavesTheAnswerGoingOn(t *testing.T) {
	m, b := newModel(t)
	gate := make(chan struct{})
	b.ask = func(_ context.Context, step func(Step), _ func(string)) (string, error) {
		step(Step{Text: "asking beta"})
		<-gate
		return "beta is fine", nil
	}
	drive(t, m, say(t, m, "how is beta"), func() bool { return m.chat.doing == "asking beta" })
	old := m.chat
	drive(t, m, run(m, "/new"), func() bool { return true })
	c := m.chat
	if c == old || c.contextID == old.contextID || len(c.entries) != 0 || c.pending || c.doing != "" {
		t.Fatalf("conversation not new: %+v", c)
	}
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "asking beta") {
		t.Fatalf("the new conversation shows the old one:\n%s", view)
	}
	close(gate)
	drive(t, m, nil, func() bool { return !old.pending })
	if m.chat != c || len(c.entries) != 0 || old.entries[len(old.entries)-1].text != "beta is fine" {
		t.Fatalf("the answer belongs to the old conversation: %+v, %+v", old.entries, c.entries)
	}
	if m.notice != "answered in “how is beta” · /session" || m.noticeErr {
		t.Fatalf("notice %q", m.notice)
	}
}

// askUntilCancelled makes b answer the way a real agent does when it is
// stopped: it asks beta, works until its context ends and fails with that.
// The returned channel is closed once the answer has given up.
func askUntilCancelled(b *fakeBackend) <-chan struct{} {
	stopped := make(chan struct{})
	b.ask = func(ctx context.Context, step func(Step), _ func(string)) (string, error) {
		step(Step{Text: "asking beta"})
		<-ctx.Done()
		step(Step{Text: "asking beta", Done: true})
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

// /cancel stops the answer on its way in the conversation shown, which says
// so; the conversation goes on as usual.
func TestCancelStopsTheAnswer(t *testing.T) {
	m, b := newModel(t)
	if run(m, "/cancel"); m.notice != "no answer is on its way here" || m.noticeErr {
		t.Fatalf("nothing to stop: %q", m.notice)
	}
	stopped := askUntilCancelled(b)
	drive(t, m, say(t, m, "ping"), func() bool { return m.chat.doing == "asking beta" })
	cmd := run(m, "/cancel")
	if m.notice != "stopping the answer…" {
		t.Fatalf("notice %q", m.notice)
	}
	drive(t, m, cmd, func() bool { return !m.chat.pending && m.notice == "answer stopped" })
	awaitStop(t, stopped)
	c := m.chat
	if e := c.entries[len(c.entries)-1]; !e.failed || e.text != "stopped before it answered" {
		t.Fatalf("entries %+v", c.entries)
	}
	if view := m.View().Content; strings.Contains(view, context.Canceled.Error()) {
		t.Fatalf("a cancelled answer is no error to show:\n%s", view)
	}

	// The conversation answers as usual.
	b.ask = nil
	answered(t, m, say(t, m, "more"))
	if e := c.entries[len(c.entries)-1]; e.text != "pong" || e.failed {
		t.Fatalf("entries %+v", c.entries)
	}

	// What cannot be stopped says why.
	b.fail = map[string]error{"cancel": errors.New("boom")}
	c.pending = true
	m.Update(run(m, "/cancel")())
	if m.notice != "boom" || !m.noticeErr {
		t.Fatalf("notice %q", m.notice)
	}
}

// Quitting the UI leaves the answer on its way going on: it is the node's.
func TestQuitLeavesTheAnswerGoingOn(t *testing.T) {
	ctrlC := tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})
	for name, quit := range map[string]func(*Model) tea.Cmd{
		"/quit":                func(m *Model) tea.Cmd { return run(m, "/quit") },
		"ctrl+c":               func(m *Model) tea.Cmd { _, cmd := m.Update(ctrlC); return cmd },
		"ctrl+c on requests":   func(m *Model) tea.Cmd { run(m, "/pending"); _, cmd := m.Update(ctrlC); return cmd },
		"ctrl+c on the help":   func(m *Model) tea.Cmd { run(m, "/help"); _, cmd := m.Update(ctrlC); return cmd },
		"ctrl+c on the splash": func(m *Model) tea.Cmd { m.showSplash = true; _, cmd := m.Update(ctrlC); return cmd },
	} {
		t.Run(name, func(t *testing.T) {
			m, b := newModel(t)
			gate := make(chan struct{})
			b.ask = func(_ context.Context, step func(Step), _ func(string)) (string, error) {
				step(Step{Text: "asking beta"})
				<-gate
				return "beta is fine", nil
			}
			drive(t, m, say(t, m, "ping"), func() bool { return m.chat.doing == "asking beta" })
			if _, ok := quit(m)().(tea.QuitMsg); !ok {
				t.Fatal("the UI should quit")
			}
			running := func() bool {
				list, _ := b.sessions.List(context.Background())
				return len(list) == 1 && list[0].Running
			}
			if !running() {
				t.Fatal("the answer should go on")
			}
			close(gate)
			for deadline := time.Now().Add(5 * time.Second); running(); time.Sleep(time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatal("the answer should end")
				}
			}
		})
	}
}

func TestQuitCancelsRequestDecisions(t *testing.T) {
	for _, key := range []string{"left", "right"} {
		m, b := newModel(t)
		b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "pk1", Name: "gamma"}}}
		m.Update(m.loadContacts())
		run(m, "/pending")
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
		run(m, "/pending")

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
	run(m, "/pending")
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
	run(m, "/pending")
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
	run(m, "/pending")
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "Changes of the configuration") || strings.Contains(view, "[accept]") {
		t.Fatalf("nothing is proposed:\n%s", view)
	}
	if cmd := press(m, "enter"); cmd != nil {
		t.Fatal("accepting nothing should do nothing")
	}
}

// A tool that asked for ouro to start anew makes the UI quit once the answer
// is in; the session it showed goes on in another UI, without the splash,
// which does not quit for what happened before it.
func TestRestartGoesOnInAnotherUI(t *testing.T) {
	m, b := newModel(t)
	answered(t, m, say(t, m, "ping"))
	if _, ok := m.Restarts(); ok {
		t.Fatal("no tool asked for a restart")
	}
	b.sessions.Attach(nil, func(string) {}) // a node that can take its own place
	b.ask = func(ctx context.Context, _ func(Step), _ func(string)) (string, error) {
		plugin.Restart(ctx, "/new/ouro")
		return "built and installed", nil
	}
	drive(t, m, say(t, m, "rebuild"), func() bool { _, ok := m.Restarts(); return ok })
	if exe, _ := m.Restarts(); exe != "/new/ouro" {
		t.Fatalf("the UI should quit to start anew from /new/ouro, not %q", exe)
	}

	other := newModelOn(t, b)
	other.showSplash = true
	other.Resume(m.Current())
	drive(t, other, other.watch(other.chat), func() bool { return len(other.chat.entries) == len(m.chat.entries) })
	if other.showSplash || !sameEntries(other.chat.entries, m.chat.entries) || other.Current() != m.Current() {
		t.Fatalf("resumed %+v, want %+v", other.chat, m.chat)
	}
	if view := ansi.Strip(other.View().Content); !strings.Contains(view, "built and installed") || !strings.Contains(view, "restarted with the new build") {
		t.Fatalf("the resumed chat should be shown:\n%s", view)
	}
	if _, ok := other.Restarts(); ok {
		t.Fatal("a restart that happened before does not make the UI quit")
	}
}

// What is pending is said in a line below the footer, with the command that
// decides on it, until it is decided: on every screen but the one that lists
// it, and within the height of the window.
func TestPendingLine(t *testing.T) {
	m, b := newModel(t)
	last := func() string {
		lines := strings.Split(ansi.Strip(m.View().Content), "\n")
		if len(lines) != 30 {
			t.Fatalf("%d lines in a window of 30", len(lines))
		}
		return strings.TrimRight(lines[len(lines)-1], " ")
	}
	if line := last(); strings.Contains(line, "pending") {
		t.Fatalf("nothing is pending: %q", line)
	}
	height := m.history.Height()

	// A peer that did not say its name is called by its key.
	b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "0123456789abcdef"}}}
	m.Update(m.loadContacts())
	if line := last(); !strings.Contains(line, "0123456789… wants to chat") {
		t.Fatalf("pending line %q", line)
	}
	b.trust = Contacts{
		Incoming:  []plugin.Request{{PublicKey: "pk1", Name: "gamma"}, {PublicKey: "pk2", Name: "bob", Proposed: true}},
		Proposals: []plugin.Proposal{{ID: "1", Field: "tools.a2a"}},
	}
	m.Update(m.loadContacts())
	// The window is too narrow for all of it: the end is cut off, not the command.
	want := " ● 3 pending · type /pending to decide · gamma wants to chat, your agent wants to connect to bob, "
	if line := last(); !strings.HasPrefix(line, want) || !strings.HasSuffix(line, "…") {
		t.Fatalf("pending line %q", line)
	}
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
	if line := last(); line != want+"your agent proposes to change tools.a2a" {
		t.Fatalf("pending line %q", line)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.history.Height() != height-1 {
		t.Fatalf("the history is %d high, was %d", m.history.Height(), height)
	}

	// Seen is not decided: the line is back after the screen.
	run(m, "/pending")
	if line := last(); strings.Contains(line, "pending") {
		t.Fatalf("the screen lists what is pending itself: %q", line)
	}
	press(m, "esc")
	if line := last(); !strings.Contains(line, "3 pending") {
		t.Fatalf("pending line %q", line)
	}

	b.trust = Contacts{}
	m.Update(m.loadContacts())
	if line := last(); strings.Contains(line, "pending") || m.history.Height() != height {
		t.Fatalf("nothing is pending: %q, history %d high", line, m.history.Height())
	}
}
