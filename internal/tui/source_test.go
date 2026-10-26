package tui

import (
	"context"
	"testing"

	"ouroboros/internal/chat"
	"ouroboros/internal/openai"
	"ouroboros/plugin"

	tea "charm.land/bubbletea/v2"
)

type sourceBackend struct {
	Backend
	asks, steers []context.Context
}

func (b *sourceBackend) Ask(ctx context.Context, _, _ string) error {
	b.asks = append(b.asks, ctx)
	return nil
}
func (b *sourceBackend) Steer(ctx context.Context, _, _ string) error {
	b.steers = append(b.steers, ctx)
	return nil
}

func TestMessageSourcePerSend(t *testing.T) {
	b := &sourceBackend{}
	m := New(b, nil, nil)
	t.Cleanup(m.quit)
	m.status.Configured = true
	m.chat.watching = true // no watch I/O needed for this frontend test
	m.ctx = plugin.WithMessageSource(m.ctx, plugin.MessageSource{Interface: "cli"})
	m.input.SetValue("hello")
	cmd := m.send()
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				c()
			}
		}
	}
	m.input.SetValue("queued")
	m.send()()
	m.chat.Asking = &chat.Question{}
	m.choose("SQLite")()
	if len(b.asks) != 1 || len(b.steers) != 2 {
		t.Fatalf("asks %d steers %d", len(b.asks), len(b.steers))
	}
	for _, ctx := range append(b.asks, b.steers...) {
		if got := openai.MessageSourceFrom(ctx).Interface; got != "tui" {
			t.Fatalf("source = %q", got)
		}
	}
	if got := openai.MessageSourceFrom(m.ctx).Interface; got != "cli" {
		t.Fatalf("base source = %q", got)
	}
}
