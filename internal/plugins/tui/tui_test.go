package tui_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/credentials"
	"ouroboros/internal/history"
	"ouroboros/internal/home"
	"ouroboros/internal/openai"
	tuiplugin "ouroboros/internal/plugins/tui"
	"ouroboros/internal/session"
	"ouroboros/internal/tui"
	"ouroboros/plugin"
	"ouroboros/plugin/example"
)

func TestMain(m *testing.M) { clitest.Main(m) }

// spy is a plugin that keeps the context to read the contributions with.
func spy(ctx **plugin.Context) plugin.Plugin {
	return plugin.New("spy", func(c *plugin.Context, _ struct{}) error {
		*ctx = c
		return nil
	})
}

// backend is what the terminal UI of the node of s controls, with sessions
// in the process of the test: their answers come from the assistant of the
// app.
func backend(t *testing.T, s cli.Settings, extra ...plugin.Plugin) tui.Backend {
	t.Helper()
	var ctx *plugin.Context
	app, _ := clitest.NewApp(t, s, "", append(extra, spy(&ctx))...)
	bot, _ := cli.Service[*plugin.Assistant](app)
	sessions := session.New(bot)
	sessions.Archive(history.Open(home.Dir(s.Home).History()), func(string, ...any) {}, func(string) string { return "beta" })
	t.Cleanup(sessions.Close)
	return tuiplugin.Backend(plugin.OwnerOf(home.Dir(s.Home), clitest.Network(t, s, app)), ctx, sessions)
}

// answer asks u text in the session id and returns what happened up to the
// end of the answer, as the UI is told.
func answer(t *testing.T, u tui.Backend, id, text string) []tui.Event {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	_, live, err := u.Watch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Ask(ctx, id, text); err != nil {
		t.Fatal(err)
	}
	var events []tui.Event
	for e := range live {
		events = append(events, e)
		if e.Kind == plugin.SessionAnswer || e.Kind == plugin.SessionFailed || e.Kind == plugin.SessionCanceled {
			return events
		}
	}
	t.Fatalf("the events ended before the answer: %+v", events)
	return nil
}

