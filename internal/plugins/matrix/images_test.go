package matrix

import (
	"bytes"
	"context"
	"ouroboros/plugin"
	"testing"
)

type imageSessions struct {
	testSessions
	images []plugin.Image
	text   string
}

func (s *imageSessions) AskImages(_ context.Context, _, text string, images []plugin.Image) error {
	s.text, s.images = text, images
	return nil
}
func TestConversationForwardsImagesAndCaption(t *testing.T) {
	s := store{t.TempDir()}
	if err := s.save(configuration{Enabled: true, Token: "token", Homeserver: "https://example.org", UserID: "@bot:example.org", AllowedUsers: []string{"@owner:example.org"}, Revision: "1"}); err != nil {
		t.Fatal(err)
	}
	sessions := &imageSessions{}
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	conn := &outputConnection{output: make(chan string, 8)}
	img := plugin.Image{Type: "image/png", Data: []byte{1, 2, 3}}
	msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "/new", Images: []plugin.Image{img}}
	if err := c.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	if sessions.text != "/new" || len(sessions.images) != 1 || sessions.images[0].Type != img.Type || !bytes.Equal(sessions.images[0].Data, img.Data) || sessions.created != 0 {
		t.Fatalf("image lost or caption treated as command: %+v", sessions)
	}
	sessions.text, sessions.images = "", nil
	msg.Text = ""
	if err := c.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	if len(sessions.images) != 1 {
		t.Fatal("captionless image lost")
	}
	sessions.images = nil
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionAsked})
	if err := c.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	if sessions.images != nil || sessions.replies != 0 {
		t.Fatal("image used as text reply")
	}
	select {
	case <-conn.output:
	default:
		t.Fatal("missing pending-question explanation")
	}
}
