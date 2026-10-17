package matrix

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"ouroboros/plugin"
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

func TestPollRetriesWhatItCouldNotSendOrSave(t *testing.T) {
	s, _ := conversationStore(t)
	sessions := &testSessions{}
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.receive(context.Background(), &recordingConnection{}, incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionSay, Text: "first"}, plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "second"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The first attempt fails, the second is sent but cannot be saved, and
	// from the third on everything works. Each failure waits for the next
	// round: nothing is sent out of order.
	calls := 0
	conn := &recordingConnection{fail: []error{errors.New("homeserver down")}}
	conn.sending = func() {
		calls++
		switch calls {
		case 2:
			os.Remove(s.path("conversations.json"))
			if err := os.Mkdir(s.path("conversations.json"), 0700); err != nil {
				t.Error(err)
			}
		case 3:
			os.Remove(s.path("conversations.json"))
		case 4:
			cancel()
		}
	}
	done := make(chan struct{})
	go func() { defer close(done); c.poll(ctx, conn) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the messages were not sent again")
	}
	first, second := "!room:example.org @owner:example.org first", "!room:example.org @owner:example.org second"
	if sent := conn.messages(); !slices.Equal(sent, []string{first, first, second}) || calls != 4 {
		t.Fatalf("sent %q in %d attempts", sent, calls)
	}
	// What was sent and saved stays sent after a restart.
	again, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range again.entries {
		if len(entry.Sent) != 2 {
			t.Fatalf("remembered %d events as sent", len(entry.Sent))
		}
	}
}

func TestPollStopsTypingWhenTheSessionIsGone(t *testing.T) {
	s, _ := conversationStore(t)
	sessions := &testSessions{}
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	conn := &pollTypingConnection{states: make(chan bool, 8)}
	if err = c.receive(context.Background(), conn, incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionMessage})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.poll(ctx, conn) }()
	expect := func(want bool) {
		t.Helper()
		select {
		case got := <-conn.states:
			if got != want {
				t.Fatalf("typing = %v, want %v", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("missing typing = %v", want)
		}
	}
	expect(true)
	sessions.mu.Lock()
	sessions.watchErr = errors.New("session gone")
	sessions.mu.Unlock()
	expect(false)
	cancel()
	<-done
	select {
	case got := <-conn.states:
		t.Fatalf("typing = %v after it stopped", got)
	default:
	}
}

func TestPollLooksAtEveryConversation(t *testing.T) {
	s, _ := conversationStore(t)
	sessions := &testSessions{}
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	conn := &pollTypingConnection{states: make(chan bool, 8)}
	rooms := []string{"!a:example.org", "!b:example.org", "!c:example.org", "!d:example.org"}
	for _, room := range rooms {
		if err = c.receive(context.Background(), conn, incoming{RoomID: room, Sender: "@owner:example.org", Text: "hello"}); err != nil {
			t.Fatal(err)
		}
	}
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionMessage})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.poll(ctx, conn) }()
	defer func() { cancel(); <-done }()
	next := func(want bool) {
		t.Helper()
		select {
		case got := <-conn.states:
			if got != want {
				t.Fatalf("typing = %v, want %v", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("missing typing = %v", want)
		}
	}
	for range rooms {
		next(true)
	}
	// A conversation whose session is gone does not keep the others waiting:
	// one round turns typing off in all of them.
	sessions.mu.Lock()
	sessions.watchErr = errors.New("sessions gone")
	sessions.mu.Unlock()
	next(false)
	c.mu.Lock() // the round holds the conversations until it is done
	rest := len(conn.states)
	c.mu.Unlock()
	if rest != len(rooms)-1 {
		t.Fatalf("one round turned typing off in %d of %d conversations", rest+1, len(rooms))
	}
}
