package tui

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// fakeSetup is a setup with one endpoint, which takes one key.
type fakeSetup struct {
	endpoint, name string
	key            string // the key the endpoint takes
	saved          string // the key known for the endpoint, "" for none
	models         plugin.SetupModels
	owner, peers   []string
	protected      []string
	connectErr     error
	saveErr        error

	tried  []string // the keys tried
	result [2]string
	ctx    context.Context
}

func newFakeSetup() *fakeSetup {
	return &fakeSetup{
		endpoint: "http://llm", name: "alpha", key: "sk-good",
		models:    plugin.SetupModels{Chat: []string{"a", "b", "c"}, All: []string{"a", "b", "c", "tts"}, Default: "b"},
		owner:     []string{plugin.AllTools},
		protected: []string{"~/.ssh", "~/.aws"},
	}
}

func (f *fakeSetup) Endpoint() string            { return f.endpoint }
func (f *fakeSetup) Name() string                { return f.name }
func (f *fakeSetup) HasKey(endpoint string) bool { return endpoint == f.endpoint && f.saved != "" }
func (f *fakeSetup) Connect(ctx context.Context, endpoint, key string) (plugin.SetupModels, error) {
	f.tried, f.ctx = append(f.tried, endpoint+" "+key), ctx
	switch {
	case f.connectErr != nil:
		return plugin.SetupModels{}, f.connectErr
	case key != f.key:
		return plugin.SetupModels{}, plugin.ErrKeyRejected
	}
	return f.models, nil
}
func (f *fakeSetup) Saved(ctx context.Context, endpoint string) (plugin.SetupModels, error) {
	return f.Connect(ctx, endpoint, f.saved)
}
func (f *fakeSetup) Rights() (owner, peers, protected []string) { return f.owner, f.peers, f.protected }
func (f *fakeSetup) Save(model, name string) error {
	f.result = [2]string{model, name}
	return f.saveErr
}

// onboard is the UI of a node that is not set up yet, at the welcome of the
// setup.
func onboard(t *testing.T) (*Model, *fakeBackend, *fakeSetup) {
	t.Helper()
	b := &fakeBackend{setup: newFakeSetup()}
	m := New(b, nil, nil)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Init()
	if m.onboard == nil || !m.onboard.first || m.showSplash {
		t.Fatal("a node that is not set up should open with the setup")
	}
	return m, b, b.setup
}

// typeIn replaces what is in the input of the setup with text.
func typeIn(m *Model, text string) {
	m.onboard.input.SetValue(text)
}

// dialog is what the setup shows, without colors.
func dialog(m *Model) string { return ansi.Strip(m.View().Content) }

// answer is the message the command of the setup comes back with.
func answer(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("want a command")
	}
	m.Update(cmd())
}

func TestSetupWalksThroughThePages(t *testing.T) {
	m, b, s := onboard(t)
	if v := dialog(m); !strings.Contains(v, "Welcome to ouroboros") || !strings.Contains(v, "esc quit") {
		t.Fatalf("welcome:\n%s", v)
	}
	press(m, "x") // nothing to type on the welcome
	press(m, "enter")
	if v := dialog(m); m.onboard.step != stepEndpoint || !strings.Contains(v, "1/5 · LLM endpoint") || !strings.Contains(v, "http://llm") {
		t.Fatalf("endpoint:\n%s", v)
	}
	typeIn(m, " http://other/ ")
	press(m, "enter")
	if v := dialog(m); m.onboard.endpoint != "http://other" || !strings.Contains(v, "for http://other") || !strings.Contains(v, "Enter for none") {
		t.Fatalf("key:\n%s", v)
	}

	// The key is not shown, and one the endpoint does not take is asked for again.
	press(m, "s", "k")
	if v := dialog(m); strings.Contains(v, "sk") || !strings.Contains(v, "••") {
		t.Fatalf("the key shows:\n%s", v)
	}
	cmd := press(m, "enter")
	if v := dialog(m); !strings.Contains(v, "connecting…") {
		t.Fatalf("while connecting:\n%s", v)
	}
	if press(m, "esc"); m.onboard.step != stepKey {
		t.Fatal("keys wait while the endpoint is asked")
	}
	answer(t, m, cmd)
	if v := dialog(m); m.onboard.step != stepKey || !strings.Contains(v, "API key rejected") || m.onboard.input.Value() != "" {
		t.Fatalf("a rejected key:\n%s", v)
	}
	typeIn(m, "sk-good")
	answer(t, m, press(m, "enter"))
	if !slices.Equal(s.tried, []string{"http://other sk", "http://other sk-good"}) || s.ctx != m.ctx {
		t.Fatalf("tried %v", s.tried)
	}

	// The default model is selected; the selection stays in the list.
	if v := dialog(m); m.onboard.step != stepModel || !strings.Contains(v, "▸ b") || !strings.Contains(v, "2 of 3") {
		t.Fatalf("models:\n%s", v)
	}
	press(m, "up", "k", "x")
	if m.onboard.model != 0 {
		t.Fatalf("selected %d", m.onboard.model)
	}
	press(m, "down", "j", "down", "down")
	if m.onboard.model != 2 {
		t.Fatalf("selected %d", m.onboard.model)
	}
	press(m, "enter")
	if v := dialog(m); m.onboard.step != stepName || !strings.Contains(v, "alpha") {
		t.Fatalf("name:\n%s", v)
	}
	typeIn(m, " ")
	if press(m, "enter"); m.onboard.step != stepName || !strings.Contains(dialog(m), "the node needs a name") {
		t.Fatal("a node without a name")
	}
	typeIn(m, "luna")
	press(m, "enter")
	if v := dialog(m); m.onboard.step != stepRights || !strings.Contains(v, "all tools") || !strings.Contains(v, "~/.ssh ~/.aws") {
		t.Fatalf("rights:\n%s", v)
	}

	// Saving: the setup saves what was chosen and starts the node with it.
	b.status.Running = true
	cmd = press(m, "enter")
	if !strings.Contains(dialog(m), "saving…") {
		t.Fatal("saving should show")
	}
	m.Update(cmd())
	if s.result != [2]string{"c", "luna"} || m.onboard == nil || m.onboard.saved.IsZero() {
		t.Fatalf("saved %v", s.result)
	}
}