// The UI sets the node up with what the owner does: the setup offers the
// providers the plugins contributed, and one whose configuration cannot be
// read is none.
func TestTUIBackendSetup(t *testing.T) {
	s := clitest.Settings(t)
	set, err := backend(t, s).Setup()
	if err != nil {
		t.Fatalf("got %v, %v", set, err)
	}
	var names []string
	for _, p := range set.Providers() {
		names = append(names, p.Name)
	}
	if !slices.Equal(names, []string{"openai", "chatgpt", "opencode-go"}) {
		t.Fatalf("the providers to sign in with: %v", names)
	}
	if err := os.WriteFile(filepath.Join(s.Home, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if set, err := backend(t, s).Setup(); err == nil || set != nil {
		t.Fatalf("got %v, %v", set, err)
	}
}

func TestTuiWithoutTerminal(t *testing.T) {
	if _, err := clitest.Run(t, clitest.Settings(t), "", "tui"); err == nil {
		t.Fatal("want error")
	}
}

// The UI on the terminal ends with its context; one whose context is given
// up already is not started.
func TestShowEndsWithItsContext(t *testing.T) {
	m := tui.New(backend(t, clitest.Settings(t)), nil, nil)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if err := tuiplugin.Show(gone, m); !errors.Is(err, context.Canceled) {
		t.Fatalf("a context given up: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := tuiplugin.Show(ctx, m); err == nil {
		t.Fatal("a context that ends: want error")
	}
}

// Of a tool call the UI is told what the plugin of the tool wants shown: for
// ask_peer, which peer the agent is waiting for.
func TestTUIBackendAsk(t *testing.T) {
	alpha, _ := clitest.TwoNodes(t)
	u := backend(t, alpha)
	// The pieces of the answer the model writes come before it, and make it
	// up; the rounds are told apart without them.
	var events []tui.Event
	var pieces strings.Builder
	var calling []string
	for _, e := range answer(t, u, "ctx", "@beta hello") {
		switch e.Kind {
		case plugin.SessionAnswering:
			pieces.WriteString(e.Text)
			continue
		case plugin.SessionCalling:
			calling = append(calling, e.Text)
			continue
		}
		events = append(events, e)
	}
	// While the model writes the call of ask_peer, the UI is told how much
	// of it there is.
	if len(calling) == 0 || !strings.HasSuffix(calling[0], " bytes") {
		t.Fatalf("calling %q", calling)
	}
	for n, want := range map[int]string{3277: "3.2 KiB", 1024: "1.0 KiB", 1023: "1023 bytes"} {
		if got := tuiplugin.Size(n); got != want {
			t.Fatalf("a call of %d bytes: %q", n, got)
		}
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, string(e.Kind))
	}
	// The round that asks beta is told of although the agent said nothing
	// along with it: that is how the UI tells the rounds apart.
	if strings.Join(kinds, " ") != "message say step step answer" || events[0].Text != "@beta hello" || events[1].Text != "" {
		t.Fatalf("events %+v", events)
	}
	// The direct call of beta is shown while waiting and when it ends.
	if events[2].Text != "asking beta" || events[2].Done ||
		!events[3].Done || !strings.HasPrefix(events[3].Text, "asking beta ✓ ") {
		t.Fatalf("steps %+v", events[2:4])
	}
	if reply := events[4]; !strings.Contains(reply.Text, "not connected") || reply.Context == 0 {
		t.Fatalf("answer %+v", reply)
	}
	if pieces.String() != events[4].Text {
		t.Fatalf("pieces %q of the answer %q", pieces.String(), events[4].Text)
	}
	// The context of the conversation is what the LLM is sent next, message
	// by message: the call of a tool with its arguments.
	msgs, err := u.Contents(context.Background(), "ctx")
	var roles []string
	var peerTool bool
	for _, m := range msgs {
		if strings.HasPrefix(m.Role, "tool ") {
			if m.Role == "tool ask_peer" && strings.Contains(m.Text, "\nparameters: {") {
				peerTool = true
			}
			continue
		}
		roles = append(roles, m.Role)
	}
	if err != nil || !peerTool || strings.Join(roles, " ") != "system user assistant tool" ||
		!slices.ContainsFunc(msgs, func(m tui.Message) bool { return m.Role == "assistant" && strings.HasPrefix(m.Text, "→ ask_peer {") }) {
		t.Fatalf("contents %q, %v", msgs, err)
	}
	// The sessions are listed, and one is stopped as asked; there is
	// nothing on its way to stop here.
	if list, err := u.Sessions(context.Background()); err != nil || len(list) != 1 || list[0].ID != "ctx" || list[0].Title != "@beta hello" {
		t.Fatalf("sessions %+v, %v", list, err)
	}
	if err := u.Cancel(context.Background(), "ctx"); err != nil {
		t.Fatal(err)
	}
	// What the model takes is not known here: models.dev is not reached.
	if efforts, known := u.Efforts(); known || efforts != nil {
		t.Fatalf("efforts %q, %v", efforts, known)
	}
	// The session gets an effort of its own, which the node tells.
	if err := u.SetSessionEffort(context.Background(), "ctx", "high"); err != nil {
		t.Fatal(err)
	}
	if past, _, err := u.Watch(context.Background(), "ctx"); err != nil || past[len(past)-1].Kind != plugin.SessionEffort || past[len(past)-1].Text != "high" {
		t.Fatalf("effort %+v, %v", past, err)
	}
	// So is what the owner says while an answer is on its way: nothing
	// waits here, and nothing is said.
	if text, err := u.Unsteer(context.Background(), "ctx"); text != "" || err != nil {
		t.Fatalf("unsteer %q, %v", text, err)
	}
	if err := u.Steer(context.Background(), "ctx", " "); !errors.Is(err, session.ErrEmpty) {
		t.Fatalf("steer: %v", err)
	}
	// Looking up the peers is not worth a line in the chat: no plugin says
	// what to show of it.
	for _, e := range answer(t, u, "ctx", "@") {
		if e.Kind == plugin.SessionStep && e.Text != "" {
			t.Fatalf("events %+v", e)
		}
	}
	// No peer talked with the agent yet; what is not a conversation of a
	// peer is not read.
	if list, err := u.Peers(context.Background()); err != nil || len(list) != 0 {
		t.Fatalf("peers %+v, %v", list, err)
	}
	if _, err := u.Read(context.Background(), "ctx"); !errors.Is(err, session.ErrNoSession) {
		t.Fatalf("read: %v", err)
	}
	history.Open(home.Dir(alpha.Home).History()).Append(history.Who{Session: "p", Peer: "key"},
		[]openai.Message{{Role: "user", Content: "hello"}, {Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "1", Function: openai.FunctionCall{Name: "history_search", Arguments: "{}"}}}}}, nil)
	list, _ := u.Peers(context.Background())
	if events, err := u.Read(context.Background(), list[0].ID); err != nil || len(events) != 3 || events[0].Text != "hello" || events[2].Text != "searching the history" {
		t.Fatalf("read %+v, %v", events, err)
	}
	// No command runs in the background, so none is stopped.
	if jobs, err := u.Jobs(context.Background()); err != nil || len(jobs) != 0 {
		t.Fatalf("jobs %+v, %v", jobs, err)
	}
	if err := u.StopJob(context.Background(), "ctx", 1); !errors.Is(err, session.ErrNoJob) {
		t.Fatalf("stop: %v", err)
	}
	// A session is deleted.
	if err := u.Delete(context.Background(), "ctx"); err != nil {
		t.Fatal(err)
	}
	if list, err := u.Sessions(context.Background()); err != nil || len(list) != 0 {
		t.Fatalf("sessions %+v, %v", list, err)
	}
}

