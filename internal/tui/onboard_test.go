package tui

import (
	"context"
	"errors"
	"fmt"
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

// fakeSetup is a setup whose providers sign in at one endpoint, which takes
// one key: they ask for the endpoint and then for the key, as the provider
// for OpenAI-compatible APIs does.
type fakeSetup struct {
	endpoint, name string
	key            string // the key the endpoint takes
	saved          string // the key known for the endpoint, "" for none
	browser        string // the page a provider opens before it asks, "" for none
	providers      []plugin.SetupProvider
	accesses       []plugin.SetupAccess
	models         plugin.SetupModels
	owner, peers   []string
	protected      []string
	loginErr       error
	saveErr        error
	// fail is what fails, by its name: "accesses", "use", "rename", "remove".
	fail map[string]error
	note string // what removing an access tells

	tried  []string // the endpoints and keys tried
	did    []string // what was done with the accesses, like "use a1"
	result [2]string
	ctx    context.Context
}

func newFakeSetup() *fakeSetup {
	return &fakeSetup{
		endpoint: "http://llm", name: "alpha", key: "sk-good",
		providers: []plugin.SetupProvider{{Name: "openai", Title: "Custom Provider"}},
		models:    plugin.SetupModels{Chat: []string{"a", "b", "c"}, All: []string{"a", "b", "c", "tts"}, Default: "b"},
		owner:     []string{plugin.AllTools},
		protected: []string{"~/.ssh", "~/.aws"},
	}
}

func (f *fakeSetup) Name() string                      { return f.name }
func (f *fakeSetup) Providers() []plugin.SetupProvider { return f.providers }
func (f *fakeSetup) Accesses() ([]plugin.SetupAccess, error) {
	if err := f.fail["accesses"]; err != nil {
		return nil, err
	}
	return slices.Clone(f.accesses), nil
}

func (f *fakeSetup) Login(ctx context.Context, provider string, d plugin.Dialog) (plugin.SetupModels, error) {
	f.did = append(f.did, "login "+provider)
	return f.signIn(ctx, d)
}

func (f *fakeSetup) Again(ctx context.Context, id string, d plugin.Dialog) (plugin.SetupModels, error) {
	f.did = append(f.did, "again "+id)
	return f.signIn(ctx, d)
}

// signIn asks as a provider does: for the endpoint, then for the key until
// the endpoint takes it.
func (f *fakeSetup) signIn(ctx context.Context, d plugin.Dialog) (plugin.SetupModels, error) {
	f.ctx = ctx
	if f.browser != "" {
		d.Tell("Sign in on the page.")
		d.Open(f.browser)
	}
	endpoint, err := d.Ask(ctx, plugin.Question{Title: "LLM endpoint", Text: []string{"Any OpenAI-compatible API:"}, Default: f.endpoint})
	if err != nil {
		return plugin.SetupModels{}, err
	}
	known := ""
	if endpoint == f.endpoint {
		known = f.saved
	}
	for {
		q := plugin.Question{Title: "API key", Text: []string{"for " + endpoint}, Secret: true, Hint: "Enter for none"}
		if known != "" {
			q.Default, q.Known, q.Hint = known, true, "Enter keeps the saved key"
		}
		key, err := d.Ask(ctx, q)
		if err != nil {
			return plugin.SetupModels{}, err
		}
		f.tried = append(f.tried, endpoint+" "+key)
		switch {
		case f.loginErr != nil:
			return plugin.SetupModels{}, f.loginErr
		case key == f.key:
			return f.models, nil
		}
		d.Reject("API key rejected, try again")
		known = ""
	}
}

func (f *fakeSetup) Use(ctx context.Context, id string) (plugin.SetupModels, error) {
	f.did, f.ctx = append(f.did, "use "+id), ctx
	return f.models, f.fail["use"]
}

func (f *fakeSetup) Rename(id, name string) error {
	f.did = append(f.did, "rename "+id+" "+name)
	if err := f.fail["rename"]; err != nil {
		return err
	}
	for i := range f.accesses {
		if f.accesses[i].ID == id {
			f.accesses[i].Name, f.accesses[i].Label = name, name
		}
	}
	return nil
}

func (f *fakeSetup) Remove(_ context.Context, id string) (string, error) {
	f.did = append(f.did, "remove "+id)
	if err := f.fail["remove"]; err != nil {
		return "", err
	}
	f.accesses = slices.DeleteFunc(f.accesses, func(a plugin.SetupAccess) bool { return a.ID == id })
	return f.note, nil
}

func (f *fakeSetup) Rights() (owner, peers, protected []string) { return f.owner, f.peers, f.protected }
func (f *fakeSetup) Save(model, name string) error {
	f.result = [2]string{model, name}
	return f.saveErr
}

// twoAccesses are the accesses of a node that has two: the first is in use.
func twoAccesses() []plugin.SetupAccess {
	return []plugin.SetupAccess{
		{ID: "a1", Provider: "openai", Label: "localhost", About: "http://llm", Active: true},
		{ID: "b2", Provider: "chatgpt", Name: "Work", Label: "Work", About: "me@example.com", Missing: true},
	}
}

// onboard is the UI of a node that is not set up yet, at the welcome of the
// setup.
func onboard(t *testing.T) (*Model, *fakeBackend, *fakeSetup) {
	t.Helper()
	b := &fakeBackend{setup: newFakeSetup()}
	m := New(b, nil, nil)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Init()
	if m.onboard == nil || !m.onboard.first || m.splash.show {
		t.Fatal("a node that is not set up should open with the setup")
	}
	return m, b, b.setup
}

// configured is the UI of a node that has the accesses of twoAccesses, with
// its setup open at the page of the accesses.
func configured(t *testing.T) (*Model, *fakeSetup) {
	t.Helper()
	m, b := newModel(t)
	b.setup = newFakeSetup()
	b.setup.accesses = twoAccesses()
	b.setup.providers = append(b.setup.providers, plugin.SetupProvider{Name: "chatgpt", Title: "ChatGPT"})
	run(m, "/setup")
	press(m, "enter")
	if m.onboard == nil || m.onboard.step != stepAccess {
		t.Fatal("the setup of a node with accesses should show them")
	}
	return m, b.setup
}

// typeIn replaces what is in the input of the setup with text.
func typeIn(m *Model, text string) {
	m.onboard.input.SetValue(text)
}

// shown is what the setup shows, without colors.
func shown(m *Model) string { return ansi.Strip(m.View().Content) }

// said is what the setup shows as running text: without the frame of its
// box and the breaks of its lines, which a long text is wrapped at.
func said(m *Model) string {
	box := ansi.Strip(m.onboard.dialog("", false))
	text := strings.NewReplacer("│", " ", "╭", " ", "╮", " ", "╰", " ", "╯", " ", "─", " ").Replace(box)
	return strings.Join(strings.Fields(text), " ")
}

// answer is the message the command of the setup comes back with.
func answer(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("want a command")
	}
	m.Update(cmd())
}

