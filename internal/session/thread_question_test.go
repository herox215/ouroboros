package session

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
)

// The thread itself asks the owner too: a command it runs on its own, like a
// change of the model, needs their consent, and what they answer in the
// thread is its answer, not a handoff to a session.
func TestThreadAsksItsOwnQuestion(t *testing.T) {
	asks := json.RawMessage(`{"question":"Change the model to new?","options":["Apply change","Reject change"],"recommended":"Apply change"}`)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if assistant.ThreadOf(ctx) == nil {
			return "pong:" + text, nil
		}
		answer, err := agent.AskOwner(ctx, asks)
		return "model: " + answer, err
	}}
	s := service(t, a)
	ctx := context.Background()
	_, thread, _ := s.Watch(ctx, ThreadID)
	if err := s.Ask(ctx, ThreadID, "take the new model"); err != nil {
		t.Fatal(err)
	}
	if got := told(until(t, thread, Asked)); !slices.Contains(got, "asked Change the model to new?") {
		t.Fatalf("got %q", got)
	}
	if err := s.Steer(WithMessageSource(ctx, MessageSource{Interface: "tui"}), ThreadID, "Apply change"); err != nil {
		t.Fatal(err)
	}
	if got := told(until(t, thread, Answer)); !same(got, "answered Apply change", "answer model: Apply change") {
		t.Fatalf("got %q", got)
	}
}