// A call whose plugin shows nothing of it stays unshown when it ends, though
// the plugin has something to add to its line.
func TestTUIBackendLeavesUnshownCallsUnshown(t *testing.T) {
	alpha, _ := clitest.TwoNodes(t)
	quiet := plugin.New("quiet", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.ToolView{Tool: "ask_peer", Show: func(json.RawMessage) string { return "" }, Detail: func(plugin.Step) string { return "ran" }})
		return nil
	})
	for _, e := range answer(t, backend(t, alpha, quiet), "ctx", "@beta hello") {
		if e.Kind == plugin.SessionStep && strings.Contains(e.Text, "ran") {
			t.Fatalf("event %+v", e)
		}
	}
}

// What the UI watches ends with ctx; a session that cannot be watched says
// why.
func TestTUIBackendWatch(t *testing.T) {
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	u := backend(t, s)
	if _, _, err := u.Watch(context.Background(), ""); !errors.Is(err, session.ErrNoSession) {
		t.Fatalf("no session: %v", err)
	}
	answer(t, u, "c1", "hi")
	ctx, cancel := context.WithCancel(context.Background())
	past, live, err := u.Watch(ctx, "c1")
	if err != nil || len(past) != 2 || past[1].Text != "[gpt-4o] turns=1 last=hi" {
		t.Fatalf("past %+v, %v", past, err)
	}
	cancel()
	for range live {
	}
	// Events that come while nobody takes them are dropped once the
	// watcher gave up.
	ctx, cancel = context.WithCancel(context.Background())
	_, live, _ = u.Watch(ctx, "c1")
	if err := u.Ask(context.Background(), "c1", "again"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	for range live {
	}
}

// The command opens the UI, which gives up without a terminal to draw on, and
// stops the node it found stopped.
func TestTuiCommand(t *testing.T) {
	s := clitest.Settings(t)
	app, _ := clitest.NewAppOn(t, s, cli.Terminal{Interactive: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := app.Run(ctx, []string{"tui"}); err == nil {
		t.Fatal("want error")
	}
	// A node that ran before is left running: here one that cannot even be stopped.
	clitest.Ghost(t, s)
	if err := app.Run(ctx, nil); err == nil || strings.Contains(err.Error(), "no such process") {
		t.Fatalf("got %v", err)
	}
}

// A node that is not set up, or does not run, tells the user of the UI how to
// get there.
func TestTUIBackendWithoutSetup(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	u := backend(t, s)
	if err := u.Start(); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "type /setup") {
		t.Fatalf("start: %v", err)
	}
	if err := u.Ask(context.Background(), "ctx", "hi"); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "type /setup") {
		t.Fatalf("ask: %v", err)
	}
	// The sessions of the node, over its socket, which needs the node.
	app, _ := clitest.NewApp(t, s, "")
	client, _ := cli.Service[plugin.Sessions](app)
	remote := tuiplugin.Backend(nil, nil, client)
	if err := remote.Ask(context.Background(), "ctx", "hi"); !errors.Is(err, plugin.ErrNotRunning) || !strings.Contains(err.Error(), "type /start") {
		t.Fatalf("ask without a node: %v", err)
	}

	// Once it is set up, the UI starts it.
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { u.Stop() })
	if err := u.Start(); err != nil {
		t.Fatal(err)
	}
	if status, err := u.Status(); err != nil || !status.Running {
		t.Fatalf("status %+v, %v", status, err)
	}
	if err := remote.Ask(context.Background(), "ctx", "hi"); err != nil {
		t.Fatalf("ask the node: %v", err)
	}
}