// provider hands the UI what the provider that signs in wants next: a
// question, a line, or its end. It waits for the provider.
func provider(t *testing.T, m *Model) tea.Cmd {
	t.Helper()
	if m.onboard == nil || m.onboard.login == nil {
		t.Fatal("no provider is signing in")
	}
	_, cmd := m.Update(m.onboard.login.next())
	return cmd
}

// reply answers the question the provider asked with text and hands the UI
// what the provider wants next.
func reply(t *testing.T, m *Model, text string) {
	t.Helper()
	if m.onboard.login == nil || m.onboard.login.asked == nil {
		t.Fatalf("nothing is asked:\n%s", shown(m))
	}
	typeIn(m, text)
	press(m, "enter")
	provider(t, m)
}

// toRights walks a new node through the setup up to its last page, with what
// is offered.
func toRights(t *testing.T, m *Model) {
	t.Helper()
	press(m, "enter")
	provider(t, m)    // the endpoint is asked
	reply(t, m, "")   // the key is asked
	reply(t, m, "")   // signed in: the models
	press(m, "enter") // the model
	press(m, "enter") // the name
	if m.onboard.step != stepRights {
		t.Fatalf("on %v:\n%s", m.onboard.step, shown(m))
	}
}

func TestSetupWalksThroughThePages(t *testing.T) {
	m, b, s := onboard(t)
	if v := shown(m); !strings.Contains(v, "Welcome to ouroboros") || !strings.Contains(v, "esc quit") {
		t.Fatalf("welcome:\n%s", v)
	}
	press(m, "x") // nothing to type on the welcome
	// A new node with one provider has nothing to pick: the provider asks.
	press(m, "enter")
	if v := shown(m); m.onboard.step != stepLogin || !strings.Contains(v, "1/4 · Sign in") || !strings.Contains(v, "one moment…") {
		t.Fatalf("while the provider starts:\n%s", v)
	}
	if press(m, "enter", "x"); m.onboard.step != stepLogin || m.onboard.login == nil {
		t.Fatal("keys wait while the provider works")
	}
	provider(t, m)
	if v := shown(m); !strings.Contains(v, "1/4 · LLM endpoint") || !strings.Contains(v, "Any OpenAI-compatible API:") || !strings.Contains(v, "http://llm") {
		t.Fatalf("endpoint:\n%s", v)
	}
	if !slices.Equal(s.did, []string{"login openai"}) || s.ctx == m.ctx || s.ctx.Err() != nil {
		t.Fatalf("did %v", s.did)
	}
	reply(t, m, " http://other ")
	if v := shown(m); !strings.Contains(v, "1/4 · API key") || !strings.Contains(v, "for http://other") || !strings.Contains(v, "Enter for none") {
		t.Fatalf("key:\n%s", v)
	}

	// The key is not shown, and one the endpoint does not take is asked for again.
	press(m, "s", "k")
	if v := shown(m); strings.Contains(v, "sk") || !strings.Contains(v, "••") {
		t.Fatalf("the key shows:\n%s", v)
	}
	press(m, "enter")
	if v := shown(m); !strings.Contains(v, "one moment…") || m.onboard.login.asked != nil {
		t.Fatalf("while the provider tries the key:\n%s", v)
	}
	provider(t, m) // rejected
	provider(t, m) // asked again
	if v := shown(m); !strings.Contains(v, "✗ API key rejected, try again") || m.onboard.input.Value() != "" || !strings.Contains(v, "1/4 · API key") {
		t.Fatalf("a rejected key:\n%s", v)
	}
	reply(t, m, "sk-good")
	if !slices.Equal(s.tried, []string{"http://other sk", "http://other sk-good"}) {
		t.Fatalf("tried %v", s.tried)
	}
	// What is no secret is offered again; a key is not kept.
	if !slices.Equal(mapKeys(m.onboard.answers), []string{"LLM endpoint"}) || m.onboard.answers["LLM endpoint"] != "http://other" {
		t.Fatalf("kept %v", m.onboard.answers)
	}

	// The default model is selected; the selection stays in the list.
	if v := shown(m); m.onboard.step != stepModel || m.onboard.login != nil || !strings.Contains(v, "2/4 · Model") || !strings.Contains(v, "▸ b") || !strings.Contains(v, "2 of 3") {
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
	if v := shown(m); m.onboard.step != stepName || !strings.Contains(v, "3/4 · Node name") || !strings.Contains(v, "alpha") {
		t.Fatalf("name:\n%s", v)
	}
	typeIn(m, " ")
	if press(m, "enter"); m.onboard.step != stepName || !strings.Contains(shown(m), "the node needs a name") {
		t.Fatal("a node without a name")
	}
	typeIn(m, "luna")
	press(m, "enter")
	if v := shown(m); m.onboard.step != stepRights || !strings.Contains(v, "4/4 · What your agent may do") || !strings.Contains(v, "all tools") || !strings.Contains(v, "~/.ssh ~/.aws") {
		t.Fatalf("rights:\n%s", v)
	}

	// Saving: the setup saves what was chosen and starts the node with it.
	b.status.Running = true
	cmd := press(m, "enter")
	if !strings.Contains(shown(m), "saving…") {
		t.Fatal("saving should show")
	}
	if press(m, "esc"); m.onboard.step != stepRights {
		t.Fatal("keys wait while the setup is saved")
	}
	m.Update(cmd())
	if s.result != [2]string{"c", "luna"} || m.onboard == nil || m.onboard.saved.IsZero() {
		t.Fatalf("saved %v", s.result)
	}
}

// mapKeys are the keys of m, sorted.
func mapKeys(m map[string]string) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Going back keeps what was entered; esc on the welcome quits the UI of a
// node that has no setup yet.
func TestSetupGoesBack(t *testing.T) {
	m, _, s := onboard(t)
	s.saved = "sk-good"
	press(m, "enter")
	provider(t, m)
	reply(t, m, "")
	if v := shown(m); !strings.Contains(v, "Enter keeps the saved key") || m.onboard.input.Value() != "" {
		t.Fatalf("a known key should be offered, and not shown:\n%s", v)
	}
	first := s.ctx
	reply(t, m, "") // Enter takes the key that is known
	if !slices.Equal(s.tried, []string{"http://llm sk-good"}) || m.onboard.step != stepModel {
		t.Fatalf("tried %v", s.tried)
	}
	press(m, "enter")
	typeIn(m, "luna")
	press(m, "enter", "esc")
	if m.onboard.step != stepName || m.onboard.input.Value() != "luna" {
		t.Fatalf("back on %v with %q", m.onboard.step, m.onboard.input.Value())
	}
	// Back from the models, the provider asks anew, with what was entered.
	press(m, "esc", "esc")
	if m.onboard.step != stepLogin || m.onboard.login == nil {
		t.Fatalf("back on %v", m.onboard.step)
	}
	provider(t, m)
	if m.onboard.input.Value() != "http://llm" || len(s.did) != 2 {
		t.Fatalf("asked again with %q, did %v", m.onboard.input.Value(), s.did)
	}
	// Back from what the provider asks, it is given up.
	second := s.ctx
	press(m, "esc")
	if m.onboard.step != stepWelcome || m.onboard.login != nil || second.Err() == nil || first == second {
		t.Fatalf("back on %v", m.onboard.step)
	}
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
	if v := shown(m); !strings.Contains(v, "esc back to the chat") {
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
	b.fail = nil
	b.setup.fail = map[string]error{"accesses": errors.New("no credentials")}
	if cmd := run(m, "/setup"); cmd != nil || m.onboard != nil || m.notice != "setup: no credentials" || !m.noticeErr {
		t.Fatalf("notice %q", m.notice)
	}
}

// A node that has accesses shows them: one is taken as it is, with enter,
// and the setup goes on to its models.
func TestSetupShowsTheAccesses(t *testing.T) {
	m, s := configured(t)
	v := shown(m)
	for _, want := range []string{"1/4 · Access", "▸ localhost · http://llm  in use", "  Work · me@example.com  (no chatgpt", "  + add another access", "enter use · l sign in again", "r rename · d remove · esc back"} {
		if !strings.Contains(v, want) {
			t.Fatalf("no %q in:\n%s", want, v)
		}
	}
	// The selection stays in the list, which ends with the row that adds.
	press(m, "up", "k")
	if m.onboard.sel != 0 {
		t.Fatalf("selected %d", m.onboard.sel)
	}
	press(m, "down", "j", "down", "down")
	if m.onboard.sel != 2 {
		t.Fatalf("selected %d", m.onboard.sel)
	}
	// On the row that adds there is no access to sign in to, rename or remove.
	if cmd := press(m, "l", "r", "d"); cmd != nil || m.onboard.step != stepAccess || m.onboard.renaming != "" || len(s.did) != 0 {
		t.Fatalf("did %v", s.did)
	}
	press(m, "up")

	cmd := press(m, "enter")
	if v := shown(m); !strings.Contains(v, "connecting…") {
		t.Fatalf("while the access is asked:\n%s", v)
	}
	if press(m, "esc", "d"); m.onboard.step != stepAccess || len(s.did) != 0 {
		t.Fatal("keys wait while the access is asked")
	}
	answer(t, m, cmd)
	if !slices.Equal(s.did, []string{"use b2"}) || s.ctx != m.ctx || m.onboard.step != stepModel {
		t.Fatalf("did %v, on %v", s.did, m.onboard.step)
	}
	// Back from the models, the accesses are there again.
	if press(m, "esc"); m.onboard.step != stepAccess || m.onboard.sel != 0 {
		t.Fatalf("back on %v, selected %d", m.onboard.step, m.onboard.sel)
	}
	if press(m, "esc"); m.onboard.step != stepWelcome {
		t.Fatalf("back on %v", m.onboard.step)
	}
}

// An access that cannot be taken says why. One that refuses its key is
// signed in to again right away.
func TestSetupAccessThatCannotBeUsed(t *testing.T) {
	m, s := configured(t)
	s.fail = map[string]error{"use": errors.New("no route to host")}
	answer(t, m, press(m, "enter"))
	if v := shown(m); m.onboard.step != stepAccess || !strings.Contains(v, "✗ no route to host") {
		t.Fatalf("an access that is not reached:\n%s", v)
	}
	s.fail["use"] = plugin.ErrKeyRejected
	answer(t, m, press(m, "enter"))
	if v := said(m); m.onboard.step != stepLogin || !strings.Contains(v, "✗ API key rejected: sign in again") {
		t.Fatalf("an access that refuses its key:\n%s", v)
	}
	provider(t, m)
	if !slices.Equal(s.did, []string{"use a1", "use a1", "again a1"}) || m.onboard.login.asked == nil {
		t.Fatalf("did %v", s.did)
	}
	// Models that are none are nothing to go on with.
	s.fail, s.models = nil, plugin.SetupModels{}
	press(m, "esc", "esc")
	answer(t, m, press(m, "enter"))
	if v := shown(m); m.onboard.step != stepAccess || !strings.Contains(v, "✗ the access offers no model") {
		t.Fatalf("an access without models:\n%s", v)
	}
}

// The accesses are managed where the node is set up: signed in to again,
// renamed and removed.
func TestSetupManagesTheAccesses(t *testing.T) {
	m, s := configured(t)

	// Sign in again: the provider of the access asks.
	press(m, "down", "l")
	provider(t, m)
	if !slices.Equal(s.did, []string{"again b2"}) || m.onboard.step != stepLogin || !strings.Contains(shown(m), "LLM endpoint") {
		t.Fatalf("did %v:\n%s", s.did, shown(m))
	}
	// With several providers, back from one that asks is the list of them,
	// and back from that the accesses.
	if press(m, "esc"); m.onboard.step != stepProvider {
		t.Fatalf("back on %v", m.onboard.step)
	}
	if press(m, "esc"); m.onboard.step != stepAccess {
		t.Fatalf("back on %v", m.onboard.step)
	}

	// Rename: the name it has is offered, esc keeps it, enter saves another.
	press(m, "down", "r")
	if v := shown(m); m.onboard.renaming != "b2" || m.onboard.input.Value() != "Work" || !strings.Contains(v, "What do you call this access?") || !strings.Contains(v, "enter save · esc back") {
		t.Fatalf("renaming:\n%s", v)
	}
	if press(m, "esc"); m.onboard.renaming != "" || m.onboard.step != stepAccess || len(s.did) != 1 {
		t.Fatalf("esc while renaming: on %v, did %v", m.onboard.step, s.did)
	}
	press(m, "r")
	typeIn(m, " Job")
	press(m, "s")
	answer(t, m, press(m, "enter"))
	if v := shown(m); !slices.Contains(s.did, "rename b2 Jobs") || !strings.Contains(v, "▸ Jobs · me@example.com") || m.onboard.renaming != "" {
		t.Fatalf("did %v:\n%s", s.did, v)
	}
	s.fail = map[string]error{"rename": errors.New("disk full")}
	press(m, "r")
	answer(t, m, press(m, "enter"))
	if v := shown(m); !strings.Contains(v, "✗ disk full") || !strings.Contains(v, "Jobs") {
		t.Fatalf("a name that cannot be saved:\n%s", v)
	}

	// Remove: what the setup tells of it shows, and the list is read anew.
	s.fail, s.note = nil, "the agents answer with a of localhost now."
	cmd := press(m, "d")
	if !strings.Contains(shown(m), "removing…") {
		t.Fatalf("while removing:\n%s", shown(m))
	}
	answer(t, m, cmd)
	if v := shown(m); !slices.Contains(s.did, "remove b2") || strings.Contains(v, "Jobs") || !strings.Contains(v, "the agents answer with a of localhost") || m.onboard.sel != 1 {
		t.Fatalf("did %v, selected %d:\n%s", s.did, m.onboard.sel, v)
	}
	press(m, "up")
	s.fail = map[string]error{"remove": plugin.ErrLastAccess}
	answer(t, m, press(m, "d"))
	if v := shown(m); !strings.Contains(v, "✗ the last access cannot be removed") || !strings.Contains(v, "localhost") {
		t.Fatalf("the last access:\n%s", v)
	}
	// The list that cannot be read anew stays as it was.
	s.fail = map[string]error{"accesses": errors.New("no credentials")}
	answer(t, m, press(m, "d"))
	if v := shown(m); !strings.Contains(v, "✗ no credentials") || len(m.onboard.accesses) != 1 {
		t.Fatalf("a list that cannot be read:\n%s", v)
	}
}

// Another access is added with a provider the owner picks, if there are
// several.
func TestSetupAddsAnAccess(t *testing.T) {
	m, s := configured(t)
	press(m, "down", "down", "enter")
	if v := shown(m); m.onboard.step != stepProvider || !strings.Contains(v, "1/4 · Provider") || !strings.Contains(v, "▸ Custom Provider") || !strings.Contains(v, "  ChatGPT") {
		t.Fatalf("providers:\n%s", v)
	}
	press(m, "up", "down", "down", "j", "k", "j")
	if m.onboard.sel != 1 {
		t.Fatalf("selected %d", m.onboard.sel)
	}
	press(m, "enter")
	provider(t, m)
	if !slices.Equal(s.did, []string{"login chatgpt"}) {
		t.Fatalf("did %v", s.did)
	}
	// With one provider there is nothing to pick.
	m, s = configured(t)
	s.providers = s.providers[:1]
	m.onboard.providers = s.providers
	press(m, "down", "down", "enter")
	provider(t, m)
	if !slices.Equal(s.did, []string{"login openai"}) || m.onboard.step != stepLogin {
		t.Fatalf("did %v", s.did)
	}
	// Back from what it asks are the accesses.
	if press(m, "esc"); m.onboard.step != stepAccess {
		t.Fatalf("back on %v", m.onboard.step)
	}
	// Without a provider there is nothing to sign in with.
	m.onboard.providers = nil
	press(m, "down", "down", "enter")
	if v := said(m); m.onboard.step != stepAccess || !strings.Contains(v, "no provider to sign in with") {
		t.Fatalf("without providers:\n%s", v)
	}
	// And a new node without one is told the same on the welcome; a new
	// node with several picks one first, and goes back to the welcome.
	m, _, _ = onboard(t)
	m.onboard.providers = nil
	if press(m, "enter"); m.onboard.step != stepWelcome || !strings.Contains(said(m), "no provider to sign in with") {
		t.Fatalf("a new node without providers:\n%s", shown(m))
	}
	m.onboard.providers = []plugin.SetupProvider{{Name: "openai", Title: "API"}, {Name: "chatgpt", Title: "ChatGPT"}}
	if press(m, "enter"); m.onboard.step != stepProvider {
		t.Fatalf("a new node with two providers: on %v", m.onboard.step)
	}
	if press(m, "esc"); m.onboard.step != stepWelcome {
		t.Fatalf("back on %v", m.onboard.step)
	}
}

// A provider that signs in with the browser has its page opened, and the
// link put on the clipboard, since the browser may be on another machine.
func TestSetupOpensThePageOfTheProvider(t *testing.T) {
	var opened, copied []string
	oldBrowse, oldClip := browse, writeClipboard
	t.Cleanup(func() { browse, writeClipboard = oldBrowse, oldClip })
	browse = func(url string) error { opened = append(opened, url); return errors.New("no browser") }
	writeClipboard = func(text string) error { copied = append(copied, text); return errors.New("no clipboard") }

	m, _, s := onboard(t)
	s.browser = "https://auth.example.com/authorize?x=1"
	press(m, "enter")
	provider(t, m) // told
	if v := shown(m); !strings.Contains(v, "Sign in on the page.") {
		t.Fatalf("what the provider tells:\n%s", v)
	}
	cmd := provider(t, m) // opens the page
	if v := shown(m); !strings.Contains(v, "The page opens in your browser.") || !strings.Contains(v, "clipboard") {
		t.Fatalf("while the page opens:\n%s", v)
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 3 {
		t.Fatalf("opening the page: %T", cmd())
	}
	if msg := batch[2](); msg != nil || !slices.Equal(opened, []string{s.browser}) || !slices.Equal(copied, []string{s.browser}) {
		t.Fatalf("opened %v, copied %v", opened, copied)
	}
	// The question that follows is asked below the note, which goes with
	// the answer.
	provider(t, m)
	if v := shown(m); !strings.Contains(v, "LLM endpoint") || !strings.Contains(v, "The page opens in your browser.") {
		t.Fatalf("the question:\n%s", v)
	}
	press(m, "enter")
	if strings.Contains(shown(m), "The page opens") {
		t.Fatalf("the note stays:\n%s", shown(m))
	}
}

func TestSetupErrors(t *testing.T) {
	m, _, s := onboard(t)
	s.loginErr = errors.New("cannot list models")
	press(m, "enter")
	provider(t, m)
	reply(t, m, "")
	reply(t, m, "") // the provider ends with the error
	if v := shown(m); !strings.Contains(v, "✗ cannot list models") || m.onboard.step != stepLogin || m.onboard.login != nil || !strings.Contains(v, "enter once more") {
		t.Fatalf("an endpoint that cannot be asked:\n%s", v)
	}
	if s.ctx.Err() == nil {
		t.Fatal("what a provider that ended ran with should be given up")
	}
	// Enter tries once more, with what was entered.
	s.loginErr = nil
	press(m, "enter")
	provider(t, m)
	reply(t, m, "")
	reply(t, m, "sk-good")
	press(m, "enter", "enter")
	s.saveErr = errors.New("disk full")
	answer(t, m, press(m, "enter"))
	if !strings.Contains(shown(m), "✗ disk full") || !m.onboard.saved.IsZero() {
		t.Fatalf("a setup that cannot be saved:\n%s", shown(m))
	}

	// Saved, the node is started with it; what fails on the way is said.
	for fail, want := range map[string]string{"stop": "stop failed", "start": "start failed", "": ""} {
		m, b, s := onboard(t)
		s.saved = "sk-good"
		toRights(t, m)
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
		t.Fatal("an answer of an access on the welcome")
	}
	if _, cmd := m.Update(savedMsg{}); cmd != nil || !m.onboard.saved.IsZero() {
		t.Fatal("a saved setup on the welcome")
	}
	if m.Update(accessesMsg{note: "late"}); m.onboard.note != "" {
		t.Fatal("accesses on the welcome")
	}
	// What a provider that was given up still sends.
	press(m, "enter")
	provider(t, m)
	gone := m.onboard.login
	press(m, "esc", "enter")
	if _, cmd := m.Update(loginMsg{from: gone, done: true, models: plugin.SetupModels{Chat: []string{"x"}}}); cmd != nil || m.onboard.step != stepLogin {
		t.Fatal("the end of a provider that was given up")
	}
	press(m, "esc")
	if _, cmd := m.Update(loginMsg{from: gone, tell: "late"}); cmd != nil || m.onboard.note != "" {
		t.Fatal("a line of a provider that was given up")
	}
	m.closeSetup()
	m.Update(connectedMsg{})
	m.Update(savedMsg{})
	m.Update(accessesMsg{})
	m.Update(loginMsg{from: gone})
}

// The dialog of a provider ends with what it was given up by: the owner who
// left the page, or the provider that has what it asked for another way.
func TestSetupDialogIsGivenUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	l := &login{events: make(chan loginMsg)}
	d := dialog{ctx, l}

	// The provider gives the question up.
	asking, done := context.WithCancel(ctx)
	asked := make(chan error)
	go func() {
		_, err := d.Ask(asking, plugin.Question{Title: "Paste"})
		asked <- err
	}()
	if msg := <-l.events; msg.ask == nil || msg.from != l {
		t.Fatalf("got %+v", msg)
	}
	done()
	if err := <-asked; !errors.Is(err, context.Canceled) {
		t.Fatalf("a question the provider gave up: %v", err)
	}
	// The owner leaves while a question waits for its answer.
	go func() {
		_, err := d.Ask(ctx, plugin.Question{Title: "Key"})
		asked <- err
	}()
	<-l.events
	cancel()
	if err := <-asked; !errors.Is(err, context.Canceled) {
		t.Fatalf("a question the owner left: %v", err)
	}
	// And afterwards nothing is shown any more.
	if _, err := d.Ask(context.Background(), plugin.Question{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a question after the owner left: %v", err)
	}
	d.Tell("late")
	d.Reject("late")
	d.Open("late")
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
	// Fast at first: after half the time three quarters of the way are done.
	if got, want := o.curled(start.Add(curlDuration/2)), curlFrom+(o.curl.to-curlFrom)*0.75; !near(got, want) || o.curl.to <= curlFrom {
		t.Fatalf("half the time curls it to %v, want %v", got, want)
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
	toRights(t, m)
	m.Update(press(m, "enter")())
	saved := o.saved
	if saved.IsZero() || strings.Contains(m.View().Content, "Welcome") || strings.Contains(m.View().Content, "╭") {
		t.Fatal("the dialog goes when the setup is saved")
	}
	if p, bright := o.pose(100, 30); p.turn != 0 || bright != setupDim || p.x != 100 {
		t.Fatalf("the snake should not turn yet: %+v", p)
	}
	// A quarter of the way it has begun to turn, to shrink and to rise, slowly.
	o.at = saved.Add(turnAt + turnDuration/4)
	if p, bright := o.pose(100, 30); !near(p.turn, 2*math.Pi*0.15625) || !near(p.scale, 1.3731971153846156) || p.x != 100 || !near(p.y, 69.90985576923076) || !near(bright, 0.578125) {
		t.Fatalf("a quarter of the way: %+v, %v", p, bright)
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
	// At the end the snake is where the splash shows the name, as large as
	// the logo.
	o.at = end
	if p, _ := o.pose(100, 20); math.Abs(p.scale-1) > 1e-9 || p.x != 100 || math.Abs(p.y-38) > 1e-9 {
		t.Fatalf("on a low screen: %+v", p)
	}
	p, bright := o.pose(100, 30)
	_, _, top, block := splashLayout(100, 30)
	if math.Abs(wrap(p.turn+1e-9)) > 1e-6 || math.Abs(p.scale-1) > 1e-9 || p.x != 100 || math.Abs(p.y-float64(top*4+block*2)) > 1e-9 || bright != 1 || o.curled(end) != bodyArc {
		t.Fatalf("got %+v, %v", p, bright)
	}
	if _, cmd := m.Update(splashMsg{end}); cmd == nil || m.onboard != nil || !m.splash.show || m.splash.elapsed != biteAt || !m.splash.start.Equal(end.Add(-biteAt)) {
		t.Fatal("the splash should take over")
	}
}

// A key skips the end of the scene.
func TestSetupSceneIsSkipped(t *testing.T) {
	m, _, s := onboard(t)
	s.saved = "sk-good"
	toRights(t, m)
	m.Update(press(m, "enter")())
	if press(m, "x"); m.onboard != nil || m.splash.show {
		t.Fatal("a key should skip to the chat")
	}
}

func TestSetupCtrlCQuits(t *testing.T) {
	m, _, s := onboard(t)
	press(m, "enter")
	provider(t, m)
	if _, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})); cmd == nil || m.ctx.Err() == nil {
		t.Fatal("ctrl+c should quit")
	}
	if s.ctx.Err() == nil {
		t.Fatal("the provider that was signing in should be given up")
	}
}

// What is not a key goes to the input of the setup.
func TestSetupTakesPastes(t *testing.T) {
	m, _, _ := onboard(t)
	press(m, "enter")
	provider(t, m)
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
		if m.onboard != nil || !m.splash.show || b.setups != 0 {
			t.Fatalf("%+v: the splash should open", b)
		}
		// The frames of the splash are coming: the setup opened now uses them.
		if cmd := m.splash.animate(); cmd != nil {
			t.Fatal("a second line of frames")
		}
	}
}

func TestSetupDialog(t *testing.T) {
	o := &onboarding{setup: newFakeSetup(), step: stepRights}
	f := o.setup.(*fakeSetup)
	f.owner, f.peers, f.protected = []string{"read", "write"}, nil, nil
	v := ansi.Strip(o.dialog("", false))
	if !strings.Contains(v, "For you    read, write") || !strings.Contains(v, "For peers  nothing") || !strings.Contains(v, "Protected  nothing") {
		t.Fatalf("rights:\n%s", v)
	}
	if got := ansi.Strip(o.progress()); got != "● ● ● ◉" {
		t.Fatalf("progress %q", got)
	}
	// The pages of the access are one part of the setup.
	for _, s := range []step{stepAccess, stepProvider, stepLogin} {
		o.step = s
		if got := ansi.Strip(o.progress()); got != "◉ ○ ○ ○" {
			t.Fatalf("progress of %v: %q", s, got)
		}
	}
	o.step = stepWelcome
	if got := ansi.Strip(o.progress()); got != "○ ○ ○ ○" {
		t.Fatalf("progress of the welcome: %q", got)
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
	// A screen of 100 by 30 is too low for more, one of 40 by 30 too narrow.
	up, down := extent()
	if !near(up, 50) || !near(down, 33.2) || !near(scale(100, 30), 120/83.2) || !near(scale(40, 30), 80/(2*(pose{curl: curlFrom}.ring().r+reach))) || scale(40, 30) >= scale(100, 30) {
		t.Fatalf("the snake reaches %v up and %v down; scales %v and %v", up, down, scale(100, 30), scale(40, 30))
	}
	// At the welcome it lies in the middle of the screen, as large as fits.
	o := &onboarding{}
	o.curl.from, o.curl.to = curlFrom, curlFrom
	if p, _ := o.pose(100, 30); !near(p.scale, 120/83.2) || p.x != 100 || !near(p.y, 60+(50-33.2)*120/83.2/2) {
		t.Fatalf("at the welcome: %+v", p)
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

// The snake curls up by the part of the setup a page belongs to: not at all
// at the welcome, all the way at the last page, and evenly in between.
func TestSetupCurlsByPhase(t *testing.T) {
	m, _, _ := onboard(t)
	o := m.onboard
	for s, part := range map[step]float64{stepWelcome: 0, stepAccess: 1, stepProvider: 1, stepLogin: 1, stepModel: 2, stepName: 3, stepRights: 4} {
		o.goTo(s)
		want := curlFrom + (curlLast-curlFrom)*part/4
		if math.Abs(o.curl.to-want) > 1e-9 {
			t.Errorf("page %v curls to %v, want %v", s, o.curl.to, want)
		}
	}
	if o.goTo(stepRights); math.Abs(o.curl.to-curlLast) > 1e-9 {
		t.Fatalf("the last page curls to %v", o.curl.to)
	}
}

// Keys are typed only where something is asked: not while the provider
// works, and not once it gave up.
func TestSetupTypesOnlyWhereItIsAsked(t *testing.T) {
	m, _, s := onboard(t)
	press(m, "enter")
	if press(m, "x", "y"); m.onboard.input.Value() != "" {
		t.Fatalf("typed %q while the provider works", m.onboard.input.Value())
	}
	provider(t, m)
	typeIn(m, "")
	if press(m, "x"); m.onboard.input.Value() != "x" {
		t.Fatalf("typed %q into a question", m.onboard.input.Value())
	}
	s.loginErr = errors.New("boom")
	typeIn(m, "")
	press(m, "enter")
	provider(t, m)
	reply(t, m, "")
	if m.onboard.login != nil || m.onboard.step != stepLogin {
		t.Fatalf("the provider should have given up: on %v", m.onboard.step)
	}
	before := m.onboard.input.Value()
	if press(m, "z"); m.onboard.input.Value() != before {
		t.Fatalf("typed %q after the provider gave up", m.onboard.input.Value())
	}
}

// A row that is too long for the dialog is cut, not wrapped: the dialog is
// as high as with a short one.
func TestSetupCutsLongRows(t *testing.T) {
	m, s := configured(t)
	short := strings.Count(ansi.Strip(m.onboard.dialog("", false)), "\n")
	s.accesses[0].About = strings.Repeat("a", 80)
	m.onboard.accesses = s.accesses
	box := ansi.Strip(m.onboard.dialog("", false))
	if strings.Count(box, "\n") != short {
		t.Fatalf("a long row wrapped:\n%s", box)
	}
	inner := boxWidth - 6
	for _, line := range strings.Split(box, "\n") {
		if strings.Contains(line, "▸ localhost") {
			row := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "│"))
			if ansi.StringWidth(row) != inner || !strings.HasSuffix(row, "…") {
				t.Fatalf("the row is %d cells wide, want %d: %q", ansi.StringWidth(row), inner, row)
			}
			return
		}
	}
	t.Fatalf("no row of the access in:\n%s", box)
}

// near reports whether two numbers of the scene are the same, but for what
// is lost in computing them.
func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// boxAt is where the dialog of the setup lies in the view of m: the row and
// the column of its top left corner, and how many rows it has.
func boxAt(m *Model) (row, col, rows int) {
	for i, l := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if at := strings.Index(l, "╭"); at >= 0 {
			row, col = i, len([]rune(l[:at]))
		}
		if strings.Contains(l, "╰") {
			rows = i - row + 1
		}
	}
	return row, col, rows
}

// The dialog of the setup lies in the middle of the screen, around the ring
// of the snake, which shows behind it; on a screen as low as the dialog it
// starts at the top.
func TestSetupDialogIsPlaced(t *testing.T) {
	m, _, _ := onboard(t)
	if m.onboard.input.Width() != boxWidth-8 {
		t.Fatalf("the input is %d wide", m.onboard.input.Width())
	}
	if row, col, rows := boxAt(m); row != 11 || col != (100-boxWidth)/2 || rows != 14 {
		t.Fatalf("the dialog is at row %d, column %d, with %d rows", row, col, rows)
	}
	snake := 0
	for _, r := range ansi.Strip(m.View().Content) {
		if r > 0x2800 && r <= 0x28ff {
			snake++
		}
	}
	if snake < 100 {
		t.Fatalf("%d cells of the snake show", snake)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 14})
	if row, col, rows := boxAt(m); row != 0 || col != (100-boxWidth)/2 || rows != 14 {
		t.Fatalf("on a low screen the dialog is at row %d, column %d, with %d rows", row, col, rows)
	}
}

