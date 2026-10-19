package tui

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/openai"
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
func (a *fakeAgent) Context(string) int                                      { return a.contextSize }
func (a *fakeAgent) Cache(string) assistant.Cache                            { return a.cache }
func (a *fakeAgent) Contents(string) (assistant.Contents, error)             { return assistant.Contents{}, nil }
func (a *fakeAgent) Inspection(string) (agent.Inspection, bool)              { return agent.Inspection{}, false }
func (a *fakeAgent) Conversation(string) json.RawMessage                     { return nil }
func (a *fakeAgent) Resume(string, json.RawMessage) error                    { return nil }
func (a *fakeAgent) Forget(string)                                           {}
func (a *fakeAgent) Title(context.Context, []openai.Message) (string, error) { return "", nil }
func (a *fakeAgent) Effort() string                                          { return "" }
func (a *fakeAgent) Model() string                                           { return "" }
func (a *fakeAgent) Note(context.Context, string, string) error              { return nil }

// fakeBackend is what the UI controls in the tests. Its sessions are those
// of the node, in this process: their answers come from the fakeAgent.
type fakeBackend struct {
	*fakeAgent
	sessions *session.Service

	status  Status
	started int
	// efforts are what the model takes, once effortsKnown says it is listed.
	efforts      []string
	effortsKnown bool
	stopped      int
	setups       int        // how often the setup was opened
	setup        *fakeSetup // what it opens; one that knows the fake endpoint if nil
	contents     []Message  // what the context of every conversation is

	trust     Contacts
	accepted  []string
	rejected  []string
	acceptErr error
	rejectErr error

	// offers are what the accesses answer with, for /model; modelsErr is
	// why not. set are the models the owner saved, each as "access model",
	// and switched says that saving one switches the access.
	offers    []Models
	modelsErr error
	set       []string
	switched  bool

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
	// fallbacks are what the node falls back on.
	fallbacks []Fallback
	// pictures are the pictures that were pasted, to keep.
	pictures []string
	// canceled are the sessions whose answer was stopped, in order.
	canceled []string
	// unsteers is how often a message that waits was asked back.
	unsteers int

	peers []plugin.SessionInfo // the conversations of peers
	hits  []string             // the sessions whose content a filter was found in
	read  map[string][]Event   // what happened in them
}

func (f *fakeBackend) Gateways(ctx context.Context) ([]plugin.GatewaySettings, error) {
	return nil, nil
}
func (f *fakeBackend) SaveGateway(context.Context, string, plugin.GatewayChange) error { return nil }

func (f *fakeBackend) Settings() (Settings, error) { return f.settings, f.fail["settings"] }
func (f *fakeBackend) ChangeSetting(s Setting) (string, error) {
	f.changes = append(f.changes, s)
	return f.done, f.fail["change"]
}

func (f *fakeBackend) Status() (Status, error) { return f.status, f.fail["status"] }
func (f *fakeBackend) Start() error            { f.started++; return f.fail["start"] }
func (f *fakeBackend) Stop() error             { f.stopped++; return f.fail["stop"] }
func (f *fakeBackend) Contacts() (Contacts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	trust := f.trust
	trust.Suggestions = slices.Clone(trust.Suggestions)
	return trust, f.fail["contacts"]
}
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
	shown := Event{Kind: e.Kind, Text: text, Done: e.Done, At: e.At, Context: e.Context, Cache: e.Cache, N: e.N, Routed: e.Routed}
	if e.Kind == plugin.SessionAsked && !e.Done {
		q := e.AsQuestion()
		shown.Text, shown.Options, shown.Recommended = q.Question, q.Options, q.Recommended
	}
	return shown
}

