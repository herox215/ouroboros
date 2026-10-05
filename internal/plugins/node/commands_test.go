package node

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/node"
	"ouroboros/plugin"
)

// commands returns the commands of the node in dir, started from exe.
func commands(dir home.Dir, exe func() (string, error)) *nodeCommands {
	term := plugin.NewConsole(plugin.Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}})
	return &nodeCommands{term: term, local: node.Local{Home: dir, Exe: exe}, wizard: &wizard{term: term, home: dir}}
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
