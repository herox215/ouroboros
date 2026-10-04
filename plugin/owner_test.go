package plugin_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/plugin"
)

// The owner starts the node, which is this test binary run as ouro.
func TestMain(m *testing.M) { clitest.Main(m) }

// owner is what the owner of the node of s does.
func owner(t *testing.T, s cli.Settings) *plugin.Owner {
	t.Helper()
	app, _ := clitest.NewApp(t, s, "")
	return plugin.OwnerOf(home.Dir(s.Home), clitest.Network(t, s, app))
}

// The owner starts and stops the node, and sees how it is.
func TestOwnerControlsTheNode(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	o := owner(t, s)
	t.Cleanup(func() { o.Stop() })

	if status, err := o.Status(); err != nil || status.Configured || status.Running || o.Running() {
		t.Fatalf("a fresh node: %+v, %v", status, err)
	}
	if err := o.Start(); err != plugin.ErrNotSetUp {
		t.Fatalf("a node that is not set up: %v", err)
	}
	self := func() (string, error) { return "/usr/bin/ouro", nil }
	cmd, err := o.WithBinary(self).SetupCommand()
	if err != nil || cmd.Path != "/usr/bin/ouro" || strings.Join(cmd.Args[1:], " ") != "start --setup" {
		t.Fatalf("setup: %+v, %v", cmd, err)
	}
	unknown := errors.New("no binary")
	if _, err := o.WithBinary(func() (string, error) { return "", unknown }).SetupCommand(); !errors.Is(err, unknown) {
		t.Fatalf("a binary that cannot be found: %v", err)
	}
	o.WithBinary(nil)

	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	if err := o.Start(); err != nil {
		t.Fatal(err)
	}
	if status, err := o.Status(); err != nil || !status.Running || !o.Running() || status.Name != "alpha" || status.Port == 0 {
		t.Fatalf("a started node: %+v, %v", status, err)
	}
	if err := o.Stop(); err != nil {
		t.Fatal(err)
	}
	if o.Running() {
		t.Fatal("the node should be stopped")
	}
}

// The owner sees the connection requests in both directions.
func TestOwnerRequests(t *testing.T) {
	s := clitest.Settings(t)
	o, store := owner(t, s), home.Dir(s.Home).Contacts()
	store.AddIncoming("pk-in", "in", "", "")
	store.AddOutgoing("pk-out", "out", "", "")
	incoming, outgoing, err := o.Requests()
	if err != nil || len(incoming) != 1 || incoming[0].Name != "in" || len(outgoing) != 1 || outgoing[0].Name != "out" {
		t.Fatalf("got %+v, %+v, %v", incoming, outgoing, err)
	}
}

// A decision on a connection request that cannot be stored reaches the front
// end as ErrNotSaved, without the cause, which is put down in the log instead.
func TestOwnerDecisionThatIsNotSaved(t *testing.T) {
	s := clitest.Settings(t)
	o, dir := owner(t, s), home.Dir(s.Home)
	ctx := context.Background()
	if err := dir.Contacts().AddIncoming("pk-gamma", "gamma", "", ""); err != nil {
		t.Fatal(err)
	}

	// A directory in the place of the lock: the contacts cannot be changed.
	lock := filepath.Join(s.Home, "contacts.json.lock")
	os.Remove(lock)
	os.Mkdir(lock, 0o700)
	for action, decide := range map[string]func(context.Context, string) error{"accept": o.Accept, "reject": o.Reject} {
		if err := decide(ctx, "pk-gamma"); err != plugin.ErrNotSaved {
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
	if err := o.Accept(ctx, "pk-gamma"); err != nil {
		t.Fatal(err)
	}
	if err := o.Reject(ctx, "pk-gamma"); !errors.Is(err, plugin.ErrNoRequest) {
		t.Fatalf("a request that is decided: %v", err)
	}
}
