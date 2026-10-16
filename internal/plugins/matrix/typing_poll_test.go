package matrix

import (
	"context"
	"ouroboros/plugin"
	"testing"
	"time"
)

type pollTypingConnection struct {
	fakeConnection
	states chan bool
}

func (c *pollTypingConnection) TypingTo(ctx context.Context, _, _ string, active bool) error {
	select {
	case c.states <- active:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestPollTypingLifecycle(t *testing.T) {
	s := store{t.TempDir()}
	cfg := configuration{Enabled: true, Token: "token", Homeserver: "https://example.org", UserID: "@bot:example.org", AllowedUsers: []string{"@owner:example.org"}}
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	sessions := &testSessions{}
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	conn := &pollTypingConnection{states: make(chan bool, 8)}
	if err = c.receive(context.Background(), conn, incoming{RoomID: "!room:test", Sender: "@owner:example.org", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	set := func(events ...plugin.SessionEvent) {
		sessions.mu.Lock()
		sessions.events = events
		sessions.mu.Unlock()
	}
	expect := func(want bool) {
		t.Helper()
		select {
		case got := <-conn.states:
			if got != want {
				t.Fatalf("typing = %v, want %v", got, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("missing typing = %v", want)
		}
	}
	msg := plugin.SessionEvent{Kind: plugin.SessionMessage}
	set(msg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.poll(ctx, conn) }()
	defer func() { cancel(); <-done }()
	expect(true)
	set(msg, plugin.SessionEvent{Kind: plugin.SessionAsked})
	expect(false)
	set(msg, plugin.SessionEvent{Kind: plugin.SessionAsked, Done: true})
	expect(true)
	set(msg, plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "done"})
	expect(false)
	set(msg, plugin.SessionEvent{Kind: plugin.SessionAnswer}, msg)
	expect(true)
	cancel()
	expect(false)
}
