package gui

import (
	"context"
	"testing"

	"ouroboros/internal/chat"
	"ouroboros/internal/openai"
)

func TestMessageSourcePerSend(t *testing.T) {
	var asks, steers int
	check := func(ctx context.Context, want string) {
		t.Helper()
		if got := openai.MessageSourceFrom(ctx).Interface; got != want {
			t.Fatalf("source = %q, want %q", got, want)
		}
	}
	s := &testSessions{
		open:       func(ctx context.Context) (bool, error) { check(ctx, ""); return false, nil },
		newSession: func(ctx context.Context) (string, error) { check(ctx, ""); return "chat", nil },
		ask:        func(ctx context.Context, _, _ string) error { check(ctx, "gui"); asks++; return nil },
		steer:      func(ctx context.Context, _, _ string) error { check(ctx, "gui"); steers++; return nil },
	}
	w := sessionWindow(t, s)
	id, err := w.NewSession(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, way := range []chat.Way{chat.Ask, chat.Steer, chat.Answer} {
		if err := w.Deliver(0, id, "hello", way); err != nil {
			t.Fatal(err)
		}
	}
	check(w.ctx, "")
	if asks != 1 || steers != 2 {
		t.Fatalf("asks %d steers %d", asks, steers)
	}
}
