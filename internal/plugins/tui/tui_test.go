package tui_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	tuiplugin "ouroboros/internal/plugins/tui"
	"ouroboros/internal/setup"
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

// backend is what the terminal UI of the node of s controls.
func backend(t *testing.T, s cli.Settings) tui.Backend {
	t.Helper()
	var ctx *plugin.Context
	app, _ := clitest.NewApp(t, s, "", spy(&ctx))
	bot, _ := cli.Service[*plugin.Assistant](app)
	return tuiplugin.Backend(plugin.OwnerOf(home.Dir(s.Home), clitest.Network(t, s, app), setup.Offer{BaseURL: s.BaseURL, APIKey: s.APIKey}), ctx, bot)
}

// The UI sets the node up with what the owner does: the setup offers the
// endpoint of the settings, and one whose configuration cannot be read is
// none.
func TestTUIBackendSetup(t *testing.T) {
	s := clitest.Settings(t)
	set, err := backend(t, s).Setup()
	if err != nil || set.Endpoint() != s.BaseURL {
		t.Fatalf("got %v, %v", set, err)
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
	var steps []tui.Step
	var says []string
	reply, err := u.Ask(context.Background(), "ctx", "@beta hello", func(s tui.Step) { steps = append(steps, s) },
		func(s string) { says = append(says, s) })
	if err != nil || !strings.Contains(reply, "not connected") {
		t.Fatalf("got %q, %v", reply, err)
	}
	// The round that asks beta is told of although the agent said nothing
	// along with it: that is how the UI tells the rounds apart.
	if len(says) != 1 || says[0] != "" {
		t.Fatalf("says %q", says)
	}
	if len(steps) != 2 || steps[0] != (tui.Step{Text: "asking beta"}) ||
		!steps[1].Done || !strings.HasPrefix(steps[1].Text, "asking beta ✓ ") {
		t.Fatalf("steps %+v", steps)
	}
	// The context of the conversation is what the LLM is sent next, message
	// by message: the call of a tool with its arguments.
	msgs, err := u.Contents("ctx")
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	if err != nil || strings.Join(roles, " ") != "system tool run user assistant tool assistant" ||
		!strings.Contains(msgs[1].Text, "\nparameters: {") || msgs[2].Text != "@beta hello" || !strings.HasPrefix(msgs[3].Text, "→ run {") {
		t.Fatalf("contents %q, %v", msgs, err)
	}
	// Looking up the peers is not worth a line in the chat: no plugin says
	// what to show of it.
	steps = nil
	if _, err := u.Ask(context.Background(), "ctx", "@", func(s tui.Step) { steps = append(steps, s) }, nil); err != nil || len(steps) != 0 {
		t.Fatalf("steps %+v, %v", steps, err)
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

// A node that is not set up tells the user of the UI how to get there.
func TestTUIBackendWithoutSetup(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	u := backend(t, s)
	if err := u.Start(); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "type /setup") {
		t.Fatalf("start: %v", err)
	}
	if _, err := u.Ask(context.Background(), "ctx", "hi", func(tui.Step) {}, nil); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "ouro start") {
		t.Fatalf("ask: %v", err)
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
}

// The model screen of the UI lists the models of the provider the node is
// set up with, and saves the model the owner picked: without the rest of the
// configuration changing.
func TestTUIBackendModels(t *testing.T) {
	s := clitest.Settings(t)
	u := backend(t, s)
	if _, err := u.Models(context.Background()); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "type /setup") {
		t.Fatalf("a node that is not set up: %v", err)
	}
	if err := u.SetModel("gpt-4o-mini"); !errors.Is(err, config.ErrIncomplete) {
		t.Fatalf("a node that is not set up: %v", err)
	}
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	models, err := u.Models(context.Background())
	if err != nil || !slices.Contains(models.Chat, "gpt-4o") {
		t.Fatalf("models %+v, %v", models, err)
	}
	if err := u.SetModel("gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	if cfg := clitest.MustLoad(t, s); cfg.Model != "gpt-4o-mini" || cfg.BaseURL != s.BaseURL {
		t.Fatalf("the configuration after saving: %+v", cfg)
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
		plugin.Add(ctx, plugin.Slash{Name: "stop", Help: "stats", Run: func(context.Context) (string, error) { return "", nil }})
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
		if want := `/stop is taken by the terminal UI: "stats" is left out`; !strings.Contains(view, want) {
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

// An answer still on its way when the owner quits the UI is stopped, and the
// log says what it was waiting for before the UI is gone.
func TestQuitLogsThePendingAnswer(t *testing.T) {
	started := make(chan struct{})
	block := plugin.New("block", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.NewTool("block", "waits until it is stopped", nil, func(ctx context.Context, _ struct{}) (string, error) {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}))
		return nil
	})
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	app, _ := clitest.NewAppOn(t, s, cli.Terminal{Interactive: true}, block)
	bot, _ := cli.Service[*plugin.Assistant](app)
	answered := make(chan error, 1)
	tuiplugin.Use(t, func(*tui.Model) error {
		go func() {
			_, err := bot.Ask(context.Background(), "c1", "!block", nil)
			answered <- err
		}()
		<-started
		return nil // the owner quits
	})
	if err := app.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	logged, _ := os.ReadFile(home.Dir(s.Home).Service().LogFile)
	lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
	// The tool is called by a script, which is canceled with it.
	if n := len(lines); n < 3 || !strings.Contains(lines[n-3], "chat c1: tool block {} took ") || !strings.HasSuffix(lines[n-3], ": canceled") ||
		!strings.Contains(lines[n-2], "chat c1: tool run ") || !strings.HasSuffix(lines[n-2], ": canceled") ||
		!strings.Contains(lines[n-1], "chat c1: canceled after ") {
		t.Fatalf("log:\n%s", logged)
	}
	if err := <-answered; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
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

// A tool the owner's agent calls in the UI can ask for ouro to start anew;
// the assistant alone has nobody who does.
func TestTUIBackendRestart(t *testing.T) {
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	var ctx *plugin.Context
	app, _ := clitest.NewApp(t, s, "", again("/new/ouro"), spy(&ctx))
	bot, _ := cli.Service[*plugin.Assistant](app)
	if _, err := bot.Ask(context.Background(), "c0", "!again", nil); err != nil {
		t.Fatal(err)
	}
	if got := bot.Conversation("c0"); !strings.Contains(string(got), "nobody starts ouro anew") {
		t.Fatalf("without the UI: %s", got)
	}
	u := tuiplugin.Backend(nil, ctx, bot)
	if u.Restarting() {
		t.Fatal("nothing asked for a restart yet")
	}
	if _, err := u.Ask(context.Background(), "c1", "!again", func(tui.Step) {}, nil); err != nil || !u.Restarting() {
		t.Fatalf("the tool should have asked for a restart: %v", err)
	}
}

// When a tool asks for ouro to start anew, the UI quits once the answer is
// in, and ouro ends with plugin.Restarting. The UI of the new process goes
// on from what it hands over: the chat, what the agent remembers of it, and
// a node that ran before stays running. The file is read once.
func TestRestartGoesOnWithTheConversation(t *testing.T) {
	s := clitest.Settings(t)
	clitest.StartNode(t, s, clitest.NodeConfig(s))
	app, _ := clitest.NewAppOn(t, s, cli.Terminal{Interactive: true}, again("/new/ouro"))

	var before tui.Chat
	tuiplugin.Use(t, func(m *tui.Model) error {
		m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
		m.Update(m.Init()().(tea.BatchMsg)[0]()) // the status of the node, which runs
		m.Update(key(' '))                       // skips the splash
		for _, r := range "!again" {
			m.Update(key(r))
		}
		_, cmd := m.Update(enter)
		for cmd != nil {
			msg := cmd()
			if _, quit := msg.(tea.QuitMsg); quit {
				break
			}
			_, cmd = m.Update(msg)
		}
		if !m.Restarts() {
			t.Fatal("the UI should quit to start anew")
		}
		before = m.Chat()
		return nil
	})
	err := app.Run(context.Background(), nil)
	var r *plugin.Restarting
	if !errors.As(err, &r) || r.Exe != "/new/ouro" || err.Error() != "ouro is to be started anew from /new/ouro" {
		t.Fatalf("got %v", err)
	}
	if len(before.Entries) != 2 || !strings.Contains(before.Entries[1].Text, "starting anew") {
		t.Fatalf("the chat %+v", before)
	}
	var file string
	for _, kv := range r.Env {
		if v, ok := strings.CutPrefix(kv, tuiplugin.ResumeEnv+"="); ok {
			file = v
		}
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the file to go on from: %v, %v", info, err)
	}

	// The new process.
	t.Setenv(tuiplugin.ResumeEnv, file)
	app, _ = clitest.NewAppOn(t, s, cli.Terminal{Interactive: true})
	bot, _ := cli.Service[*plugin.Assistant](app)
	var after tui.Chat
	tuiplugin.Use(t, func(m *tui.Model) error {
		after = m.Chat()
		return nil
	})
	if err := app.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if after.ContextID != before.ContextID || !reflect.DeepEqual(after.Entries, before.Entries) {
		t.Fatalf("resumed %+v, want %+v", after, before)
	}
	if got := bot.Conversation(after.ContextID); !strings.Contains(string(got), "starting anew") {
		t.Fatalf("the agent remembers %s", got)
	}
	if status, _ := (plugin.OwnerOf(home.Dir(s.Home), nil, setup.Offer{})).Status(); !status.Running {
		t.Fatal("the node ran before the restart and should run on")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("the file should be gone")
	}
	if _, ok := os.LookupEnv(tuiplugin.ResumeEnv); ok {
		t.Fatal("the node started later should not see the variable")
	}

	// A file that cannot be read, or not as a conversation, is said so.
	dir := t.TempDir()
	for name, content := range map[string]string{"not JSON": "{", "no memory": `{"memory": 5}`} {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600)
	}
	for _, name := range []string{"gone", "not JSON", "no memory"} {
		t.Setenv(tuiplugin.ResumeEnv, filepath.Join(dir, name))
		if err := app.Run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "the conversation to go on with") {
			t.Fatalf("%s: got %v", name, err)
		}
	}
}

// What cannot be handed over leaves no file behind.
func TestHandOverFails(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if err := tuiplugin.HandOver("/new/ouro", tui.Chat{}, json.RawMessage("{"), false); err == nil {
		t.Fatal("a memory that is no JSON should fail")
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	t.Setenv("TMPDIR", filepath.Join(tmp, "gone"))
	if err := tuiplugin.HandOver("/new/ouro", tui.Chat{}, nil, false); err == nil {
		t.Fatal("without a directory for the file it should fail")
	}
}

func TestContextShowsPostprocessingDiagnostic(t *testing.T) {
	s := clitest.Settings(t)
	cfg := clitest.NodeConfig(s)
	config.Save(s.Home, cfg)
	var ctx *plugin.Context
	hook := plugin.New("post", func(c *plugin.Context, _ struct{}) error {
		plugin.Add(c, plugin.AnswerHook{Name: "post", After: func(context.Context, plugin.AnswerOutcome) error { return errors.New("storage failed") }})
		return nil
	})
	app, _ := clitest.NewApp(t, s, "", spy(&ctx), hook)
	bot, _ := cli.Service[*plugin.Assistant](app)
	u := tuiplugin.Backend(plugin.OwnerOf(home.Dir(s.Home), clitest.Network(t, s, app), setup.Offer{BaseURL: s.BaseURL, APIKey: s.APIKey}), ctx, bot)
	reply, err := u.Ask(context.Background(), "id", "hi", nil, nil)
	if err != nil || !strings.Contains(reply, "last=hi") {
		t.Fatal(reply, err)
	}
	msgs, err := u.Contents("id")
	if err != nil {
		t.Fatal(err)
	}
	last := msgs[len(msgs)-1]
	if last.Role != "plugin diagnostic" || last.Text != "post after failed: storage failed" {
		t.Fatal(msgs)
	}
}