// Going back keeps what was entered; esc on the welcome quits the UI of a
// node that has no setup yet.
func TestSetupGoesBack(t *testing.T) {
	m, _, s := onboard(t)
	s.saved = "sk-good"
	press(m, "enter")
	typeIn(m, "http://llm")
	press(m, "enter")
	if !strings.Contains(dialog(m), "Enter keeps the saved key") {
		t.Fatal("a known key should be offered")
	}
	answer(t, m, press(m, "enter"))
	if !slices.Equal(s.tried, []string{"http://llm sk-good"}) || m.onboard.step != stepModel {
		t.Fatalf("tried %v", s.tried)
	}
	press(m, "enter")
	typeIn(m, "luna")
	press(m, "enter", "esc")
	if m.onboard.step != stepName || m.onboard.input.Value() != "luna" {
		t.Fatalf("back on %v with %q", m.onboard.step, m.onboard.input.Value())
	}
	press(m, "esc", "esc", "esc")
	if m.onboard.step != stepEndpoint || m.onboard.input.Value() != "http://llm" {
		t.Fatalf("back on %v with %q", m.onboard.step, m.onboard.input.Value())
	}
	typeIn(m, "")
	if press(m, "enter"); m.onboard.step != stepEndpoint || !strings.Contains(dialog(m), "the endpoint is needed") {
		t.Fatal("an empty endpoint")
	}
	press(m, "esc")
	if cmd := press(m, "esc"); cmd == nil || m.ctx.Err() == nil {
		t.Fatal("esc on the welcome should quit")
	}
}

// Set up again from the chat, the setup goes back to it.
func TestSetupFromTheChat(t *testing.T) {
	m, b := newModel(t)
	if run(m, "/setup"); m.onboard == nil || m.onboard.first {
		t.Fatal("/setup should open the setup")
	}
	if v := dialog(m); !strings.Contains(v, "esc back to the chat") {
		t.Fatalf("welcome:\n%s", v)
	}
	press(m, "esc")
	if m.onboard != nil || m.ctx.Err() != nil || b.stopped != 0 {
		t.Fatal("esc should go back to the chat")
	}

	b.fail = map[string]error{"setup": errors.New("boom")}
	if cmd := run(m, "/setup"); cmd != nil || m.onboard != nil || m.notice != "setup: boom" || !m.noticeErr {
		t.Fatalf("notice %q", m.notice)
	}
}

