package node

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/home"
)

// self is the test binary, which is a node when called with ServeCommand.
func self() (string, error) { return os.Args[0], nil }

// hold makes dir look like the home of a node that runs on port.
func hold(t *testing.T, dir home.Dir, port int) {
	t.Helper()
	release, err := daemon.Hold(dir.Service().PIDFile, port)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
}

// The node is started as a process of its own, found through its PID file
// and stopped again.
func TestStartAndStop(t *testing.T) {
	dir := configured(t)
	t.Setenv(homeEnv, string(dir))
	l := Local{Home: dir, Exe: self}
	t.Cleanup(func() { l.Stop() })

	if _, ok := l.Running(); ok {
		t.Fatal("nothing was started yet")
	}
	p, err := l.Start()
	if err != nil {
		log, _ := os.ReadFile(dir.Service().LogFile)
		t.Fatalf("%v\n%s", err, log)
	}
	if p.PID <= 0 || p.PID == os.Getpid() || p.Port == 0 {
		t.Fatalf("process %+v", p)
	}
	if got, ok := l.Running(); !ok || got != p {
		t.Fatalf("running: %+v, %v, started %+v", got, ok, p)
	}
	if got := fetchCard(t, p.Port); got.Name != "alpha" {
		t.Fatalf("card %+v", got)
	}

	// A node that runs is not started twice.
	if again, err := l.Start(); err != nil || again != p {
		t.Fatalf("second start: %+v, %v", again, err)
	}

	if was, err := l.Stop(); err != nil || !was {
		t.Fatalf("stop: %v, %v", was, err)
	}
	if _, ok := l.Running(); ok {
		t.Fatal("a stopped node should not count as running")
	}
	if was, err := l.Stop(); err != nil || was {
		t.Fatalf("second stop: %v, %v", was, err)
	}
}

func TestStartNeedsSetup(t *testing.T) {
	l := Local{Home: home.Dir(t.TempDir()), Exe: self}
	if _, err := l.Start(); !errors.Is(err, config.ErrIncomplete) {
		t.Fatalf("got %v", err)
	}
	if _, ok := l.Running(); ok {
		t.Fatal("a node that is not set up should not be started")
	}
}

func TestStartReportsErrors(t *testing.T) {
	if _, err := (Local{Home: unreadable(t), Exe: self}).Start(); err == nil {
		t.Error("an unreadable home should fail")
	}

	unknown := Local{Home: configured(t), Exe: func() (string, error) { return "", errTest }}
	if _, err := unknown.Start(); !errors.Is(err, errTest) {
		t.Errorf("unknown binary: got %v", err)
	}

	missing := Local{Home: configured(t), Exe: func() (string, error) { return filepath.Join(t.TempDir(), "ouro"), nil }}
	if _, err := missing.Start(); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing binary: got %v", err)
	}
	if _, ok := missing.Running(); ok {
		t.Error("a node without a binary should not count as running")
	}
}

func TestStatus(t *testing.T) {
	fresh := Local{Home: home.Dir(t.TempDir())}
	if s, err := fresh.Status(); err != nil || s != (Status{}) {
		t.Fatalf("fresh home: %+v, %v", s, err)
	}

	dir := configured(t)
	id, err := dir.Identity()
	if err != nil {
		t.Fatal(err)
	}
	l := Local{Home: dir}
	want := Status{Configured: true, ID: id.ID(), Name: "alpha", Model: "gpt"}
	if s, err := l.Status(); err != nil || s != want {
		t.Fatalf("got %+v, %v, want %+v", s, err, want)
	}

	hold(t, dir, 41337)
	want.Running, want.Port = true, 41337
	if s, err := l.Status(); err != nil || s != want {
		t.Fatalf("running: got %+v, %v, want %+v", s, err, want)
	}

	if _, err := (Local{Home: unreadable(t)}).Status(); err == nil {
		t.Error("an unreadable home should fail")
	}
}

// The client speaks for the node: with its identity and, while it runs, the
// port peers can call back on.
func TestClient(t *testing.T) {
	dir := configured(t)
	l := Local{Home: dir}
	c, err := l.Client()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := dir.Identity()
	if c.Identity.Public() != id.Public() || c.Name != "alpha" || c.Model != "gpt" || c.Port != 0 {
		t.Fatalf("client %+v", c)
	}

	hold(t, dir, 41337)
	if c, err := l.Client(); err != nil || c.Port != 41337 {
		t.Fatalf("running: client %+v, %v", c, err)
	}

	if _, err := (Local{Home: unreadable(t)}).Client(); err == nil {
		t.Error("an unreadable home should fail")
	}
	if _, err := (Local{Home: keyless(t)}).Client(); err == nil {
		t.Error("an invalid identity should fail")
	}
}

func TestExecutable(t *testing.T) {
	want, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	want, _ = filepath.Abs(want)
	if got, err := Executable(); err != nil || got != want {
		t.Fatalf("got %q, %v, want %q", got, err, want)
	}
	// Without Exe the binary that is started is the running one.
	if got, err := (Local{}).executable(); err != nil || got != want {
		t.Fatalf("local: got %q, %v, want %q", got, err, want)
	}

	replace(t, &osExecutable, func() (string, error) { return "", errTest })
	if _, err := Executable(); !errors.Is(err, errTest) {
		t.Fatalf("got %v", err)
	}
}