// The model screen of the UI lists the models of the accesses of the node,
// and saves the model the owner picked: without the rest of the
// configuration changing.
func TestTUIBackendModels(t *testing.T) {
	s := clitest.Settings(t)
	u := backend(t, s)
	if _, err := u.Models(context.Background()); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "type /setup") {
		t.Fatalf("a node that is not set up: %v", err)
	}
	// An access without a node that is set up, as after a setup that was
	// not finished.
	stray, _ := home.Dir(s.Home).Credentials().Put(credentials.Access{Provider: "openai"})
	if _, err := u.SetModel(stray.ID, "gpt-4o-mini"); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "type /setup") {
		t.Fatalf("a node that is not set up: %v", err)
	}
	if err := u.SetPeerModel(stray.ID, "gpt-4o-mini"); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "type /setup") {
		t.Fatalf("a node that is not set up: %v", err)
	}
	home.Dir(s.Home).Credentials().Remove(stray.ID)
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	models, err := u.Models(context.Background())
	if err != nil || len(models) != 1 || !slices.Contains(models[0].Models, "gpt-4o") || !models[0].Access.Active {
		t.Fatalf("models %+v, %v", models, err)
	}
	if switched, err := u.SetModel(models[0].Access.ID, "gpt-4o-mini"); err != nil || switched {
		t.Fatalf("SetModel = %v, %v", switched, err)
	}
	if endpoint, _ := clitest.Reached(t, s); clitest.MustLoad(t, s).Model != "gpt-4o-mini" || endpoint != s.BaseURL {
		t.Fatalf("the configuration after saving: %+v", clitest.MustLoad(t, s))
	}
	if err := u.SetPeerModel(models[0].Access.ID, "gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if status, _ := u.Status(); status.PeerAccess != models[0].Access.ID || status.PeerModel != "gpt-4o" || status.Model != "gpt-4o-mini" {
		t.Fatalf("the status after choosing for peers: %+v", status)
	}
}

// The UI finds the sessions by what was said in them, lists the services
// that run past the answers, and stops one: here none runs.
func TestTUIBackendFindAndServices(t *testing.T) {
	s := clitest.Settings(t)
	u := backend(t, s)
	history.Open(home.Dir(s.Home).History()).Append(history.Who{Session: "c1"}, []openai.Message{{Role: "user", Content: "the dark mode"}}, nil)
	if found, err := u.Find(context.Background(), "dark"); err != nil || !slices.Equal(found, []string{"c1"}) {
		t.Fatalf("found %q, %v", found, err)
	}
	if found, err := u.Find(context.Background(), "light"); err != nil || len(found) != 0 {
		t.Fatalf("found %q, %v", found, err)
	}
	if list, err := u.Services(context.Background()); err != nil || len(list) != 0 {
		t.Fatalf("services %+v, %v", list, err)
	}
	if err := u.StopService(context.Background(), "s1"); !errors.Is(err, session.ErrNoService) {
		t.Fatalf("stop: %v", err)
	}
}

// The gateways are read from what the plugins contributed when the UI asks,
// each under the name and title it was contributed with: one without
// settings is left out, and one whose settings cannot be read fails the
// list. A change goes to the gateway of its name, if that takes changes.
func TestTUIBackendGateways(t *testing.T) {
	var ctx *plugin.Context
	var saved []string
	broken := errors.New("the store cannot be read")
	fail := false
	gateways := plugin.New("gateways", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		plugin.Add(c, plugin.Gateway{Name: "echo", Title: "Echo",
			Settings: func(context.Context) (plugin.GatewaySettings, error) {
				return plugin.GatewaySettings{Name: "other", Title: "Other", Enabled: true, Status: "Ready"}, nil
			},
			Save: func(_ context.Context, change plugin.GatewayChange) error {
				saved = append(saved, change.UserID)
				return nil
			}})
		plugin.Add(c, plugin.Gateway{Name: "mute", Title: "Mute"})
		plugin.Add(c, plugin.Gateway{Name: "flaky", Settings: func(context.Context) (plugin.GatewaySettings, error) {
			if fail {
				return plugin.GatewaySettings{}, broken
			}
			return plugin.GatewaySettings{Status: "Synchronizing"}, nil
		}})
		return nil
	})
	if _, err := cli.NewWith(gateways); err != nil {
		t.Fatal(err)
	}
	u := tuiplugin.Backend(nil, ctx, nil)
	list, err := u.Gateways(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("gateways %+v, %v", list, err)
	}
	if g := list[0]; g.Name != "echo" || g.Title != "Echo" || !g.Enabled || g.Status != "Ready" {
		t.Errorf("echo: %+v", g)
	}
	if g := list[1]; g.Name != "flaky" || g.Title != "" || g.Status != "Synchronizing" {
		t.Errorf("flaky: %+v", g)
	}
	fail = true
	if list, err := u.Gateways(context.Background()); !errors.Is(err, broken) || list != nil {
		t.Fatalf("unreadable: %+v, %v", list, err)
	}
	if err := u.SaveGateway(context.Background(), "echo", plugin.GatewayChange{UserID: "@luna:example.org"}); err != nil || !slices.Equal(saved, []string{"@luna:example.org"}) {
		t.Fatalf("saved %q, %v", saved, err)
	}
	for _, name := range []string{"mute", "flaky", "none"} {
		if err := u.SaveGateway(context.Background(), name, plugin.GatewayChange{}); err == nil || err.Error() != `gateway "`+name+`" is not available` || len(saved) != 1 {
			t.Errorf("%s: %v, saved %q", name, err, saved)
		}
	}
}