func TestSetupErrors(t *testing.T) {
	m, _, s := onboard(t)
	press(m, "enter", "enter")
	s.connectErr = errors.New("cannot list models")
	answer(t, m, press(m, "enter"))
	if !strings.Contains(dialog(m), "✗ cannot list models") || m.onboard.step != stepKey {
		t.Fatalf("an endpoint that cannot be asked:\n%s", dialog(m))
	}
	s.connectErr = nil
	typeIn(m, "sk-good")
	answer(t, m, press(m, "enter"))
	press(m, "enter", "enter")
	s.saveErr = errors.New("disk full")
	answer(t, m, press(m, "enter"))
	if !strings.Contains(dialog(m), "✗ disk full") || !m.onboard.saved.IsZero() {
		t.Fatalf("a setup that cannot be saved:\n%s", dialog(m))
	}

	// Saved, the node is started with it; what fails on the way is said.
	s.saveErr = nil
	for fail, want := range map[string]string{"stop": "stop failed", "start": "start failed", "": ""} {
		m, b, s := onboard(t)
		s.saved = "sk-good"
		press(m, "enter", "enter")
		answer(t, m, press(m, "enter"))
		press(m, "enter", "enter")
		_, restart := m.Update(press(m, "enter")())
		b.fail = map[string]error{fail: errors.New(want)}
		msg := restart().(actionMsg)
		if want == "" && (msg.err != nil || msg.done != "node set up and started" || b.started != 1) {
			t.Fatalf("got %+v", msg)
		}
		if want != "" && (msg.err == nil || msg.err.Error() != want) {
			t.Fatalf("%s: got %+v", fail, msg)
		}
	}
}

// Answers that come when the setup is elsewhere are dropped.
func TestSetupDropsLateAnswers(t *testing.T) {
	m, _, _ := onboard(t)
	if _, cmd := m.Update(connectedMsg{}); cmd != nil || m.onboard.step != stepWelcome {
		t.Fatal("an answer of the endpoint on the welcome")
	}
	if _, cmd := m.Update(savedMsg{}); cmd != nil || !m.onboard.saved.IsZero() {
		t.Fatal("a saved setup on the welcome")
	}
	m.closeSetup()
	m.Update(connectedMsg{})
	m.Update(savedMsg{})
}

// The snake curls up with every page, and once the setup is saved it turns
// and shrinks into the logo, where the splash takes over with the bite.
func TestSetupScene(t *testing.T) {
	m, _, s := onboard(t)
	o := m.onboard
	start := o.at
	if o.curled(start) != curlFrom {
		t.Fatal("the welcome starts uncurled")
	}
	press(m, "enter")
	if o.curled(start) != curlFrom || o.curled(start.Add(curlDuration)) <= curlFrom {
		t.Fatal("the next page curls the snake up, bit by bit")
	}
	// Frames move the scene on.
	at := start.Add(time.Second)
	if _, cmd := m.Update(splashMsg{at}); cmd == nil || o.at != at {
		t.Fatal("the setup should be animated")
	}
	before := m.View().Content
	m.Update(splashMsg{at.Add(200 * time.Millisecond)})
	if after := m.View().Content; after == before {
		t.Fatal("the scene should move")
	}

	s.saved = "sk-good"
	press(m, "enter")
	answer(t, m, press(m, "enter"))
	press(m, "enter", "enter")
	m.Update(press(m, "enter")())
	saved := o.saved
	if saved.IsZero() || strings.Contains(m.View().Content, "Welcome") || strings.Contains(m.View().Content, "╭") {
		t.Fatal("the dialog goes when the setup is saved")
	}
	if p, bright := o.pose(100, 30); p.turn != 0 || bright != setupDim {
		t.Fatalf("the snake should not turn yet: %+v", p)
	}
	m.Update(splashMsg{saved.Add(turnAt + turnDuration/2)})
	if p, bright := o.pose(100, 30); p.turn <= 0 || p.turn >= 2*math.Pi || bright <= setupDim || bright >= 1 {
		t.Fatalf("the snake should be turning: %+v", p)
	}
	end := saved.Add(turnAt + turnDuration)
	m.Update(splashMsg{end.Add(-time.Millisecond)})
	if m.onboard == nil {
		t.Fatal("the scene ended early")
	}
	// At the end the snake is the logo of the splash, at its place.
	o.at = end
	p, bright := o.pose(100, 30)
	_, _, left, top, _ := splashLayout(100, 30)
	if math.Abs(wrap(p.turn+1e-9)) > 1e-6 || p.scale != 1 || p.x != float64(left*2)+ringX || p.y != float64(top*4)+ringY || bright != 1 || o.curled(end) != bodyArc {
		t.Fatalf("got %+v, %v", p, bright)
	}
	if _, cmd := m.Update(splashMsg{end}); cmd == nil || m.onboard != nil || !m.showSplash || m.splashElapsed != biteAt {
		t.Fatal("the splash should take over")
	}
	if m.skyShift != end.Sub(o.opened)-biteAt {
		t.Fatalf("the stars should stay where they are: %v", m.skyShift)
	}
}

