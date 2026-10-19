package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestActivity(t *testing.T) {
	s := New(nil)
	defer s.Close()
	ctx := context.Background()
	cancel := func() {}
	s.sessions = map[string]*session{
		"private idle title": {title: "secret", events: []Event{{Text: "secret message"}}},
		"idle":               {},
		"working":            {cancel: cancel},
		"also working":       {cancel: cancel},
		"question":           {cancel: cancel, asked: make(chan queued, 1)},
		"task only":          {tasks: []*task{{}}},
	}
	got, err := s.Activity(ctx)
	if err != nil || got != (Activity{Running: 4, Waiting: 1}) {
		t.Fatalf("activity: %+v, %v", got, err)
	}
	wire, _ := json.Marshal(got)
	if strings.Contains(string(wire), "secret") || string(wire) != `{"running":4,"waiting":1}` {
		t.Fatalf("private data: %s", wire)
	}
	// Accepted replies and canceled questions must disappear on the next sample.
	s.sessions["question"].asked = nil
	got, _ = s.Activity(ctx)
	if got.Waiting != 0 {
		t.Fatal(got)
	}
	s.sessions = nil // fabricated sessions must not be closed as live answers
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := s.Activity(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestActivitySocket(t *testing.T) {
	s := New(nil)
	defer s.Close()
	n := serve(t, Handler(s))
	client := NewClient(n.dir)
	got, err := client.Activity(context.Background())
	if err != nil || got != (Activity{}) {
		t.Fatalf("socket: %+v %v", got, err)
	}
	n.stop()
	if _, err := client.Activity(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stopped node: %v", err)
	}
}
