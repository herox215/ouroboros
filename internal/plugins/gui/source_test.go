package gui

import (
	"context"
	"testing"

	"ouroboros/internal/openai"
	"ouroboros/plugin"
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
		ask: func(ctx context.Context, _, _ string) error {
			check(ctx, "gui")
			asks++
			if asks == 2 {
				return plugin.ErrBusy
			}
			return nil
		},
		steer: func(ctx context.Context, _, _ string) error { check(ctx, "gui"); steers++; return nil },
	}
	w := sessionWindow(t, s)
	id, err := w.NewSession(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"hello", "answer the question"} {
		if err := w.SendMessage(0, id, text); err != nil {
			t.Fatal(err)
		}
	}
	check(w.ctx, "")
	if asks != 2 || steers != 1 {
		t.Fatalf("asks %d steers %d", asks, steers)
	}
}
