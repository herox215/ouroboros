package session

import (
	"context"
	"errors"
	"testing"
)

func TestRunningSessionsSocket(t *testing.T) {
	s := New(nil)
	defer s.Close()
	s.sessions = map[string]*session{"active": {title: "Tray test session", cancel: func() {}}}
	defer func() { s.sessions = nil }()
	n := serve(t, Handler(s))
	c := NewClient(n.dir)
	got, err := c.RunningSessions(context.Background())
	if err != nil || len(got) != 1 || got[0] != "Tray test session" {
		t.Fatalf("%v %v", got, err)
	}
	n.stop()
	if _, err := c.RunningSessions(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatal(err)
	}
}
