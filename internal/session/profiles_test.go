package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"ouroboros/internal/assistant"
)

// The Services of the profiles of a node share its process: an answer of one
// keeps it running for all, a new build that one asks for waits for the
// answers of the others, which take no new message meanwhile, and a release
// of one stops it once none answers.
func TestShare(t *testing.T) {
	entered, finish := make(chan struct{}, 1), make(chan struct{})
	other := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		entered <- struct{}{}
		<-finish
		return "done", nil
	}})
	requester := service(t, &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if text == "rebuild" {
			AskRestart(ctx, "new-build")
		}
		return "rebuilt", nil
	}})
	Share(requester, other)
	replaced, stops := make(chan string, 1), make(chan struct{}, 1)
	other.Attach(func() { stops <- struct{}{} }, func(exe string) { replaced <- exe })

	// A release waits for the answer of the other profile.
	ctx := context.Background()
	other.Ask(ctx, "other", "work")
	<-entered
	if requester.Releases() {
		t.Fatal("a release of one profile stops the answer of another")
	}
	close(finish)
	awaitStop(t, stops)

	finish = make(chan struct{})
	other.Ask(ctx, "other", "more work")
	<-entered
	_, live, _ := requester.Watch(ctx, "mine")
	requester.Ask(ctx, "mine", "rebuild")
	until(t, live, Restart)
	if err := other.Ask(ctx, "new", "new work"); !errors.Is(err, ErrRestarting) {
		t.Fatalf("new work during the replacement: %v", err)
	}
	// What is said to the answer on its way is not queued for a handover:
	// the queue of the answer would be dropped with the process, so it is
	// refused instead of lost.
	if err := other.Steer(ctx, "other", "late during the replacement"); !errors.Is(err, ErrRestarting) {
		t.Fatalf("queued during the replacement instead of refused: %v", err)
	}
	if text, err := other.Unsteer(ctx, "other"); text != "" || err != nil {
		t.Fatalf("unsteer during the replacement: %q, %v", text, err)
	}
	select {
	case <-replaced:
		t.Fatal("the answer of the other profile was cut off")
	default:
	}
	close(finish)
	select {
	case exe := <-replaced:
		if exe != "new-build" {
			t.Fatal(exe)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the node was not replaced")
	}
}