// A key skips the end of the scene.
func TestSetupSceneIsSkipped(t *testing.T) {
	m, _, s := onboard(t)
	s.saved = "sk-good"
	press(m, "enter", "enter")
	answer(t, m, press(m, "enter"))
	press(m, "enter", "enter")
	m.Update(press(m, "enter")())
	if press(m, "x"); m.onboard != nil || m.showSplash {
		t.Fatal("a key should skip to the chat")
	}
}

func TestSetupCtrlCQuits(t *testing.T) {
	m, _, _ := onboard(t)
	if _, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})); cmd == nil || m.ctx.Err() == nil {
		t.Fatal("ctrl+c should quit")
	}
}

// What is not a key goes to the input of the setup.
func TestSetupTakesPastes(t *testing.T) {
	m, _, _ := onboard(t)
	press(m, "enter")
	typeIn(m, "")
	m.Update(tea.PasteMsg{Content: "http://pasted"})
	if m.onboard.input.Value() != "http://pasted" {
		t.Fatalf("got %q", m.onboard.input.Value())
	}
	m.Update(tea.BackgroundColorMsg{Color: lipgloss.Color("#ffffff")})
	if !m.light || !strings.Contains(m.View().Content, "pasted") {
		t.Fatal("the setup should follow the background")
	}
}

// The UI of a node that is set up starts with the splash; one whose status
// cannot be read too.
func TestInitOpensTheSetupOnlyWhenNeeded(t *testing.T) {
	for _, b := range []*fakeBackend{{status: Status{Configured: true}}, {fail: map[string]error{"status": errors.New("boom")}}} {
		m := New(b, nil, nil)
		m.Init()
		if m.onboard != nil || !m.showSplash || b.setups != 0 {
			t.Fatalf("%+v: the splash should open", b)
		}
		// The frames of the splash are coming: the setup opened now uses them.
		if cmd := m.animate(); cmd != nil {
			t.Fatal("a second line of frames")
		}
	}
}

func TestSetupDialog(t *testing.T) {
	o := &onboarding{setup: newFakeSetup(), step: stepRights}
	f := o.setup.(*fakeSetup)
	f.owner, f.peers, f.protected = []string{"read", "write"}, nil, nil
	v := ansi.Strip(o.dialog(""))
	if !strings.Contains(v, "For you    read, write") || !strings.Contains(v, "For peers  nothing") || !strings.Contains(v, "Protected  nothing") {
		t.Fatalf("rights:\n%s", v)
	}
	if got := ansi.Strip(o.progress()); got != "● ● ● ● ◉" {
		t.Fatalf("progress %q", got)
	}
	o.step = stepEndpoint
	if got := ansi.Strip(o.progress()); got != "◉ ○ ○ ○ ○" {
		t.Fatalf("progress %q", got)
	}

	// Of many models a few are shown around the one selected.
	o.models.Chat = []string{"m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9"}
	for selected, want := range map[int][]string{0: {"▸ m0", "  m5"}, 5: {"  m2", "▸ m5", "  m7"}, 9: {"  m4", "▸ m9"}} {
		o.model = selected
		list := ansi.Strip(strings.Join(o.modelList(30), "\n"))
		for _, w := range want {
			if !strings.Contains(list, w) {
				t.Errorf("selected %d: %q is missing:\n%s", selected, w, list)
			}
		}
		if lines := strings.Count(list, "\n"); lines != 6 {
			t.Errorf("selected %d: %d lines", selected, lines+1)
		}
	}
}

// The snake is as large as fits, within bounds.
func TestSetupScale(t *testing.T) {
	if s := scale(100, 30); s <= 1 || s >= 2.5 {
		t.Fatalf("scale %v", s)
	}
	if scale(1000, 1000) != 2.5 || scale(10, 5) != 0.6 {
		t.Fatal("the scale should stay within its bounds")
	}
	// Uncurled most, the snake fits on the screen.
	for _, size := range [][2]int{{100, 30}, {80, 24}, {200, 60}} {
		w, h := size[0], size[1]
		o := &onboarding{}
		o.curl.from, o.curl.to = curlFrom, curlFrom
		p, _ := o.pose(w, h)
		cells := poseCells(p, 1, false, w, h)
		for _, edge := range []rune{cells[0][w/2].dots, cells[h-1][w/2].dots} {
			if edge != 0 {
				t.Errorf("%dx%d: the snake touches the edge", w, h)
			}
		}
		drawn := 0
		for _, row := range cells {
			for _, c := range row {
				if c.dots != 0 {
					drawn++
				}
			}
		}
		if drawn < w*h/40 {
			t.Errorf("%dx%d: the snake is hardly there", w, h)
		}
	}
}
