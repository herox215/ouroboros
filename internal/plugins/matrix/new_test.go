package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"ouroboros/plugin"
)

// A room talks in the thread, from its first message on, without what the
// thread said before; /new starts nothing, as there is one conversation, and
// says so.
func TestRoomsTalkInTheThread(t *testing.T) {
	s, cfg := conversationStore(t)
	sessions := &testSessions{}
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionAnswer, PresentationID: "earlier", Text: "said in the terminal"})
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	conn := &outputConnection{output: make(chan string, 8)}
	ctx := context.Background()
	msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}
	key := account(cfg) + "\n" + msg.Sender + "\n" + msg.RoomID
	if err := c.receive(ctx, conn, msg); err != nil {
		t.Fatal(err)
	}
	if entry := c.entries[key]; entry.Session != plugin.ThreadID || !entry.Sent["id:earlier"] || sessions.created != 0 || sessions.asks != 1 {
		t.Fatalf("room %+v, %d sessions, %d asks", entry, sessions.created, sessions.asks)
	}
	msg.Text = " /new "
	if err := c.receive(ctx, conn, msg); err != nil {
		t.Fatal(err)
	}
	if text := <-conn.output; text != "There is one conversation here, about everything: just write, and Ouro keeps the topics apart." {
		t.Fatalf("said %q", text)
	}
	if sessions.created != 0 || sessions.cancels != 0 || sessions.asks != 1 || c.entries[key].Session != plugin.ThreadID {
		t.Fatal("/new started something")
	}
	msg.Sender, msg.Text = "@stranger:example.org", "/new"
	if err := c.receive(ctx, conn, msg); !errors.Is(err, plugin.ErrGatewayDenied) {
		t.Fatalf("a stranger: %v", err)
	}
	// What comes in the thread is sent to the room, what was there before not.
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionAnswer, PresentationID: "earlier", Text: "said in the terminal"},
		plugin.SessionEvent{Kind: plugin.SessionAnswer, PresentationID: "now", Text: "hi"})
	if text := polled(t, c, conn); text != "hi" {
		t.Fatalf("sent %q", text)
	}
}

// A room that talked in a session of its own talks in the thread once the
// gateway starts anew, and is not sent what the thread said before; the
// session stays in Ouro.
func TestRoomsMoveToTheThread(t *testing.T) {
	s, cfg := conversationStore(t)
	who := plugin.GatewayIdentity{Gateway: account(cfg), Subject: "@owner:example.org\n!room:example.org"}
	old := map[string]*conversation{who.Gateway + "\n" + who.Subject: {Session: "gateway-session-1", Identity: who, Room: "!room:example.org", Sent: map[string]bool{"id:old": true}, SentVersion: 1}}
	data, _ := json.Marshal(old)
	if err := os.WriteFile(s.path("conversations.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	sessions := &testSessions{}
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionAnswer, PresentationID: "earlier", Text: "said in the terminal"})
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	entry := c.entries[who.Gateway+"\n"+who.Subject]
	if entry.Session != plugin.ThreadID || entry.SentVersion != 0 {
		t.Fatalf("room %+v", entry)
	}
	conn := &outputConnection{output: make(chan string, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.poll(ctx, conn) }()
	time.Sleep(1500 * time.Millisecond) // a round of the poll, which takes the thread as sent
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionAnswer, PresentationID: "earlier", Text: "said in the terminal"},
		plugin.SessionEvent{Kind: plugin.SessionAnswer, PresentationID: "now", Text: "hi"})
	select {
	case text := <-conn.output:
		if text != "hi" {
			t.Fatalf("sent %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing sent")
	}
	cancel()
	<-done
	if sessions.created != 0 {
		t.Fatal("a session was opened")
	}
}

// polled runs one round of the poll of c and returns what it sent.
func polled(t *testing.T, c *conversations, conn *outputConnection) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.poll(ctx, conn) }()
	defer func() { cancel(); <-done }()
	select {
	case text := <-conn.output:
		return text
	case <-time.After(5 * time.Second):
		t.Fatal("nothing sent")
		return ""
	}
}

// A room that cannot read the thread does not talk in it yet; one kept
// without what was sent to it is sent what comes.
func TestRoomsAndTheThreadTheyCannotRead(t *testing.T) {
	s, cfg := conversationStore(t)
	sessions := &testSessions{}
	unavailable := errors.New("thread unavailable")
	sessions.mu.Lock()
	sessions.watchErr = unavailable
	sessions.mu.Unlock()
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	conn := &outputConnection{output: make(chan string, 8)}
	msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}
	if err := c.receive(context.Background(), conn, msg); !errors.Is(err, unavailable) || len(c.entries) != 0 || sessions.asks != 0 {
		t.Fatalf("received without the thread: %v, %d rooms, %d asks", err, len(c.entries), sessions.asks)
	}
	who := plugin.GatewayIdentity{Gateway: account(cfg), Subject: msg.Sender + "\n" + msg.RoomID}
	data, _ := json.Marshal(map[string]*conversation{who.Gateway + "\n" + who.Subject: {Session: plugin.ThreadID, Identity: who, Room: msg.RoomID, SentVersion: 1}})
	if err := os.WriteFile(s.path("conversations.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	sessions.mu.Lock()
	sessions.watchErr = nil
	sessions.mu.Unlock()
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionAnswer, PresentationID: "now", Text: "hi"})
	if c, err = openConversations(s, sessions); err != nil {
		t.Fatal(err)
	}
	if text := polled(t, c, conn); text != "hi" {
		t.Fatalf("sent %q", text)
	}
}
