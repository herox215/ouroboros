package update_test

import (
	"bytes"
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
	"ouroboros/internal/node"
	updateplugin "ouroboros/internal/plugins/update"
	"ouroboros/internal/update"
	"ouroboros/internal/update/updatetest"
)

func TestMain(m *testing.M) { clitest.Main(m) }

func TestUpdateInstallsNewestVersion(t *testing.T) {
	s := clitest.Settings(t)
	repo, rev := updatetest.NewRepo(t, "v1")
	exe := filepath.Join(t.TempDir(), "ouro")
	app, out := clitest.NewApp(t, s, "")
	updater, _ := cli.Service[*update.Updater](app)
	updater.Repo, updater.Exe, updater.Revision = repo, exe, "old"
	alive := clitest.Bystander(t, s) // no node runs: there is nothing to restart

	if err := app.Run(context.Background(), []string{"update"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "v1" || !strings.Contains(out.String(), "updated to "+rev[:7]) {
		t.Fatalf("binary %q, output %q", got, out.String())
	}
	if !alive() {
		t.Fatal("the update signalled an unrelated process")
	}

	out.Reset()
	updater.Revision = rev
	if err := app.Run(context.Background(), []string{"update"}); err != nil || !strings.Contains(out.String(), "already up to date") {
		t.Fatalf("second update: %v, output %q", err, out.String())
	}
}

// command returns the update command of the node of s, which installs from
// repo over exe, and what it prints.
func command(t *testing.T, s cli.Settings, repo string, exe func() (string, error)) (func() error, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	updater := &update.Updater{Repo: repo, Dir: filepath.Join(s.Home, "update"), Revision: "old"}
	run := updateplugin.Command(home.Dir(s.Home), &out, updater, exe)
	return func() error { return run(context.Background(), nil) }, &out
}

// Without a path to install to, the update goes over the binary of the node.
func TestUpdateReplacesTheBinaryOfTheNode(t *testing.T) {
	s := clitest.Settings(t)
	repo, _ := updatetest.NewRepo(t, "v2")
	exe := filepath.Join(t.TempDir(), "ouro")
	run, _ := command(t, s, repo, func() (string, error) { return exe, nil })
	if err := run(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "v2" {
		t.Fatalf("binary %q", got)
	}

	unknown := errors.New("no binary")
	run, _ = command(t, s, repo, func() (string, error) { return "", unknown })
	if err := run(); !errors.Is(err, unknown) {
		t.Fatalf("a binary that cannot be found: %v", err)
	}
}

func TestUpdateThatFails(t *testing.T) {
	s := clitest.Settings(t)
	exe := func() (string, error) { return filepath.Join(t.TempDir(), "ouro"), nil }
	run, out := command(t, s, filepath.Join(t.TempDir(), "no-repository"), exe)
	if err := run(); err == nil || strings.Contains(out.String(), "updated") {
		t.Fatalf("got %v, output %q", err, out.String())
	}

	// A node that runs but cannot be stopped is not replaced silently.
	repo, _ := updatetest.NewRepo(t, "v1")
	clitest.Ghost(t, s)
	run, out = command(t, s, repo, exe)
	if err := run(); err == nil || strings.Contains(out.String(), "updated to") {
		t.Fatalf("got %v, output %q", err, out.String())
	}
}

// A node that runs is stopped and started again from the new binary.
func TestUpdateRestartsTheNode(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	config.Save(s.Home, clitest.NodeConfig(s))
	self := func() (string, error) { return os.Args[0], nil }
	local := node.Local{Home: home.Dir(s.Home), Exe: self}
	before, err := local.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { local.Stop() })

	if err := updateplugin.Restart(home.Dir(s.Home), os.Args[0]); err != nil {
		t.Fatal(err)
	}
	after, ok := local.Running()
	if !ok || after.PID == before.PID {
		t.Fatalf("the node should run anew: %+v, %v, before %+v", after, ok, before)
	}
}
