package matrix

import (
	"context"
	"testing"

	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

type sourceSessions struct {
	testSessions
	source plugin.MessageSource
}

func (s *sourceSessions) Ask(ctx context.Context, id, text string) error {
	s.source = openai.MessageSourceFrom(ctx)
	return s.testSessions.Ask(ctx, id, text)
}
func (s *sourceSessions) AskImages(ctx context.Context, id, text string, _ []plugin.Image) error {
	return s.Ask(ctx, id, text)
}
func (s *sourceSessions) Steer(ctx context.Context, id, text string) error {
	s.source = openai.MessageSourceFrom(ctx)
	return s.testSessions.Steer(ctx, id, text)
}
func TestMatrixMessageSources(t *testing.T) {
	store, _ := conversationStore(t)
	sessions := &sourceSessions{}
	conn := &outputConnection{output: make(chan string, 8)}
	c, err := openConversations(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}
	for _, operation := range []string{"text", "image", "reply", "restored"} {
		msg.Images = nil
		sessions.set()
		switch operation {
		case "image":
			msg.Images = []plugin.Image{{Type: "image/png", Data: []byte("picture")}}
		case "reply":
			sessions.set(plugin.SessionEvent{Kind: plugin.SessionAsked})
		case "restored":
			c, err = openConversations(store, sessions)
			if err != nil {
				t.Fatal(err)
			}
		}
		sessions.source = plugin.MessageSource{}
		if err := c.receive(t.Context(), conn, msg); err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		if want := (plugin.MessageSource{Interface: "api", Gateway: "matrix"}); sessions.source != want {
			t.Fatalf("%s source: %+v, want %+v", operation, sessions.source, want)
		}
	}
}
