package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"ouroboros/plugin"
)

func TestQuickAnswers(t *testing.T) {
	question := plugin.SessionEvent{Kind: plugin.SessionAsked, Args: json.RawMessage(`{"question":"Which database?","options":["SQLite","Postgres"]}`)}
	for _, tc := range []struct {
		name, text, want string
		events           []plugin.SessionEvent
		reply            bool
	}{
		{"plain answer", "Postgres", "Postgres", []plugin.SessionEvent{question}, true},
		{"number", " 2 ", "Postgres", []plugin.SessionEvent{question}, true},
		{"first option", "1", "SQLite", []plugin.SessionEvent{question}, true},
		{"free text", "Something else", "Something else", []plugin.SessionEvent{question}, true},
		{"out of range stays text", "3", "3", []plugin.SessionEvent{question}, true},
		{"zero stays text", "0", "0", []plugin.SessionEvent{question}, true},
		{"explicit number", "/answer 2", "Postgres", []plugin.SessionEvent{question}, true},
		{"ordinary message", "2", "", nil, false},
		{"answered question", "2", "", []plugin.SessionEvent{question, {Kind: plugin.SessionAsked, Done: true}}, false},
		{"canceled question", "2", "", []plugin.SessionEvent{question, {Kind: plugin.SessionCanceled}}, false},
		{"finished answer", "2", "", []plugin.SessionEvent{question, {Kind: plugin.SessionAnswer}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := conversationStore(t)
			sessions := &testSessions{}
			c, err := openConversations(s, sessions)
			if err != nil {
				t.Fatal(err)
			}
			conn := &recordingConnection{}
			msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}
			if err := c.receive(context.Background(), conn, msg); err != nil {
				t.Fatal(err)
			}
			// Pending questions must also work after restoring the room binding.
			c, err = openConversations(s, sessions)
			if err != nil {
				t.Fatal(err)
			}
			sessions.set(tc.events...)
			msg.Text = tc.text
			if err := c.receive(context.Background(), conn, msg); err != nil {
				t.Fatal(err)
			}
			wantReplies, wantAsks := 0, 2
			if tc.reply {
				wantReplies, wantAsks = 1, 1
			}
			if sessions.replies != wantReplies || sessions.asks != wantAsks || sessions.replied != tc.want {
				t.Fatalf("replies=%d asks=%d text=%q; want %d, %d, %q", sessions.replies, sessions.asks, sessions.replied, wantReplies, wantAsks, tc.want)
			}
			// An unavailable snapshot must not turn a possible answer into a new task.
			unavailable := errors.New("unavailable")
			sessions.watchErr = unavailable
			if err := c.receive(context.Background(), conn, msg); !errors.Is(err, unavailable) {
				t.Fatalf("poll failure: %v", err)
			}
			if sessions.replies != wantReplies || sessions.asks != wantAsks {
				t.Fatal("routed despite poll failure")
			}
		})
	}
}
