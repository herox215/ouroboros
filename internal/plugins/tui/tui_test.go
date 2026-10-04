package tui_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	tuiplugin "ouroboros/internal/plugins/tui"
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
	return tuiplugin.Backend(plugin.OwnerOf(home.Dir(s.Home), clitest.Network(t, s, app)), ctx, bot)
}

func TestTuiWithoutTerminal(t *testing.T) {
	if _, err := clitest.Run(t, clitest.Settings(t), "", "tui"); err == nil {
		t.Fatal("want error")
	}
}

// Of a tool call the UI is told what the plugin of the tool wants shown: for
// ask_peer, which peer the agent is waiting for.
func TestTUIBackendAsk(t *testing.T) {
	alpha, _ := clitest.TwoNodes(t)
	u := backend(t, alpha)
	var steps []tui.Step
	reply, err := u.Ask(context.Background(), "ctx", "@beta hello", func(s tui.Step) { steps = append(steps, s) })
	if err != nil || !strings.Contains(reply, "not connected") {
		t.Fatalf("got %q, %v", reply, err)
	}
	if len(steps) != 2 || steps[0] != (tui.Step{Text: "asking beta"}) || steps[1] != (tui.Step{Text: "asking beta", Done: true}) {
		t.Fatalf("steps %+v", steps)
	}
	// Looking up the peers is not worth a line in the chat: no plugin says
	// what to show of it.
	steps = nil
	if _, err := u.Ask(context.Background(), "ctx", "@", func(s tui.Step) { steps = append(steps, s) }); err != nil || len(steps) != 0 {
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
	if err := u.Start(); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "press c") {
		t.Fatalf("start: %v", err)
	}
	if _, err := u.Ask(context.Background(), "ctx", "hi", func(tui.Step) {}); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "ouro start") {
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

// The UI is opened with what the plugins contributed to it, through the
// public API alone: here the screen of the example plugin, and a plugin that
// claims a key of the UI.
func TestUIShowsWhatPluginsContribute(t *testing.T) {
	greedy := plugin.New("greedy", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.Key{Key: "s", Help: "stats", Run: func(context.Context) (string, error) { return "", nil }})
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
		for _, want := range []string{`key s is taken by the terminal UI: "stats" is left out`, "g Greetings"} {
			if !strings.Contains(view, want) {
				t.Errorf("view lacks %q:\n%s", want, view)
			}
		}
		_, load := m.Update(key('g'))
		if load == nil {
			t.Fatal("g should open the screen of the example plugin")
		}
		m.Update(load())
		view = ansi.Strip(m.View().Content)
		for _, want := range []string{"Greetings", "No connected peers to greet.", "a greet all · esc back"} {
			if !strings.Contains(view, want) {
				t.Errorf("view lacks %q:\n%s", want, view)
			}
		}
		_, run := m.Update(key('a'))
		if m.Update(run()); !strings.Contains(ansi.Strip(m.View().Content), "greeted 0 peers") {
			t.Errorf("the key of the screen should say what it did:\n%s", ansi.Strip(m.View().Content))
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
	if n := len(lines); n < 2 || !strings.Contains(lines[n-2], "chat c1: tool block {} took ") || !strings.HasSuffix(lines[n-2], ": canceled") ||
		!strings.Contains(lines[n-1], "chat c1: canceled after ") {
		t.Fatalf("log:\n%s", logged)
	}
	if err := <-answered; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
