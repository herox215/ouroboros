package session

import (
	"context"
	"errors"
	"testing"
)

// The node says which image of ouro it runs: the same for each call of one
// process, with the binary it runs, and a front end reads it on the socket.
func TestInstance(t *testing.T) {
	old := executable
	t.Cleanup(func() { executable = old })
	executable = func() (string, error) { return "/new/ouro", nil }
	s := New(nil)
	defer s.Close()
	n := serve(t, Handler(s))
	c := NewClient(n.dir)
	got, err := c.Instance(context.Background())
	if err != nil || got != (Instance{ID: processInstance, Executable: "/new/ouro"}) || got.ID == "" {
		t.Fatalf("%+v %v", got, err)
	}
	if again, err := s.Instance(context.Background()); err != nil || again != got {
		t.Fatalf("again: %+v %v", again, err)
	}
	failing := errors.New("no binary")
	executable = func() (string, error) { return "", failing }
	if _, err := c.Instance(context.Background()); err == nil || err.Error() != failing.Error() {
		t.Fatalf("binary unknown: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Instance(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	n.stop()
	if _, err := c.Instance(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stopped: %v", err)
	}
}