// The screen of the remaining usage asks the providers of the accesses of
// the node, which has none here; a node whose configuration cannot be read
// says why.
func TestUsageScreen(t *testing.T) {
	s := clitest.Settings(t)
	var ctx *plugin.Context
	clitest.NewApp(t, s, "", spy(&ctx))
	var usage *plugin.Screen
	for _, screen := range plugin.All[plugin.Screen](ctx) {
		if screen.Command == "usage" {
			usage = &screen
		}
	}
	if usage == nil || usage.Title != "Remaining usage" {
		t.Fatalf("the screen: %+v", usage)
	}
	if body, err := usage.Body(context.Background()); err != nil || body != "No configured provider exposes remaining quota through its API." {
		t.Fatalf("body %q, %v", body, err)
	}
	if err := os.WriteFile(filepath.Join(s.Home, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if body, err := usage.Body(context.Background()); err == nil || body != "" {
		t.Fatalf("an unreadable configuration: %q, %v", body, err)
	}
}

// The settings screen sees the tools the plugins contributed, and changes
// the configuration through the owner.
func TestTUIBackendSettings(t *testing.T) {
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	u := backend(t, s)
	settings, err := u.Settings()
	if err != nil || settings.Name != "alpha" || !slices.ContainsFunc(settings.Tools[plugin.ForPeers].Tools, func(t plugin.ToolState) bool { return t.Name == "read" }) {
		t.Fatalf("settings %+v, %v", settings, err)
	}
	holds, err := u.ChangeSetting(plugin.Setting{Kind: plugin.SetTool, For: plugin.ForPeers, Name: "list_peers", On: true})
	if err != nil || !strings.Contains(holds, "from the next message") {
		t.Fatalf("change: %q, %v", holds, err)
	}
	if cfg := clitest.MustLoad(t, s); len(cfg.PeerTools()) != 1 || cfg.PeerTools()[0].Tool != "list_peers" {
		t.Fatalf("the configuration after the change: %+v", cfg.Tools)
	}
}

// The UI shows the connection requests in both directions.
func TestTUIBackendContacts(t *testing.T) {
	s := clitest.Settings(t)
	u, store := backend(t, s), home.Dir(s.Home).Contacts()
	store.AddIncoming("pk-in", "in", "", "")
	store.AddOutgoing("pk-out", "out", "", "")
	got, err := u.Contacts()
	if err != nil || len(got.Incoming) != 1 || got.Incoming[0].Name != "in" || len(got.Outgoing) != 1 || got.Outgoing[0].Name != "out" || len(got.Proposals) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}

	// With them it shows what the agent proposed for the configuration.
	cfg := clitest.NodeConfig(s)
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := home.Dir(s.Home).Proposals().Add(cfg, "name", []byte(`"omega"`), "a better name"); err != nil {
		t.Fatal(err)
	}
	got, err = u.Contacts()
	if err != nil || len(got.Incoming) != 1 || len(got.Proposals) != 1 || got.Proposals[0].Field != "name" || got.Proposals[0].Then != `"omega"` {
		t.Fatalf("got %+v, %v", got, err)
	}
	// What cannot be read is an error, not an empty list.
	os.WriteFile(filepath.Join(s.Home, "proposals.json"), []byte("nope"), 0o600)
	if got, err := u.Contacts(); err == nil || len(got.Incoming) != 0 {
		t.Fatalf("unreadable proposals: %+v, %v", got, err)
	}
	os.Remove(filepath.Join(s.Home, "proposals.json"))
	// And with them what the reviews suggested.
	if _, err := home.Dir(s.Home).Suggestions().Add("add grep", nil, "s1"); err != nil {
		t.Fatal(err)
	}
	if got, err := u.Contacts(); err != nil || len(got.Suggestions) != 1 || got.Suggestions[0].Text != "add grep" {
		t.Fatalf("got %+v, %v", got, err)
	}
	os.WriteFile(filepath.Join(s.Home, "suggestions.json"), []byte("nope"), 0o600)
	if got, err := u.Contacts(); err == nil || len(got.Incoming) != 0 {
		t.Fatalf("unreadable suggestions: %+v, %v", got, err)
	}
	os.Remove(filepath.Join(s.Home, "suggestions.json"))
	os.WriteFile(filepath.Join(s.Home, "contacts.json"), []byte("nope"), 0o600)
	if got, err := u.Contacts(); err == nil || len(got.Proposals) != 0 {
		t.Fatalf("unreadable contacts: %+v, %v", got, err)
	}
}

// key is the press of the key with the given letter.
func key(letter rune) tea.Msg { return tea.KeyPressMsg{Code: letter, Text: string(letter)} }

// enter is the press of enter.
var enter = tea.KeyPressMsg{Code: tea.KeyEnter}

// command types the command line into m and sends it.
func command(m *tui.Model, line string) tea.Cmd {
	for _, letter := range line {
		m.Update(key(letter))
	}
	_, cmd := m.Update(enter)
	return cmd
}

// The UI is opened with what the plugins contributed to it, through the
// public API alone: here the screen of the example plugin, and a plugin that
// claims a command of the UI.
func TestUIShowsWhatPluginsContribute(t *testing.T) {
	greedy := plugin.New("greedy", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.Slash{Name: "help", Help: "stats", Run: func(context.Context) (string, error) { return "", nil }})
		return nil
	})
	s := clitest.Settings(t)
	app, _ := clitest.NewAppOn(t, s, cli.Terminal{Interactive: true}, example.Plugin(), greedy)

	opened := 0
	tuiplugin.Use(t, func(m *tui.Model) error {
		opened++
		m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
		m.Update(key(' ')) // skips the splash
		view := ansi.Strip(m.View().Content)
		if want := `/help is taken by the terminal UI: "stats" is left out`; !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
		load := command(m, "/greetings")
		if load == nil {
			t.Fatal("/greetings should open the screen of the example plugin")
		}
		m.Update(load())
		view = ansi.Strip(m.View().Content)
		for _, want := range []string{"Greetings", "No connected peers to greet.", "[greet all]", "enter run · esc back"} {
			if !strings.Contains(view, want) {
				t.Errorf("view lacks %q:\n%s", want, view)
			}
		}
		_, run := m.Update(enter)
		if m.Update(run()); !strings.Contains(ansi.Strip(m.View().Content), "greeted 0 peers") {
			t.Errorf("the action of the screen should say what it did:\n%s", ansi.Strip(m.View().Content))
		}
		return nil
	})
	for _, args := range [][]string{nil, {"tui"}, {"chat"}} {
		if err := app.Run(context.Background(), args); err != nil {
			t.Fatalf("ouro %v: %v", args, err)
		}
	}
	if opened != 3 {
		t.Fatalf("the UI was opened %d times", opened)
	}
}

