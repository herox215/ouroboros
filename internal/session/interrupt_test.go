package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
)

// An answer the node went away with goes on in the next image, once, told
// that it was stopped in the middle; one the owner stopped does not.
func TestInterruptedAnswerGoesOnInTheNextImage(t *testing.T) {
	dir := t.TempDir()
	s := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}})
	s.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })
	builder := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		AskRestart(ctx, "/new/ouro")
		return "built", nil
	}})
	Share(s, builder)
	s.node.restartWait = 30 * time.Millisecond
	replaced := make(chan string, 1)
	builder.Attach(nil, func(exe string) { replaced <- exe })
	if err := s.Ask(context.Background(), "work", "long work"); err != nil {
		t.Fatal(err)
	}
	if err := builder.Ask(context.Background(), "builder", "build"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-replaced:
	case <-time.After(5 * time.Second):
		t.Fatal("the node was not replaced")
	}
	s.answers.Wait()

	told := make(chan string, 4)
	next := service(t, &fakeAgent{run: func(_ context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		told <- text
		return "went on", nil
	}})
	next.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })
	next.GoOn()
	select {
	case got := <-told:
		if !strings.Contains(got, interruptedText) {
			t.Fatalf("told %q", got)
		}
		for _, want := range []string{`This chat is "work" in the history archive`, "\n\nNothing of this chat is archived: what the answer was asked and did is not in history_read."} {
			if !strings.Contains(got, want) {
				t.Errorf("told %q, want %q", got, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the interrupted answer did not go on")
	}
	// The mark of the interruption is taken back when the answer ends: wait
	// for that, or the second GoOn still finds the session interrupted.
	next.answers.Wait()
	next.GoOn()
	select {
	case got := <-told:
		t.Fatalf("it went on twice: %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestStoppedAnswerDoesNotGoOn(t *testing.T) {
	dir := t.TempDir()
	s := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}})
	s.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })
	if err := s.Ask(context.Background(), "work", "long work"); err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(context.Background(), "work"); err != nil {
		t.Fatal(err)
	}
	s.answers.Wait()

	told := make(chan string, 1)
	next := service(t, &fakeAgent{run: func(_ context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		told <- text
		return "answered", nil
	}})
	next.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })
	next.GoOn()
	select {
	case got := <-told:
		t.Fatalf("a stopped answer went on: %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}

// A scheduled job is not brought on: its conversation has nobody to report
// to, and the next run begins one of its own.
func TestScheduledAnswerIsNotInterrupted(t *testing.T) {
	dir := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	s := service(t, &fakeAgent{run: func(_ context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return "done", nil
	}})
	s.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })
	if _, _, err := s.RunScheduled(context.Background(), "daily", "work", dir, false); err != nil {
		t.Fatal(err)
	}
	<-started // the mark is written before the answer runs
	told := make(chan string, 1)
	next := service(t, &fakeAgent{run: func(_ context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		told <- text
		return "answered", nil
	}})
	next.Archive(history.Open(dir), func(string, ...any) {}, func(string) string { return "" })
	next.GoOn()
	close(release)
	select {
	case got := <-told:
		t.Fatalf("a scheduled answer went on: %q", got)
	case <-time.After(200 * time.Millisecond):
	}
}