// What a line of the dialog has no room for is cut: the dialog keeps its
// width and its rows.
func TestSetupDialogCutsLongLines(t *testing.T) {
	o := &onboarding{setup: newFakeSetup(), step: stepRights}
	f := o.setup.(*fakeSetup)
	rows := lipgloss.Height(o.dialog("", false))
	var many []string
	for i := range 20 {
		many = append(many, fmt.Sprintf("tool%d", i))
	}
	f.owner, f.peers, f.protected = many, many, many
	v := ansi.Strip(o.dialog("", false))
	if lipgloss.Height(v) != rows || lipgloss.Width(v) != boxWidth || strings.Count(v, "… ") != 3 {
		t.Fatalf("%d rows, %d cells, want %d and %d:\n%s", lipgloss.Height(v), lipgloss.Width(v), rows, boxWidth, v)
	}
	o.models.Chat = []string{strings.Repeat("m", 40), "short"}
	for _, line := range o.modelList(30) {
		if lipgloss.Width(line) > 30 {
			t.Errorf("a model on a line of %d cells: %q", lipgloss.Width(line), line)
		}
	}
	if first := ansi.Strip(o.modelList(30)[0]); first != "▸ "+strings.Repeat("m", 27)+"…" {
		t.Errorf("the long name: %q", first)
	}
}

// The scene of the setup, as it is drawn: the sky drifts and twinkles behind
// the snake, whose colors flow along its body, and the dialog lies in front
// of them.
func TestGoldenSetupScene(t *testing.T) {
	m, _, _ := onboard(t)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 26})
	o := m.onboard
	var b strings.Builder
	for _, after := range []time.Duration{700 * time.Millisecond, 2300 * time.Millisecond} {
		m.Update(splashMsg{o.opened.Add(after)})
		fmt.Fprintf(&b, "--- %v\n%s\n", after, m.View().Content)
	}
	golden(t, "setup-scene", b.String())
}