// drivers run the commands of the UIs of the tests, by UI.
var drivers sync.Map

// drive runs cmd and what follows of it as the program does, until done
// reports true. A command that does not come back at once, like the wait for
// what happens next in a session, goes on waiting: what it brings is handed
// to the UI in a later drive.
func drive(t *testing.T, m *tui.Model, cmd tea.Cmd, done func() bool) {
	t.Helper()
	v, _ := drivers.LoadOrStore(m, make(chan tea.Msg, 1024))
	msgs := v.(chan tea.Msg)
	start := func(c tea.Cmd) {
		if c != nil {
			go func() { msgs <- c() }()
		}
	}
	start(cmd)
	deadline := time.After(clitest.Patience)
	for !done() {
		select {
		case msg := <-msgs:
			if batch, ok := msg.(tea.BatchMsg); ok {
				for _, c := range batch {
					start(c)
				}
				continue
			}
			_, c := m.Update(msg)
			start(c)
		case <-deadline:
			t.Fatalf("the UI did not get there:\n%s", ansi.Strip(m.View().Content))
		}
	}
}

// typed types text into m and sends it.
func typed(m *tui.Model, text string) tea.Cmd {
	for _, r := range text {
		m.Update(key(r))
	}
	_, cmd := m.Update(enter)
	return cmd
}

// waiting is a plugin with a tool that waits until it is let go: started is
// closed when it is called.
func waiting(started, letGo chan struct{}) plugin.Plugin {
	return plugin.New("wait", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.NewTool("wait", "waits until it is let go", nil, func(ctx context.Context, _ struct{}) (string, error) {
			close(started)
			select {
			case <-letGo:
				return "let go", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}))
		return nil
	})
}

