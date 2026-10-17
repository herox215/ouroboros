package node

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/node"
	"ouroboros/internal/session"
	"ouroboros/plugin"
)

// commands returns the commands of the node in dir, started from exe.
func commands(dir home.Dir, exe func() (string, error)) *nodeCommands {
	term := plugin.NewConsole(plugin.Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}})
	return &nodeCommands{term: term, local: node.Local{Home: dir, Exe: exe}, wizard: &wizard{term: term, home: dir, providers: func() []plugin.Provider { return nil }}}
}

// A home whose files cannot be read fails every command, and so does a
// binary that cannot be started.
func TestCommandsReportErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unreadable := commands(home.Dir(filepath.Join(file, "home")), nil)
	ctx := context.Background()
	if err := unreadable.start(ctx, nil); err == nil {
		t.Error("start: want error")
	}
	if err := unreadable.status(ctx, nil); err == nil {
		t.Error("status: want error")
	}

	dir := home.Dir(t.TempDir())
	if err := dir.SaveConfig(config.Config{BaseURL: "http://localhost:1", Model: "gpt", Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	unknown := errors.New("no binary")
	lost := commands(dir, func() (string, error) { return "", unknown })
	if err := lost.start(ctx, nil); !errors.Is(err, unknown) {
		t.Errorf("start without a binary: %v", err)
	}
}

// A node that does not start stops what serve started for it, for every
// profile and once, as a node that ran does: nothing of it goes on after
// serve returned, like a tray that writes to the log.
func TestServeThatFailsStopsWhatItStarted(t *testing.T) {
	dir := home.Dir(t.TempDir()) // not set up
	c := commands(dir, nil)
	c.sessions = session.New(nil)
	var told []string
	tell := func(what string) func() { return func() { told = append(told, what) } }
	c.serving = []func() []plugin.Serving{func() []plugin.Serving { return []plugin.Serving{{Start: tell("start")}} }}
	c.server = node.Server{Home: dir, OnStop: tell("stop first"), Profiles: []node.Server{{Home: dir, OnStop: tell("stop second")}}}
	if err := c.serve(context.Background(), nil); !errors.Is(err, config.ErrIncomplete) {
		t.Fatalf("serve: %v", err)
	}
	if !slices.Equal(told, []string{"start", "stop first", "stop second"}) {
		t.Fatalf("told %v", told)
	}
	// The next serve stops them again: what the commands keep is not changed.
	told = nil
	if err := c.serve(context.Background(), nil); !errors.Is(err, config.ErrIncomplete) || !slices.Equal(told, []string{"start", "stop first", "stop second"}) {
		t.Fatalf("the second time told %v: %v", told, err)
	}
}

// An endpoint without models cannot be set up.
func TestSetupWithoutModels(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer empty.Close()
	w, _ := newWizard(settings{Home: t.TempDir(), BaseURL: empty.URL}, "\n\n")
	if _, err := w.run(context.Background(), config.Config{}); err == nil || !strings.Contains(err.Error(), "no models available") {
		t.Fatalf("got %v", err)
	}
}
