package update_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/node"
	updateplugin "ouroboros/internal/plugins/update"
	"ouroboros/internal/update"
	"ouroboros/internal/update/updatetest"
	"ouroboros/plugin"
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

// source is a checkout that builds a binary with the given content, once the
// shell command build passes.
func source(t *testing.T, content, build string) string {
	t.Helper()
	src, _ := updatetest.NewRepo(t, "committed")
	updatetest.Change(t, src, content)
	updatetest.Build(t, src, build)
	return src
}

// The agent of the owner builds the source the binary was built from.
func TestRebuildToolInstallsTheSource(t *testing.T) {
	src := source(t, "v2", "true")
	update.Source = src
	t.Cleanup(func() { update.Source = "" })
	s := clitest.Settings(t)
	var ctx *plugin.Context
	app, _ := clitest.NewApp(t, s, "", plugin.New("reader", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	}))
	exe := filepath.Join(t.TempDir(), "ouro")
	updater, _ := cli.Service[*update.Updater](app)
	updater.Exe = exe
	alive := clitest.Bystander(t, s) // no node runs: there is nothing to restart

	tools := plugin.All[plugin.Tool](ctx)
	at := slices.IndexFunc(tools, func(tool plugin.Tool) bool { return tool.Name == updateplugin.Rebuild })
	if at < 0 || !strings.Contains(tools[at].Description, "source in "+src+" as it is now") || !strings.Contains(tools[at].Description, "It only builds, and runs no tests") ||
		strings.Contains(tools[at].Description, "sandbox") {
		t.Fatalf("tools %+v", tools)
	}
	if got := plugin.ToolLine(ctx, plugin.Step{Tool: updateplugin.Rebuild, Args: json.RawMessage(`{}`)}); got != "building "+src {
		t.Fatalf("shown %q", got)
	}

	// A peer builds nothing, whatever the configuration lets it call.
	peer := a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"})
	if got, err := tools[at].Run(peer, json.RawMessage(`{}`)); err == nil || got != "" || !strings.Contains(err.Error(), "for the owner") {
		t.Fatalf("for a peer: %q, %v", got, err)
	}
	if _, err := os.Stat(exe); err == nil {
		t.Fatal("a peer had the source built")
	}

	got, err := tools[at].Run(context.Background(), json.RawMessage(`{}`))
	if err != nil || !strings.HasPrefix(got, "built "+src+" and installed the build as "+exe+". ") {
		t.Fatalf("got %q, %v", got, err)
	}
	if built, _ := os.ReadFile(exe); string(built) != "v2" {
		t.Fatalf("binary %q", built)
	}
	if !strings.HasSuffix(got, "this conversation uses the old build until ouro is started again.") {
		t.Fatalf("without a node that starts anew: %q", got)
	}

	// In a session of the node, the node starts anew with the build once
	// the answer is given: the tool does not stop it.
	var restart string
	inNode := plugin.WithRestart(context.Background(), func(exe string) { restart = exe })
	got, err = tools[at].Run(inNode, json.RawMessage(`{}`))
	if err != nil || restart != exe || !strings.HasSuffix(got, "The node starts anew with it once you have answered, and this conversation goes on there.") {
		t.Fatalf("in the node: %q, %v, restart %q", got, err, restart)
	}
	if !alive() {
		t.Fatal("the build signalled an unrelated process")
	}
}

// tool returns the tool rebuild of the node of s, which builds src and
// installs over exe.
func tool(s cli.Settings, src string, exe func() (string, error)) func() (string, error) {
	updater := &update.Updater{Source: src, Dir: filepath.Join(s.Home, "update")}
	run := updateplugin.Tool(home.Dir(s.Home), updater, exe)
	return func() (string, error) { return run(context.Background()) }
}

func TestRebuildToolThatFails(t *testing.T) {
	s := clitest.Settings(t)
	path := filepath.Join(t.TempDir(), "ouro")
	exe := func() (string, error) { return path, nil }

	// A binary that does not know its source says so, and the tool too.
	if got, err := tool(s, "", exe)(); !errors.Is(err, update.ErrNoSource) || got != "" || strings.Contains(err.Error(), "output") {
		t.Fatalf("without a source: %q, %v", got, err)
	}
	var ctx *plugin.Context
	clitest.NewApp(t, s, "", plugin.New("reader", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	}))
	if got := plugin.ToolLine(ctx, plugin.Step{Tool: updateplugin.Rebuild}); got != "building a checkout this build does not know" {
		t.Fatalf("shown %q", got)
	}

	// A failing build installs nothing, and the agent gets what it printed.
	src := source(t, "v2", "echo ERROR: BuildOfTheAgent; exit 1")
	got, err := tool(s, src, exe)()
	if err == nil || got != "" || !strings.Contains(err.Error(), "; nothing was installed. The end of the output:\n") ||
		!strings.Contains(err.Error(), "\nERROR: BuildOfTheAgent\n") {
		t.Fatalf("a failing build: %q, %v", got, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a build that fails was installed")
	}

	src = source(t, "v2", "true")
	unknown := errors.New("no binary")
	if _, err := tool(s, src, func() (string, error) { return "", unknown })(); !errors.Is(err, unknown) {
		t.Fatalf("a binary that cannot be found: %v", err)
	}

	// A node that runs but cannot be stopped: the agent learns that the
	// build is in place and the node is not.
	clitest.Ghost(t, s)
	if got, err := tool(s, src, exe)(); err == nil || got != "" || !strings.HasPrefix(err.Error(), "the build is installed, but the node did not restart: ") {
		t.Fatalf("a node that does not stop: %q, %v", got, err)
	}
	if built, _ := os.ReadFile(path); string(built) != "v2" {
		t.Fatalf("binary %q", built)
	}
}

// Of a long output the agent gets the end, from the start of a line.
func TestTail(t *testing.T) {
	fill := func(n int) string { return strings.Repeat("x", n) }
	exact := "first\n" + fill(updateplugin.MaxOutput-6)
	for name, tt := range map[string]struct{ output, want string }{
		"short":           {"\n one\ntwo \n", "one\ntwo"},
		"just fits":       {exact, exact},
		"cut in a line":   {"first\nsecond\n" + fill(updateplugin.MaxOutput-3), fill(updateplugin.MaxOutput - 3)},
		"cut at a line":   {"first\n" + fill(updateplugin.MaxOutput-1), fill(updateplugin.MaxOutput - 1)},
		"one long line":   {fill(updateplugin.MaxOutput + 1), fill(updateplugin.MaxOutput)},
		"nothing printed": {"", ""},
	} {
		if got := updateplugin.Tail(tt.output); got != tt.want {
			t.Errorf("%s: got %d bytes %.20q…, want %d bytes", name, len(got), got, len(tt.want))
		}
	}
}
