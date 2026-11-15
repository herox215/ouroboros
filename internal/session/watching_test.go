package session

import (
	"context"
	"testing"
	"time"
)

// A watch says which front end it belongs to, and a node tells from it whether
// a window of ouro shows the thread: that is what a picture needs to be shown
// in one.
func TestWatchingTellsTheFrontEndsApart(t *testing.T) {
	s := service(t, &fakeAgent{})
	ctx, cancel := context.WithCancel(context.Background())
	if _, _, err := s.Watch(WithWatchSource(ctx, SourceWindow), ThreadID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Watch(WithWatchSource(ctx, SourceWindow), ThreadID); err != nil {
		t.Fatal(err)
	}
	// The page of the thread, and a front end that does not say, are no window.
	if _, _, err := s.Watch(WithWatchSource(ctx, SourcePage), ThreadID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Watch(ctx, ThreadID); err != nil {
		t.Fatal(err)
	}
	if got := s.Watching(ThreadID, SourceWindow); got != 2 {
		t.Fatalf("the node counts %d windows, not 2", got)
	}
	if got := s.Watching(ThreadID, SourcePage); got != 1 {
		t.Fatalf("the node counts %d pages, not 1", got)
	}
	if got := s.Watching(ThreadID, ""); got != 1 {
		t.Fatalf("the node counts %d front ends that do not say, not 1", got)
	}
	if got := s.Watching("elsewhere", SourceWindow); got != 0 {
		t.Fatalf("a session nobody watches counts %d windows", got)
	}
	if got := s.Watching("no such id", SourceWindow); got != 0 {
		t.Fatalf("an id that is none counts %d windows", got)
	}
	cancel()
	// The watches end with their context, and the window is closed. The
	// service hears of it just after the context ended.
	deadline := time.Now().Add(2 * time.Second)
	for s.Watching(ThreadID, SourceWindow) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("a window that closed is still counted: %d", s.Watching(ThreadID, SourceWindow))
		}
		time.Sleep(time.Millisecond)
	}
}

// A front end that does not say which one watches is one that shows no
// picture: the source of a watch is nothing unless it is named.
func TestWatchSourceOfAPlainContext(t *testing.T) {
	if got := WatchSource(context.Background()); got != "" {
		t.Fatalf("a plain context watches as %q", got)
	}
}
