package matrix

import (
	"context"
	"errors"
	"os"
	"testing"

	"ouroboros/plugin"
)

func TestNewChat(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "first", true: "existing"}[existing], func(t *testing.T) {
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
			conn := &outputConnection{output: make(chan string, 8)}
			ctx := context.Background()
			msg := incoming{RoomID: "!room:test", Sender: "@owner:example.org", Text: "hello"}
			key := account(cfg) + "\n" + msg.Sender + "\n" + msg.RoomID
			old := ""
			if existing {
				if err := c.receive(ctx, conn, msg); err != nil {
					t.Fatal(err)
				}
				old = c.entries[key].Session
				c.entries[key].Sent = map[string]bool{"old": true}
			}
			msg.Text = " /new "
			if err := c.receive(ctx, conn, msg); err != nil {
				t.Fatal(err)
			}
			select {
			case <-conn.output:
			default:
				t.Fatal("missing confirmation")
			}
			entry := c.entries[key]
			if entry.Session == old || len(entry.Sent) != 0 {
				t.Fatal("did not reset conversation")
			}
			want := 1
			if existing {
				want = 2
			}
			if sessions.created != want || sessions.cancels != want-1 || sessions.asks != want-1 {
				t.Fatal("wrong session lifecycle")
			}
			restored, err := openConversations(s, sessions)
			if err != nil {
				t.Fatal(err)
			}
			if restored.entries[key].Session != entry.Session {
				t.Fatal("new binding not persisted")
			}
			msg.Text = "next message"
			if err := restored.receive(ctx, conn, msg); err != nil {
				t.Fatal(err)
			}
			if sessions.created != want || sessions.asks != want {
				t.Fatal("next message did not reuse new session")
			}
			msg.Sender = "@stranger:example.org"
			msg.Text = "/new"
			if err := restored.receive(ctx, conn, msg); !errors.Is(err, plugin.ErrGatewayDenied) {
				t.Fatalf("unauthorized new: %v", err)
			}
			if sessions.created != want {
				t.Fatal("unauthorized session created")
			}
			// A failed persistence step restores the old in-memory room binding.
			saved := *entry
			if err := os.Remove(s.path("conversations.json")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(s.path("conversations.json"), 0700); err != nil {
				t.Fatal(err)
			}
			msg.Sender = "@owner:example.org"
			if err := c.receive(ctx, conn, msg); err == nil {
				t.Fatal("expected persistence failure")
			}
			if c.entries[key].Session != saved.Session {
				t.Fatal("lost old binding after failed save")
			}
		})
	}
}
