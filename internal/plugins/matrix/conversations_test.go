package matrix

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ouroboros/plugin"
)

type testSessions struct {
	plugin.Sessions
	mu                              sync.Mutex
	created, asks, replies, cancels int
	events                          []plugin.SessionEvent
}

func (s *testSessions) New(context.Context) (string, error) {
	s.created++
	return "gateway-session-1", nil
}
func (s *testSessions) Ask(context.Context, string, string) error   { s.asks++; return nil }
func (s *testSessions) Steer(context.Context, string, string) error { s.replies++; return nil }
func (s *testSessions) Cancel(context.Context, string) error        { s.cancels++; return nil }
func (s *testSessions) Watch(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]plugin.SessionEvent(nil), s.events...), nil, nil
}

type outputConnection struct {
	fakeConnection
	output chan string
}

func (o *outputConnection) Send(ctx context.Context, _ string, text string) error {
	select {
	case o.output <- text:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *outputConnection) SendTo(ctx context.Context, room, peer, text string) error {
	if peer != "@owner:example.org" {
		return errors.New("wrong recipient")
	}
	return o.Send(ctx, room, text)
}

func TestConversationAuthorizationPersistenceAndPresentation(t *testing.T) {
	s := store{t.TempDir()}
	cfg := configuration{Enabled: true, Token: "token", Homeserver: "https://example.org", UserID: "@bot:example.org", AllowedUsers: []string{"@owner:example.org"}, Revision: "1"}
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	sessions := &testSessions{}
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	conn := &outputConnection{output: make(chan string, 8)}
	msg := incoming{RoomID: "!room:example.org", Sender: "@stranger:example.org", Text: "must not run"}
	if err = c.receive(context.Background(), conn, msg); !errors.Is(err, plugin.ErrGatewayDenied) {
		t.Fatalf("unauthorized: %v", err)
	}
	if sessions.created != 0 {
		t.Fatal("unauthorized user created a session")
	}
	msg.Sender = "@owner:example.org"
	msg.Text = "hello"
	if err = c.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	restored, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	if sessions.created != 1 || sessions.asks != 2 {
		t.Fatal("did not resume profile conversation")
	}
	sessions.mu.Lock()
	sessions.events = []plugin.SessionEvent{{Kind: plugin.SessionAsked}}
	sessions.mu.Unlock()
	msg.Text = "/answer yes"
	if err = restored.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	msg.Text = "/cancel"
	if err = restored.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	if sessions.replies != 1 || sessions.cancels != 1 {
		t.Fatal("commands not routed")
	}
	sessions.mu.Lock()
	sessions.events = []plugin.SessionEvent{{Kind: plugin.SessionThought, Text: "private thought"}, {Kind: plugin.SessionAnswer, Text: "visible answer"}}
	sessions.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); restored.poll(ctx, conn) }()
	select {
	case text := <-conn.output:
		if text != "visible answer" {
			t.Fatalf("leaked presentation %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no answer published")
	}
	cancel()
	<-done
	// Snapshot metadata can move as answers are appended. Restoring must not
	// resend the first answer or skip the new one.
	again, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	sessions.mu.Lock()
	sessions.events = append(sessions.events, plugin.SessionEvent{Kind: plugin.SessionEffort, Text: "high"}, plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "second answer"})
	sessions.mu.Unlock()
	ctx2, stop2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { defer close(done2); again.poll(ctx2, conn) }()
	select {
	case text := <-conn.output:
		if text != "second answer" {
			t.Fatalf("replayed or skipped event: %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("new answer missing")
	}
	stop2()
	<-done2
	// Revocation applies even to a previously bound session.
	cfg.AllowedUsers = nil
	if err = s.save(cfg); err != nil {
		t.Fatal(err)
	}
	msg.Text = "must not run"
	if err = restored.receive(context.Background(), conn, msg); !errors.Is(err, plugin.ErrGatewayDenied) {
		t.Fatalf("revocation: %v", err)
	}
}
