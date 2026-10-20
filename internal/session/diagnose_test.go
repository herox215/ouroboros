package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ouroboros/internal/assistant"
)

// The thread tells what a session does, a tool that runs and a message that
// waits for it, and stops the answer on its way on request: the message is
// answered next.
func TestThreadDiagnoses(t *testing.T) {
	gate := make(chan struct{})
	s := service(t, &fakeAgent{run: func(ctx context.Context, text string, observe func(assistant.Step), _ func(string)) (string, error) {
		if text == "wait" {
			observe(assistant.Step{ID: "c1", Tool: "bash", Args: json.RawMessage(`{"command":"sleep 99"}`)})
			select {
			case <-gate:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "pong:" + text, nil
	}})
	ctx := context.Background()
	th := threading{s}
	if _, err := th.State(ctx, "nope"); err == nil {
		t.Fatal("an unknown session has a state")
	}
	if _, err := th.Stop(ctx, "nope", 0); err == nil {
		t.Fatal("an unknown session was stopped")
	}
	_, s1, _ := s.Watch(ctx, "s1")
	if err := s.Ask(ctx, "s1", "wait"); err != nil {
		t.Fatal(err)
	}
	until(t, s1, Step)
	if _, _, err := th.Send(ctx, "s1", "more"); err != nil {
		t.Fatal(err)
	}
	until(t, s1, Queued)
	got, err := th.State(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"An answer is on its way", "Tool bash runs", "sleep 99", `"more"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("state lacks %q:\n%s", want, got)
		}
	}
	if _, err := th.Stop(ctx, "s1", 3); err == nil {
		t.Fatal("a job that does not run was stopped")
	}
	if _, err := th.Stop(ctx, "s1", 0); err != nil {
		t.Fatal(err)
	}
	until(t, s1, Canceled)
	until(t, s1, Answer)
	got, _ = th.State(ctx, "s1")
	if !strings.Contains(got, "idle") || !strings.Contains(got, "last answer ended") {
		t.Fatalf("state after:\n%s", got)
	}
	if got, _ := th.Stop(ctx, "s1", 0); !strings.HasPrefix(got, "Nothing runs") {
		t.Fatalf("stop when idle: %s", got)
	}
	close(gate)
}

func TestClip(t *testing.T) {
	if got := clip("ääää", 3); got != "ä…" {
		t.Fatalf("clip: %q", got)
	}
	if got := clip("abc", 3); got != "abc" {
		t.Fatalf("clip: %q", got)
	}
}
