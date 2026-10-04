package tui_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/assistant"
	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/peers"
	tuiplugin "ouroboros/internal/plugins/tui"
	"ouroboros/internal/tui"
)

func TestMain(m *testing.M) { clitest.Main(m) }

// backend is what the terminal UI of the node of s controls.
func backend(t *testing.T, s cli.Settings) tui.Backend {
	t.Helper()
	app, _ := clitest.NewApp(t, s, "")
	bot, _ := cli.Service[*assistant.Assistant](app)
	self := func() (string, error) { return os.Args[0], nil }
	return tuiplugin.Backend(home.Dir(s.Home), clitest.Network(t, s, app), bot, self)
}

func TestTuiWithoutTerminal(t *testing.T) {
	if _, err := clitest.Run(t, clitest.Settings(t), "", "tui"); err == nil {
		t.Fatal("want error")
	}
}

func TestTUIBackendAsk(t *testing.T) {
	alpha, _ := clitest.TwoNodes(t)
	u := backend(t, alpha)
	var steps []tui.Step
	reply, err := u.Ask(context.Background(), "ctx", "@beta hello", func(s tui.Step) { steps = append(steps, s) })
	if err != nil || !strings.Contains(reply, "not connected") {
		t.Fatalf("got %q, %v", reply, err)
	}
	if len(steps) != 2 || steps[0] != (tui.Step{Peer: "beta"}) || steps[1] != (tui.Step{Peer: "beta", Done: true}) {
		t.Fatalf("steps %+v", steps)
	}
	// Looking up the peers is not worth a line in the chat.
	steps = nil
	if _, err := u.Ask(context.Background(), "ctx", "@", func(s tui.Step) { steps = append(steps, s) }); err != nil || len(steps) != 0 {
		t.Fatalf("steps %+v, %v", steps, err)
	}
}

// A decision on a connection request that cannot be stored reaches the UI as
// tui.ErrNotSaved, without the cause, which is put down in the log instead.
func TestTUIBackendDecisionThatIsNotSaved(t *testing.T) {
	s := clitest.Settings(t)
	u, dir := backend(t, s), home.Dir(s.Home)
	ctx := context.Background()
	if err := dir.Contacts().AddIncoming("pk-gamma", "gamma", "", ""); err != nil {
		t.Fatal(err)
	}

	// A directory in the place of the lock: the contacts cannot be changed.
	lock := filepath.Join(s.Home, "contacts.json.lock")
	os.Remove(lock)
	os.Mkdir(lock, 0o700)
	for action, decide := range map[string]func(context.Context, string) error{"accept": u.Accept, "reject": u.Reject} {
		if err := decide(ctx, "pk-gamma"); err != tui.ErrNotSaved {
			t.Fatalf("%s: %v", action, err)
		}
	}
	logged, _ := os.ReadFile(dir.Service().LogFile)
	for _, want := range []string{"accept the connection request of pk-gamma", "reject the connection request of pk-gamma", "contacts.json.lock"} {
		if !strings.Contains(string(logged), want) {
			t.Fatalf("log %q should contain %q", logged, want)
		}
	}

	// Nothing was decided: once the contacts can be written, it is.
	os.Remove(lock)
	if d, _ := dir.Contacts().Snapshot(); len(d.Incoming) != 1 || len(d.Trusted) != 0 || len(d.Blocked) != 0 {
		t.Fatalf("a decision that failed changed the contacts: %+v", d)
	}
	if err := u.Accept(ctx, "pk-gamma"); err != nil {
		t.Fatal(err)
	}
	if err := u.Reject(ctx, "pk-gamma"); !errors.Is(err, peers.ErrNoRequest) {
		t.Fatalf("a request that is decided: %v", err)
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

// The UI starts and stops the node, and shows how it is.
func TestTUIBackendControlsTheNode(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	u := backend(t, s)
	t.Cleanup(func() { u.Stop() })

	if status, err := u.Status(); err != nil || status.Configured || status.Running {
		t.Fatalf("a fresh node: %+v, %v", status, err)
	}
	if err := u.Start(); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "press c") {
		t.Fatalf("a node that is not set up: %v", err)
	}
	cmd, err := u.SetupCommand()
	if err != nil || cmd.Path != os.Args[0] || strings.Join(cmd.Args[1:], " ") != "start --setup" {
		t.Fatalf("setup: %+v, %v", cmd, err)
	}

	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	if err := u.Start(); err != nil {
		t.Fatal(err)
	}
	if status, err := u.Status(); err != nil || !status.Running || status.Name != "alpha" || status.Port == 0 {
		t.Fatalf("a started node: %+v, %v", status, err)
	}
	if err := u.Stop(); err != nil {
		t.Fatal(err)
	}
	if status, _ := u.Status(); status.Running {
		t.Fatal("the node should be stopped")
	}

	unknown := errors.New("no binary")
	lost := tuiplugin.Backend(home.Dir(s.Home), nil, nil, func() (string, error) { return "", unknown })
	if _, err := lost.SetupCommand(); !errors.Is(err, unknown) {
		t.Fatalf("a binary that cannot be found: %v", err)
	}
}

// The UI shows the connection requests in both directions.
func TestTUIBackendContacts(t *testing.T) {
	s := clitest.Settings(t)
	u, store := backend(t, s), home.Dir(s.Home).Contacts()
	store.AddIncoming("pk-in", "in", "", "")
	store.AddOutgoing("pk-out", "out", "", "")
	got, err := u.Contacts()
	if err != nil || len(got.Incoming) != 1 || got.Incoming[0].Name != "in" || len(got.Outgoing) != 1 || got.Outgoing[0].Name != "out" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// An assistant that is not set up tells the user of the UI how to get there.
func TestTUIBackendAskWithoutSetup(t *testing.T) {
	u := backend(t, clitest.Settings(t))
	if _, err := u.Ask(context.Background(), "ctx", "hi", func(tui.Step) {}); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "ouro start") {
		t.Fatalf("got %v", err)
	}
}