// An answer still on its way when the owner quits the UI goes on in the
// node: it ends there, and is in the log and in the history. A node the UI
// would have stopped stops once the answer is given.
func TestQuitLeavesTheAnswerToTheNode(t *testing.T) {
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	started, letGo := make(chan struct{}), make(chan struct{})
	n := clitest.Serve(t, s, waiting(started, letGo))
	app, _ := clitest.NewAppOn(t, s, cli.Terminal{Interactive: true})
	sessions, _ := cli.Service[plugin.Sessions](app)
	// The UI of a process that started the node.
	t.Setenv(tuiplugin.ResumeEnv, `{"session":"c1","was_running":false}`)
	tuiplugin.Use(t, func(m *tui.Model) error {
		if m.Current() != "c1" {
			t.Errorf("the UI shows %s", m.Current())
		}
		if err := sessions.Ask(context.Background(), "c1", "!wait"); err != nil {
			t.Error(err)
		}
		<-started
		return nil // the owner quits
	})
	if err := app.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if list, err := sessions.List(context.Background()); err != nil || len(list) != 1 || !list[0].Running {
		t.Fatalf("the answer should go on: %+v, %v", list, err)
	}
	close(letGo)
	if err := n.Wait(); err != nil {
		t.Fatalf("the node should stop once the answer is given: %v", err)
	}
	logged, _ := os.ReadFile(home.Dir(s.Home).Service().LogFile)
	if !strings.Contains(string(logged), "chat c1: tool wait {} took ") || !strings.Contains(string(logged), "chat c1: answered after ") {
		t.Fatalf("log:\n%s", logged)
	}
	if archived, err := os.ReadDir(home.Dir(s.Home).History()); err != nil || len(archived) == 0 {
		t.Fatalf("the history: %v, %v", archived, err)
	}
}

// again is a plugin with a tool that asks for ouro to start anew from exe.
func again(exe string) plugin.Plugin {
	return plugin.New("again", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.NewTool("again", "starts ouro anew", nil, func(ctx context.Context, _ struct{}) (string, error) {
			if !plugin.Restart(ctx, exe) {
				return "", errors.New("nobody starts ouro anew")
			}
			return "starting anew", nil
		}))
		return nil
	})
}

// When a tool asks for ouro to start anew, the node hands its sessions over
// to the new build, and the UI quits once the answer is in: ouro ends with
// plugin.Restarting. The UI of the new process shows the same session, and
// the agent of the new node remembers it. A node that ran before stays
// running.
func TestRestartGoesOnWithTheConversation(t *testing.T) {
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	old := clitest.Serve(t, s, again("/new/ouro"))
	app, _ := clitest.NewAppOn(t, s, cli.Terminal{Interactive: true})

	var shown string
	tuiplugin.Use(t, func(m *tui.Model) error {
		m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
		m.Update(key(' ')) // skips the splash
		drive(t, m, m.Init(), func() bool { return strings.Contains(ansi.Strip(m.View().Content), "gpt-") })
		drive(t, m, typed(m, "/new"), func() bool { return m.Current() != plugin.ThreadID }) // the thread hands on, and starts nothing anew
		drive(t, m, typed(m, "!again"), func() bool { _, ok := m.Restarts(); return ok })
		shown = m.Current()
		return nil
	})
	err := app.Run(context.Background(), nil)
	r, ok := errors.AsType[*plugin.Restarting](err)
	if !ok || r.Exe != "/new/ouro" || err.Error() != "ouro is to be started anew from /new/ouro" {
		t.Fatalf("got %v", err)
	}
	resume := `{"session":"` + shown + `","was_running":true}`
	if !slices.Contains(r.Env, tuiplugin.ResumeEnv+"="+resume) {
		t.Fatalf("the UI hands over %v", r.Env)
	}

	// The node is replaced by the new build: here a node in the same
	// process, with what the old one handed over.
	nr, ok := errors.AsType[*plugin.Restarting](old.Wait())
	if !ok || nr.Exe != "/new/ouro" {
		t.Fatalf("the node ended with %v", old.Wait())
	}
	for _, kv := range nr.Env {
		if k, v, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "OURO_") {
			t.Setenv(k, v)
		}
	}
	replaced := clitest.Serve(t, s)
	t.Setenv(tuiplugin.ResumeEnv, resume)
	app, _ = clitest.NewAppOn(t, s, cli.Terminal{Interactive: true})
	tuiplugin.Use(t, func(m *tui.Model) error {
		if m.Current() != shown {
			t.Errorf("the UI shows %s, not %s", m.Current(), shown)
		}
		m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
		drive(t, m, m.Init(), func() bool { return strings.Contains(ansi.Strip(m.View().Content), "starting anew") })
		return nil
	})
	if err := app.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	bot, _ := cli.Service[*plugin.Assistant](replaced.App)
	if got := bot.Conversation(shown); !strings.Contains(string(got), "starting anew") {
		t.Fatalf("the agent remembers %s", got)
	}
	if status, _ := (plugin.OwnerOf(home.Dir(s.Home), nil)).Status(); !status.Running {
		t.Fatal("the node ran before the restart and should run on")
	}
	if _, ok := os.LookupEnv(tuiplugin.ResumeEnv); ok {
		t.Fatal("the node started later should not see the variable")
	}

	// What cannot be read as a session to go on with is said so.
	for _, v := range []string{"{", `{"session":""}`} {
		t.Setenv(tuiplugin.ResumeEnv, v)
		if err := app.Run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "the conversation to go on with") {
			t.Fatalf("%s: got %v", v, err)
		}
	}
}

