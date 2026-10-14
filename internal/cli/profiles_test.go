package cli_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/credentials"
	"ouroboros/internal/daemon"
	"ouroboros/internal/home"
	"ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
	"ouroboros/internal/session"
	"ouroboros/plugin"
)

// syncBuffer is what a terminal prints, for a test that reads it while a
// node writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

// Take returns what was printed since the last time, and forgets it.
func (b *syncBuffer) Take() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	defer b.b.Reset()
	return b.b.String()
}

// twoProfiles sets up the node of s as Terra, with the profile Luna besides,
// which answers with another model through another access.
func twoProfiles(t *testing.T, s cli.Settings) (terra, luna home.Dir) {
	t.Helper()
	terra = home.Dir(s.Home)
	t.Cleanup(func() { os.RemoveAll(s.Home + "-workspaces") })
	cfg := clitest.NodeConfig(s)
	cfg.Name = "Terra"
	if err := terra.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	luna, err := terra.AddProfile("Luna")
	if err != nil {
		t.Fatal(err)
	}
	other, err := terra.Credentials().Put(credentials.Access{Provider: openai.ProviderName, Credentials: openai.Credentials(s.BaseURL, openaitest.Key)})
	if err != nil {
		t.Fatal(err)
	}
	err = config.Update(string(luna), func(c *config.Config) error {
		c.Access, c.Model = other.ID, "luna-model"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return terra, luna
}

// serve runs "ouro serve" of app until the test ends, or until the node
// stops by itself: done is closed then, and err is what serve ended with.
func serve(t *testing.T, app *cli.App, dir home.Dir, args ...string) (done <-chan struct{}, err *error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	var served error
	go func() {
		defer close(ended)
		served = app.Run(ctx, append(args, "serve"))
	}()
	t.Cleanup(func() {
		cancel()
		<-ended
	})
	if _, ok := daemon.WaitRunning(dir.Service().PIDFile, clitest.Patience); !ok {
		t.Fatal("the node did not start")
	}
	return ended, &served
}

// A node with several profiles runs them all in one process, each with its
// agent, model and sessions. A command for one of them names it; one about
// the node does not need to.
func TestProfiles(t *testing.T) {
	s := clitest.Settings(t)
	terra, luna := twoProfiles(t, s)
	terraID, _ := terra.Identity()
	var out syncBuffer
	app, err := cli.New(s, cli.Terminal{In: strings.NewReader(""), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, args := range [][]string{{"chat", "hi"}, {"status"}, {"start", "--setup"}} {
		if err := app.Run(ctx, args); err == nil || !strings.Contains(err.Error(), "the node has several profiles: name the one to run it for, as in ouro --profile PROFILE "+strings.Join(args, " ")) {
			t.Errorf("%v without a profile: %v", args, err)
		}
	}
	for _, args := range [][]string{{"--profile", "Mars", "status"}, {"--profile"}, {"--profile=", "status"}, {"--profile", " ", "status"}} {
		if err := app.Run(ctx, args); err == nil {
			t.Errorf("%v should fail", args)
		}
	}
	// What is about the node needs no profile.
	if err := app.Run(ctx, []string{"profile"}); err != nil {
		t.Fatal(err)
	}
	listed := out.Take()
	lunaID, _ := luna.Identity()
	if !strings.Contains(listed, terraID.ID()+"  Terra  model=gpt-4o\n") || !strings.Contains(listed, lunaID.ID()+"  Luna  model=luna-model\n") {
		t.Fatalf("profiles:\n%s", listed)
	}
	if err := app.Run(ctx, []string{"--profile=luna", "status"}); err != nil || out.Take() != "○ not running\n" {
		t.Fatalf("status: %v", err)
	}
	for _, dir := range []home.Dir{terra, luna} {
		if w := dir.WorkingDir(); w == "" {
			t.Fatalf("no workspace for %s", dir)
		} else if info, err := os.Stat(w); err != nil || !info.IsDir() {
			t.Fatalf("workspace %s: %v", w, err)
		}
	}

	// The node runs from the first profile, whichever is named.
	done, served := serve(t, app, terra, "--profile", "Luna")
	if err := app.Run(ctx, []string{"--profile", "Luna", "chat", "hello Luna"}); err != nil {
		t.Fatal(err)
	}
	// What the node logs goes to the terminal too.
	if got := out.Take(); !strings.HasSuffix(got, "\n[luna-model] turns=1 last=hello Luna\n") || !strings.Contains(got, "(Luna) listening on") {
		t.Fatalf("Luna answered %q", got)
	}
	if err := app.Run(ctx, []string{"--profile", terraID.ID()[:8], "chat", "hello Terra"}); err != nil {
		t.Fatal(err)
	}
	if got := out.Take(); got != "[gpt-4o] turns=1 last=hello Terra\n" {
		t.Fatalf("Terra answered %q", got)
	}
	if err := app.Run(ctx, []string{"--profile", "Luna", "status"}); err != nil || !strings.Contains(out.Take(), "name=Luna") {
		t.Fatalf("status of Luna: %v", err)
	}
	// Named without a command, a profile is run like ouro without one.
	if err := app.Run(ctx, []string{"--profile", "Luna"}); err != nil || !strings.HasPrefix(out.Take(), "ouro – tiny A2A agent node") {
		t.Fatalf("Luna without a command: %v", err)
	}
	terraSessions, lunaSessions := session.NewClient(terra), session.NewClient(luna)
	terraList, err := terraSessions.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lunaList, err := lunaSessions.List(ctx)
	if err != nil || len(terraList) != 1 || len(lunaList) != 1 || terraList[0].ID == lunaList[0].ID || lunaList[0].Title != "hello Luna" {
		t.Fatalf("sessions %+v and %+v, %v", terraList, lunaList, err)
	}
	// A profile that was added after this ouro started is not in it.
	if _, err := terra.AddProfile("Sol"); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(ctx, []string{"--profile", "Sol", "status"}); err == nil || !strings.Contains(err.Error(), `the profile "Sol" was added after this ouro started`) {
		t.Fatalf("Sol: %v", err)
	}
	// A front end of either profile that lets the node go lets it stop.
	if err := lunaSessions.Release(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if *served != nil {
			t.Fatal(*served)
		}
	case <-time.After(clitest.Patience):
		t.Fatal("the node kept running")
	}

	// Started again, each profile goes on with its sessions.
	again, err := cli.New(s, cli.Terminal{In: strings.NewReader(""), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	serve(t, again, terra)
	if after, err := session.NewClient(luna).List(ctx); err != nil || len(after) != 1 || after[0].ID != lunaList[0].ID {
		t.Fatalf("Luna after the restart: %+v, %v", after, err)
	}
	if after, err := session.NewClient(terra).List(ctx); err != nil || len(after) != 1 || after[0].ID != terraList[0].ID {
		t.Fatalf("Terra after the restart: %+v, %v", after, err)
	}
}

// A node with one profile needs none to be named, and takes its own; an ouro
// without profiles takes none.
func TestOneProfile(t *testing.T) {
	s := clitest.Settings(t)
	cfg := clitest.NodeConfig(s)
	config.Save(s.Home, cfg)
	var out syncBuffer
	app, err := cli.New(s, cli.Terminal{In: strings.NewReader(""), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"status"}, {"--profile", "alpha", "status"}} {
		if err := app.Run(context.Background(), args); err != nil || out.Take() != "○ not running\n" {
			t.Fatalf("%v: %v", args, err)
		}
	}
	// It has its workspace as soon as it has the key that names it.
	if _, err := os.Stat(s.Home + "-workspaces"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a node without a key has a workspace: %v", err)
	}
	dir := home.Dir(s.Home)
	if _, err := dir.Identity(); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.New(s, cli.Terminal{In: strings.NewReader(""), Out: &out}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir.WorkingDir()); err != nil || !info.IsDir() {
		t.Fatalf("the workspace of the only profile: %v", err)
	}
	plain, _ := clitest.NewApp(t, s, "")
	if err := plain.Run(context.Background(), []string{"--profile", "alpha", "status"}); err == nil || err.Error() != "this ouro runs no profiles" {
		t.Fatalf("got %v", err)
	}
}

// A node whose profiles cannot all be started does not start any.
func TestProfilesThatCannotStart(t *testing.T) {
	term := cli.Terminal{In: strings.NewReader(""), Out: &syncBuffer{}}
	unlisted := clitest.Settings(t)
	os.WriteFile(filepath.Join(unlisted.Home, "profiles"), nil, 0o600)
	if _, err := cli.New(unlisted, term); err == nil {
		t.Error("profiles that cannot be listed should fail")
	}

	s := clitest.Settings(t)
	_, luna := twoProfiles(t, s)
	os.WriteFile(filepath.Join(string(luna), "config.json"), []byte(`{"private_key":"nonsense"}`), 0o600)
	if _, err := cli.New(s, term); err == nil || !strings.Contains(err.Error(), "the profile in "+string(luna)) {
		t.Errorf("a profile without an identity: %v", err)
	}

	s = clitest.Settings(t)
	twoProfiles(t, s)
	os.WriteFile(s.Home+"-workspaces", nil, 0o600)
	if _, err := cli.New(s, term); err == nil {
		t.Error("workspaces that cannot be made should fail")
	}
	os.Remove(s.Home + "-workspaces")

	broken := plugin.New("broken", func(*plugin.Context, struct{ Missing *testing.T }) error { return nil })
	for i, want := range []string{"plugin broken needs", "the profile in "} {
		standard := *cli.Standard
		*cli.Standard = func(at cli.Settings, t cli.Terminal, p *cli.Profile) []plugin.Plugin {
			list := standard(at, t, p)
			if (i == 0) == (home.Dir(at.Home).Root() == home.Dir(at.Home)) {
				list = append(list, broken)
			}
			return list
		}
		_, err := cli.New(s, term)
		*cli.Standard = standard
		// The first profile is the node itself: its error names no profile.
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "the profile in ") != (i > 0) {
			t.Errorf("a plugin that cannot start: %v, want %q", err, want)
		}
	}
}

// GUI profiles are joined without those that turned it off, including when
// the first profile or every profile turned it off. Starting needs no display.
func TestProfilesWithoutTheGUI(t *testing.T) {
	for _, off := range [][]bool{{false, false}, {false, true}, {true, false}, {true, true}} {
		t.Run(fmt.Sprint(off), func(t *testing.T) {
			s := clitest.Settings(t)
			terra, luna := twoProfiles(t, s)
			for i, dir := range []home.Dir{terra, luna} {
				if off[i] {
					if err := config.Update(string(dir), func(c *config.Config) error {
						c.Plugins = &config.Plugins{Off: []string{"gui"}}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
			app, err := cli.New(s, cli.Terminal{In: strings.NewReader(""), Out: &syncBuffer{}})
			if err != nil {
				t.Fatal(err)
			}
			// A disabled GUI must reach the dispatcher, not demand a profile.
			if off[0] {
				if err := app.Run(context.Background(), []string{"gui"}); err == nil || strings.Contains(err.Error(), "the node has several profiles") {
					t.Fatalf("disabled GUI: %v", err)
				}
			}
		})
	}
}

// A profile may turn the UI off: the others show only themselves then.
func TestProfileWithoutTheUI(t *testing.T) {
	s := clitest.Settings(t)
	_, luna := twoProfiles(t, s)
	config.Update(string(luna), func(c *config.Config) error {
		c.Plugins = &config.Plugins{Off: []string{"tui"}}
		return nil
	})
	if _, err := cli.New(s, cli.Terminal{In: strings.NewReader(""), Out: &syncBuffer{}}); err != nil {
		t.Fatal(err)
	}
}
