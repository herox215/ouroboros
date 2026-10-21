package matrix

import (
	"context"
	"encoding/json"
	"testing"

	"ouroboros/plugin"
)

type replyImageSessions struct {
	testSessions
	text   string
	images []plugin.Image
}

func (s *replyImageSessions) SteerImages(_ context.Context, _, text string, images []plugin.Image) error {
	s.text, s.images = text, images
	return nil
}

func TestPendingQuestionAcceptsPicturesAndTopicChanges(t *testing.T) {
	for _, text := range []string{"", "Look at this instead", "  new topic\nlook here  ", "Allow once"} {
		t.Run(text, func(t *testing.T) {
			s, _ := conversationStore(t)
			sessions := &replyImageSessions{}
			c, err := openConversations(s, sessions)
			if err != nil {
				t.Fatal(err)
			}
			conn := &recordingConnection{}
			msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}
			if err := c.receive(t.Context(), conn, msg); err != nil {
				t.Fatal(err)
			}
			sessions.set(plugin.SessionEvent{Kind: plugin.SessionAsked, Args: json.RawMessage(`{"question":"Continue?","options":["Allow once","No"]}`)})
			c, err = openConversations(s, sessions)
			if err != nil {
				t.Fatal(err)
			}
			msg.Text = text
			msg.Images = []plugin.Image{{Type: "image/png", Data: []byte("picture")}}
			if err := c.receive(t.Context(), conn, msg); err != nil {
				t.Fatal(err)
			}
			if sessions.text != text || len(sessions.images) != 1 || string(sessions.images[0].Data) != "picture" || sessions.replies != 0 || len(conn.messages()) != 0 {
				t.Fatalf("text=%q images=%v replies=%d messages=%v", sessions.text, sessions.images, sessions.replies, conn.messages())
			}
		})
	}
}