// What the plugins report as they prepare the context is shown with it.
func TestContextShowsPostprocessingDiagnostic(t *testing.T) {
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	hook := plugin.New("post", func(c *plugin.Context, _ struct{}) error {
		plugin.Add(c, plugin.AnswerHook{Name: "post", After: func(context.Context, plugin.AnswerOutcome) error { return errors.New("storage failed") }})
		return nil
	})
	u := backend(t, s, hook)
	if events := answer(t, u, "id", "hi"); !strings.Contains(events[len(events)-1].Text, "last=hi") {
		t.Fatal(events)
	}
	msgs, err := u.Contents(context.Background(), "id")
	if err != nil {
		t.Fatal(err)
	}
	last := msgs[len(msgs)-1]
	if last.Role != "plugin diagnostic" || last.Text != "post after failed: storage failed" {
		t.Fatal(msgs)
	}
}

// What became of a call is added to its line once it ended, and only to a
// line: a call that is still running has none of it, and a call no plugin
// shows stays unseen when it ended.
func TestStepShowsHowItEndedOnlyOnceItEnded(t *testing.T) {
	var ctx *plugin.Context
	views := plugin.New("views", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		plugin.Add(c, plugin.ToolView{Tool: "seen", Show: func(json.RawMessage) string { return "looking" }, Detail: func(plugin.Step) string { return "✓" }})
		plugin.Add(c, plugin.ToolView{Tool: "unseen", Show: func(json.RawMessage) string { return "" }, Detail: func(plugin.Step) string { return "✓" }})
		return nil
	})
	if _, err := cli.NewWith(views); err != nil {
		t.Fatal(err)
	}
	// A call failed once it ended with an error, and only then.
	for _, tt := range []struct {
		tool   string
		done   bool
		err    string
		want   string
		failed bool
	}{
		{"seen", false, "", "looking", false},
		{"seen", true, "", "looking ✓", false},
		{"seen", false, "broke", "looking", false},
		{"seen", true, "broke", "looking ✓", true},
		{"unseen", false, "", "", false},
		{"unseen", true, "", "", false},
	} {
		got := tuiplugin.Shown(ctx, plugin.SessionEvent{Kind: plugin.SessionStep, Tool: tt.tool, Done: tt.done, Err: tt.err})
		if got.Text != tt.want || got.Done != tt.done || got.Failed != tt.failed {
			t.Errorf("%s, done %v, error %q: shown as %q, failed %v", tt.tool, tt.done, tt.err, got.Text, got.Failed)
		}
	}
}

// A question of the agent is shown with its answers to choose from; once the
// wait for its answer is over, it is what the owner answered.
func TestQuestionIsShownWithItsOptions(t *testing.T) {
	asked := plugin.SessionEvent{Kind: plugin.SessionAsked, Args: json.RawMessage(`{"question":"Which?","options":["a","b"],"recommended":"b"}`)}
	if got := tuiplugin.Shown(nil, asked); got.Text != "Which?" || strings.Join(got.Options, ",") != "a,b" || got.Recommended != "b" || got.Done {
		t.Errorf("asked: %+v", got)
	}
	if got := tuiplugin.Shown(nil, plugin.SessionEvent{Kind: plugin.SessionAsked, Done: true, Text: "b"}); got.Text != "b" || got.Options != nil || !got.Done {
		t.Errorf("answered: %+v", got)
	}
}

// The report of a task the agent delegated is shown as a line: its number,
// what it was and how it ended.
func TestTaskIsShownAsALine(t *testing.T) {
	ended := plugin.SessionEvent{Kind: plugin.SessionTask, N: 2, Remark: "find the callers", Text: "they are…", Took: 2500 * time.Millisecond}
	if got := tuiplugin.Shown(nil, ended); got.Text != "task 2 · find the callers ✓ 3s" || got.Failed {
		t.Errorf("ended: %+v", got)
	}
	failed := plugin.SessionEvent{Kind: plugin.SessionTask, N: 3, Remark: "read", Err: "it broke\n  badly"}
	if got := tuiplugin.Shown(nil, failed); got.Text != "task 3 · read ✗ it broke badly" || !got.Failed {
		t.Errorf("failed: %+v", got)
	}
}

// How much works for the thread comes to the UI as it is.
func TestWorkingIsShown(t *testing.T) {
	if got := tuiplugin.Shown(nil, plugin.SessionEvent{Kind: plugin.SessionWorking, N: 2}); got.Kind != plugin.SessionWorking || got.N != 2 {
		t.Errorf("working: %+v", got)
	}
	if got := tuiplugin.Shown(nil, plugin.SessionEvent{Kind: plugin.SessionMessage, Text: "go", Routed: true}); !got.Routed {
		t.Errorf("a message the thread handed on: %+v", got)
	}
}