func (f *fakeBackend) Steer(ctx context.Context, id, text string) error {
	if err := f.fail["steer"]; err != nil {
		return err
	}
	return f.sessions.Steer(ctx, id, text)
}
func (f *fakeBackend) Unsteer(ctx context.Context, id string) (string, error) {
	f.mu.Lock()
	f.unsteers++
	f.mu.Unlock()
	if err := f.fail["unsteer"]; err != nil {
		return "", err
	}
	return f.sessions.Unsteer(ctx, id)
}
func (f *fakeBackend) SavePicture(data []byte, ext string) (string, error) {
	f.pictures = append(f.pictures, string(data))
	return "/work/pasted/" + strconv.Itoa(len(f.pictures)) + "." + ext, f.fail["picture"]
}
func (f *fakeBackend) Cancel(ctx context.Context, id string) error {
	f.mu.Lock()
	f.canceled = append(f.canceled, id)
	f.mu.Unlock()
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

// Find returns the sessions whose content holds query.
func (f *fakeBackend) Find(ctx context.Context, query string) ([]string, error) {
	return f.hits, nil
}

func (f *fakeBackend) Peers(ctx context.Context) ([]plugin.SessionInfo, error) {
	if err := f.fail["peers"]; err != nil {
		return nil, err
	}
	return f.peers, nil
}
func (f *fakeBackend) Jobs(ctx context.Context) ([]plugin.Job, error) {
	if err := f.fail["jobs"]; err != nil {
		return nil, err
	}
	return f.sessions.Jobs(ctx)
}
func (f *fakeBackend) Services(ctx context.Context) ([]plugin.ServiceInfo, error) {
	return f.sessions.Services(ctx)
}
func (f *fakeBackend) StopService(ctx context.Context, id string) error {
	return f.sessions.StopService(ctx, id)
}
func (f *fakeBackend) StopJob(ctx context.Context, id string, n int) error {
	return f.sessions.StopJob(ctx, id, n)
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
func (f *fakeBackend) TakeSuggestion(id string) (plugin.Suggestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accepted = append(f.accepted, "suggestion "+id)
	for i, s := range f.trust.Suggestions {
		if s.ID == id {
			if f.acceptErr == nil {
				f.trust.Suggestions = slices.Delete(slices.Clone(f.trust.Suggestions), i, i+1)
			}
			return s, f.acceptErr
		}
	}
	return plugin.Suggestion{}, plugin.ErrNoSuggestion
}
func (f *fakeBackend) DropSuggestion(id string) error {
	f.rejected = append(f.rejected, "suggestion "+id)
	return f.rejectErr
}
func (f *fakeBackend) Models(ctx context.Context) ([]Models, error) { return f.offers, f.modelsErr }
func (f *fakeBackend) Fallbacks() ([]Fallback, error)               { return f.fallbacks, f.fail["fallbacks"] }
func (f *fakeBackend) ToggleFallback(access, model string) ([]Fallback, error) {
	if err := f.fail["togglefallback"]; err != nil {
		return nil, err
	}
	this := Fallback{Access: access, Model: model}
	if at := slices.Index(f.fallbacks, this); at >= 0 {
		f.fallbacks = slices.Delete(f.fallbacks, at, at+1)
	} else {
		f.fallbacks = append(f.fallbacks, this)
	}
	return slices.Clone(f.fallbacks), nil
}
func (f *fakeBackend) SetPeerModel(access, model string) error {
	if err := f.fail["setpeermodel"]; err != nil {
		return err
	}
	f.status.PeerAccess, f.status.PeerModel = access, model
	return nil
}
func (f *fakeBackend) SetModel(access, model string) (bool, error) {
	f.set = append(f.set, access+" "+model)
	return f.switched, f.fail["setmodel"]
}

func (f *fakeBackend) SetDebug(on bool) error {
	f.debug = append(f.debug, on)
	return f.fail["setdebug"]
}

// SetEffort saves effort in the status, as the node does.
func (f *fakeBackend) Efforts() ([]string, bool) { return f.efforts, f.effortsKnown }

func (f *fakeBackend) SetSessionEffort(ctx context.Context, id, effort string) error {
	if err := f.fail["session effort"]; err != nil {
		return err
	}
	return f.sessions.SetEffort(ctx, id, effort)
}

func (f *fakeBackend) SetEffort(effort string) error {
	if err := f.fail["effort"]; err != nil {
		return err
	}
	f.status.Effort = effort
	return nil
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

// newModelOn is the UI of b in a window of 100×30, without the splash, in a
// conversation of its own, as /new starts it: the thread, which a UI shows
// first, hides what an answer does on its way, see thread_test.go.
func newModelOn(t *testing.T, b *fakeBackend) *Model {
	t.Helper()
	m := New(b, nil, nil)
	delete(m.chats, plugin.ThreadID)
	m.chat = m.newChat()
	t.Cleanup(m.quit)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.splash.show = false // dismiss the start screen; its own test is below
	m.Update(m.refreshStatus())
	m.inputShown = true // the tests here take the box of the input as shown
	m.layout()
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
		case "backspace":
			msg.Code = tea.KeyBackspace
		case "ctrl+d":
			msg.Code, msg.Mod = 'd', tea.ModCtrl
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
	m.splash.show = true
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.splash.elapsed = splashDuration - fadeOut
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{bigTitle[0], tagline, skipHint} {
		if !strings.Contains(view, want) {
			t.Fatalf("splash should show %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, nameRow(m.agentName())) {
		t.Fatal("splash should hide the normal view")
	}

	if cmd := press(m, "x"); cmd != nil {
		t.Fatal("closing the splash should not run a command")
	}
	if m.splash.show {
		t.Fatal("a key should close the splash")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, nameRow(m.agentName())) {
		t.Fatal("after the splash the normal view should be shown")
	}
}

func TestSplashClosesAfterTimeout(t *testing.T) {
	m, _ := newModel(t)
	m.splash.show = true
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.splash.start = time.Unix(0, 0)
	if _, cmd := m.Update(splashMsg{m.splash.start.Add(splashDuration / 2)}); cmd == nil || !m.splash.show {
		t.Fatal("the splash should keep animating until its time is up")
	}
	m.Update(splashMsg{m.splash.start.Add(splashDuration)})
	if m.splash.show {
		t.Fatal("the splash should close when its time is up")
	}
	if !strings.Contains(view(m), nameRow(m.agentName())) {
		t.Fatal("after the splash the normal view should be shown")
	}
}

func TestSplashSceneFillsTheScreen(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {40, 24}, {12, 4}} {
		for _, light := range []bool{false, true} {
			w, h := size[0], size[1]
			seen := map[string]bool{}
			for at := time.Duration(0); at <= splashDuration; at += splashFrame {
				frame := drawSplash(w, h, at, light)
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
	if end := ansi.Strip(drawSplash(40, 24, splashDuration-fadeOut, false)); !strings.Contains(end, titleText) || strings.Contains(end, bigTitle[0]) {
		t.Fatalf("narrow splash:\n%s", end)
	}
	// The colors follow the background.
	if drawSplash(100, 30, biteAt, false) == drawSplash(100, 30, biteAt, true) {
		t.Fatal("a light background should get other colors")
	}
}

func TestSplashTagIsTyped(t *testing.T) {
	half := ansi.Strip(drawSplash(100, 30, taglineAt+typeSpeed*6, false))
	if !strings.Contains(half, "a tiny▌") || strings.Contains(half, tagline) {
		t.Fatalf("the tagline should be typed letter by letter:\n%s", half)
	}
	if early := ansi.Strip(drawSplash(100, 30, titleAt-scrambleFor, false)); strings.Contains(early, "▀▄▄▀") {
		t.Fatalf("the title should not be there before it decodes:\n%s", early)
	}
}

func TestSplashCtrlCQuits(t *testing.T) {
	m, _ := newModel(t)
	m.splash.show = true
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("ctrl+c should quit")
	}
	if !m.splash.show {
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
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"alpha", nameRow(m.agentName())} {
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
	m.now = func() time.Time { return time.Now().Add(toolShown) } // the tool has run long enough to be named
	if !m.chat.pending || !strings.Contains(m.View().Content, "I will ask beta") || !strings.Contains(m.View().Content, "asking beta") {
		t.Fatalf("what the agent says on the way belongs in the chat:\n%s", m.View().Content)
	}
	close(gate) // beta answered, the agent thinks again and answers
	drive(t, m, nil, func() bool { return !m.chat.pending })

	c := m.chat
	// The call these words belong to goes below them.
	want := []entry{{mine: true, text: "ask beta for the time"},
		{text: "I will ask beta", calls: []call{{text: "asking beta", done: true}}}, {text: "beta says it is noon"}}
	if !sameEntries(c.entries, want) {
		t.Fatalf("the loop belongs in the history: %+v", c.entries)
	}
	view := trimmedLines(m)
	// Once the answer is in, its calls are summed up below the words.
	for _, want := range []string{"I will ask beta\n│   ↳ Used 1 tool", "beta says it is noon"} {
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

func TestChatShowsEveryCallOfTheLoop(t *testing.T) {
	m, b := newModel(t)
	written, read := make(chan struct{}), make(chan struct{})
	b.ask = func(_ context.Context, step func(Step), say func(string)) (string, error) {
		say("I read both")
		step(Step{Text: "reading a.go"})
		step(Step{Text: "reading a.go ✓ 3ms", Done: true})
		step(Step{Text: "reading b.go"})
		step(Step{Text: "reading b.go ✗ no such file", Done: true})
		say("")
		step(Step{Text: "reading d.go"})
		step(Step{Text: "writing c.go"})
		<-written
		step(Step{Text: "writing c.go ✓ 1ms", Done: true})
		<-read
		step(Step{Text: "reading d.go ✓ 9ms", Done: true})
		// A call that ended without having started changes nothing.
		step(Step{Text: "listing ~ ✓ 1ms", Done: true})
		return "", nil
	}
	drive(t, m, say(t, m, "fix it"), func() bool { return m.chat.doing == "writing c.go" })
	// While it is on its way, the calls that are already there are one line.
	if !strings.Contains(trimmedLines(m), "│   ↳ Used 4 tools, 1 failed\n") {
		t.Fatalf("the calls should be one line: %+v\n%s", m.chat, trimmedLines(m))
	}
	close(written)
	drive(t, m, nil, func() bool { return m.chat.doing == "reading d.go" }) // the read still runs
	close(read)
	drive(t, m, nil, func() bool { return !m.chat.pending })

	want := []entry{{mine: true, text: "fix it"},
		{text: "I read both", calls: []call{{text: "reading a.go ✓ 3ms", done: true}, {text: "reading b.go ✗ no such file", done: true}}},
		{calls: []call{{text: "reading d.go ✓ 9ms", done: true}, {text: "writing c.go ✓ 1ms", done: true}}}} // the answer said nothing at the end and leaves no entry of its own
	if !sameEntries(m.chat.entries, want) {
		t.Fatalf("the loop in the history: %+v", m.chat.entries)
	}
	view := ansi.Strip(m.View().Content)
	// The calls of all the rounds of the answer are one line, whatever they were.
	if !strings.Contains(trimmedLines(m), "I read both\n│   ↳ Used 4 tools, 1 failed\n") ||
		strings.Contains(view, "↳ ·") {
		t.Fatalf("the calls should be one line:\n%s", view)
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

// esc in the chat stops the answer on its way in the session shown, and only
// that one: on a screen it goes back, and without an answer it does nothing.
func TestEscapeStopsTheAnswer(t *testing.T) {
	m, b := newModel(t)
	if press(m, "esc") != nil || m.notice != "" || len(b.canceled) != 0 {
		t.Fatalf("nothing to stop: %q, %v", m.notice, b.canceled)
	}
	if strings.Contains(m.View().Content, "esc stop") {
		t.Fatal("esc stops nothing while no answer is on its way")
	}
	b.ask = func(ctx context.Context, step func(Step), _ func(string)) (string, error) {
		step(Step{Text: "asking beta"})
		<-ctx.Done()
		return "", ctx.Err()
	}
	first := m.chat
	drive(t, m, say(t, m, "ping"), func() bool { return first.doing == "asking beta" })

	// On a screen esc goes back, and the answer goes on.
	run(m, "/help")
	if press(m, "esc") != nil || m.screen != screenChat || len(b.canceled) != 0 || !first.pending {
		t.Fatalf("esc on a screen: screen %v, canceled %v", m.screen, b.canceled)
	}

	// Another session answers too: esc stops the one shown.
	drive(t, m, run(m, "/new"), func() bool { return true })
	second := m.chat
	drive(t, m, say(t, m, "pong"), func() bool { return second.doing == "asking beta" })
	if !strings.Contains(m.View().Content, "esc stop") {
		t.Fatalf("the footer should say what esc does:\n%s", m.View().Content)
	}
	cmd := press(m, "esc")
	if m.notice != "stopping the answer…" {
		t.Fatalf("notice %q", m.notice)
	}
	drive(t, m, cmd, func() bool { return !second.pending && m.notice == "answer stopped" })
	if e := second.entries[len(second.entries)-1]; !e.failed || e.text != "stopped before it answered" {
		t.Fatalf("entries %+v", second.entries)
	}
	if !first.pending || len(b.canceled) != 1 || b.canceled[0] != second.contextID {
		t.Fatalf("only the session shown is stopped: %v", b.canceled)
	}

	// Stopped, there is nothing more to stop, and the session answers again.
	if press(m, "esc") != nil || len(b.canceled) != 1 {
		t.Fatalf("stopped twice: %v", b.canceled)
	}
	b.ask = nil
	answered(t, m, say(t, m, "more"))
	if e := second.entries[len(second.entries)-1]; e.text != "pong" || e.failed {
		t.Fatalf("entries %+v", second.entries)
	}
	b.sessions.Cancel(context.Background(), first.contextID)
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
		"/quit":                 func(m *Model) tea.Cmd { return run(m, "/quit") },
		"ctrl+c":                func(m *Model) tea.Cmd { _, cmd := m.Update(ctrlC); return cmd },
		"ctrl+c on suggestions": func(m *Model) tea.Cmd { run(m, "/suggestions"); _, cmd := m.Update(ctrlC); return cmd },
		"ctrl+c on the help":    func(m *Model) tea.Cmd { run(m, "/help"); _, cmd := m.Update(ctrlC); return cmd },
		"ctrl+c on the splash":  func(m *Model) tea.Cmd { m.splash.show = true; _, cmd := m.Update(ctrlC); return cmd },
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

func TestNodeLifecycleCommandsAreCLIOnly(t *testing.T) {
	m, b := newModel(t)
	for _, name := range []string{"start", "stop"} {
		if cmd := run(m, "/"+name); cmd != nil || m.notice != "unknown command /"+name+": /help lists them" {
			t.Fatalf("/%s should not be a UI command: %q", name, m.notice)
		}
		if len(m.matching("/"+name)) != 0 || strings.Contains(m.helpView(), "/"+name) {
			t.Fatalf("/%s should not be suggested or listed in help", name)
		}
	}
	if b.started != 0 || b.stopped != 0 || b.setups != 0 {
		t.Fatal("CLI-only commands should not change the node")
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
		m.splash.show = true
		m.View() // the splash must survive tiny windows
		m.splash.show = false
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
	other.splash.show = true
	other.Resume(m.Current())
	drive(t, other, other.watch(other.chat), func() bool { return len(other.chat.entries) == len(m.chat.entries) })
	if other.splash.show || !sameEntries(other.chat.entries, m.chat.entries) || other.Current() != m.Current() {
		t.Fatalf("resumed %+v, want %+v", other.chat, m.chat)
	}
	if view := ansi.Strip(other.View().Content); !strings.Contains(view, "built and installed") || !strings.Contains(view, "restarted with the new build") {
		t.Fatalf("the resumed chat should be shown:\n%s", view)
	}
	if _, ok := other.Restarts(); ok {
		t.Fatal("a restart that happened before does not make the UI quit")
	}
}

// The empty chat follows the current node name.
func TestEmptyChatFollowsTheName(t *testing.T) {
	m, b := newModel(t)
	b.status.Name = "beta"
	m.Update(m.refreshStatus())
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, nameRow("beta")) {
		t.Fatalf("the empty chat does not name the agent:\n%s", view)
	}
}

// A paste goes to the input that is open: the one of a setting while it is
// edited, else the one of the chat.
func TestPasteGoesToTheInputThatIsOpen(t *testing.T) {
	m, _ := openSettings(t, someSettings())
	pick(t, m, nodeSection, "Name", "change")
	press(m, "enter")
	m.Update(tea.PasteMsg{Content: "-x"})
	if m.config.input.Value() != "alpha-x" || m.input.Value() != "" {
		t.Fatalf("edit %q, chat %q", m.config.input.Value(), m.input.Value())
	}
	press(m, "esc", "esc", "esc")
	m.Update(tea.PasteMsg{Content: "hello"})
	if m.input.Value() != "hello" || m.config.input.Value() != "alpha-x" {
		t.Fatalf("edit %q, chat %q", m.config.input.Value(), m.input.Value())
	}
}

// The call an ending one started as is the last that is still running with
// its text: not one that ended, nor one of other text.
func TestTheCallAnEndStartedAs(t *testing.T) {
	e := &entry{calls: []call{{text: "read a"}, {text: "read a", done: true}, {text: "list"}}}
	for done, want := range map[string]int{"read a ✓ 1s": 0, "list ✗ 2s": 2, "read b ✓": -1} {
		if got := e.started(done); got != want {
			t.Errorf("%q started as call %d, not %d", done, got, want)
		}
	}
}

// The line below the footer comes at once: the chat takes one row less for
// it the moment a suggestion waits.
func TestPendingLineResizesTheChatAtOnce(t *testing.T) {
	m, b := newModel(t)
	before := m.history.Height()
	b.trust = Contacts{Suggestions: []plugin.Suggestion{{ID: "s1", Text: "a change"}}}
	m.Update(m.loadContacts())
	if m.pendingRows() != 1 || m.history.Height() != before-1 {
		t.Fatalf("%d rows wait, the history has %d of %d", m.pendingRows(), m.history.Height(), before)
	}
}

// What is decided of an entry stays decided when the same entry is selected
// again, as it is when what waits comes anew.
func TestChoiceStaysWhenTheSameEntryIsSelected(t *testing.T) {
	m, b := newModel(t)
	b.trust = Contacts{Suggestions: []plugin.Suggestion{{ID: "s1", Text: "first change"}}}
	m.Update(m.loadContacts())
	run(m, "/suggestions")
	press(m, "right")
	if m.choice != reject {
		t.Fatalf("the first change is chosen %v", m.choice)
	}
	b.trust.Suggestions = append(b.trust.Suggestions, plugin.Suggestion{ID: "s2", Text: "second change"})
	m.Update(m.loadContacts())
	if m.suggestionRequests.sel != 0 || m.choice != reject {
		t.Fatalf("selected %d, chosen %v", m.suggestionRequests.sel, m.choice)
	}
}

// What came since the list was last open is unread, and only that: a list
// looked at before adds nothing again.
func TestUnreadCountsOnlyWhatCameSinceTheListWasSeen(t *testing.T) {
	m, b := newModel(t)
	b.trust = Contacts{Suggestions: []plugin.Suggestion{{ID: "s1", Text: "first"}}}
	m.Update(m.loadContacts())
	if m.suggestionRequests.unread != 1 {
		t.Fatalf("%d unread of one", m.suggestionRequests.unread)
	}
	b.trust.Suggestions = append(b.trust.Suggestions, plugin.Suggestion{ID: "s2", Text: "second"}, plugin.Suggestion{ID: "s3", Text: "third"})
	m.Update(m.loadContacts())
	if got := m.suggestionRequests.unread; got != 3 {
		t.Fatalf("%d unread of one seen and three waiting", got)
	}
}
