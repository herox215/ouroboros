package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
)

// homeEnv tells the test binary which home to serve when it runs as the node.
const homeEnv = "OURO_NODE_TEST_HOME"

// The test binary is also the node of these tests: called with ServeCommand,
// as Local.Start calls the real binary, it serves the home named by homeEnv.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == ServeCommand {
		os.Exit(fakeNode(home.Dir(os.Getenv(homeEnv))))
	}
	os.Exit(m.Run())
}

// fakeNode serves dir until it is asked to stop.
func fakeNode(dir home.Dir) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second) // never outlives a test by much
	defer cancel()
	s := testServer(dir)
	s.Log = os.Stderr // the log file of the node
	if err := s.Run(ctx); err != nil {
		fmt.Println(err)
		return 1
	}
	return 0
}

// testServer is a node on free ports that answers no messages.
func testServer(dir home.Dir) Server {
	return Server{
		Home:      dir,
		Version:   "test",
		Log:       io.Discard,
		Responder: func(func(string, ...any)) (a2a.Responder, error) { return nil, nil },
	}
}

// configured returns the home of a node that was set up.
func configured(t *testing.T) home.Dir {
	t.Helper()
	dir := home.Dir(t.TempDir())
	if err := dir.SaveConfig(config.Config{BaseURL: "http://localhost:1", Model: "gpt", Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// unreadable returns a home whose configuration cannot be read: it is below
// a file.
func unreadable(t *testing.T) home.Dir {
	t.Helper()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return home.Dir(filepath.Join(file, "home"))
}

// keyless returns the home of a node whose key is not one.
func keyless(t *testing.T) home.Dir {
	t.Helper()
	dir := configured(t)
	cfg, _ := dir.Config()
	cfg.PrivateKey = "nonsense"
	if err := dir.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return dir
}

// replace sets a variable of the package for the duration of the test.
func replace[T any](t *testing.T, target *T, value T) {
	t.Helper()
	old := *target
	*target = value
	t.Cleanup(func() { *target = old })
}

var errTest = errors.New("test error")
