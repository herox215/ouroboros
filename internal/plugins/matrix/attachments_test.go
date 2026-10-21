package matrix

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	transport "ouroboros/internal/matrix"
	"ouroboros/plugin"
)

type attachmentSessions struct {
	testSessions
	text           string
	files          []plugin.Attachment
	asked, steered int
}

func (s *attachmentSessions) AskAttachments(_ context.Context, _, text string, files []plugin.Attachment) error {
	s.text, s.files = text, files
	s.asked++
	return nil
}
func (s *attachmentSessions) SteerAttachments(_ context.Context, _, text string, files []plugin.Attachment) error {
	s.text, s.files = text, files
	s.steered++
	return nil
}

func TestAttachmentConversion(t *testing.T) {
	msg := transport.Message{Attachments: []transport.Attachment{{Name: "report.pdf", MIMEType: "application/pdf", Data: []byte("fixture")}}}
	want := []plugin.Attachment{{Name: "report.pdf", Type: "application/pdf", Data: []byte("fixture")}}
	if got := attachmentsOf(msg); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if len(imagesOf(msg)) != 0 {
		t.Fatal("file converted to image")
	}
}

func TestConversationForwardsAttachments(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, text := range []string{"", "/new", "/cancel", "/answer Allow once", "  new topic\nlook here  ", "Allow once"} {
			t.Run(text+map[bool]string{false: " send", true: " reply"}[pending], func(t *testing.T) {
				store, _ := conversationStore(t)
				sessions := &attachmentSessions{}
				c, err := openConversations(store, sessions)
				if err != nil {
					t.Fatal(err)
				}
				conn := &recordingConnection{}
				msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}
				if err := c.receive(t.Context(), conn, msg); err != nil {
					t.Fatal(err)
				}
				if pending {
					sessions.set(plugin.SessionEvent{Kind: plugin.SessionAsked, Args: json.RawMessage(`{"question":"Continue?","options":["Allow once","No"]}`)})
				}
				msg.Text = text
				msg.Attachments = []plugin.Attachment{{Name: "clip.dat", Type: "application/octet-stream", Data: []byte("data")}}
				if err := c.receive(t.Context(), conn, msg); err != nil {
					t.Fatal(err)
				}
				if sessions.text != text || !reflect.DeepEqual(sessions.files, msg.Attachments) || len(conn.messages()) != 0 {
					t.Fatalf("text=%q files=%+v messages=%v", sessions.text, sessions.files, conn.messages())
				}
				if pending && (sessions.steered != 1 || sessions.asked != 0) || !pending && (sessions.asked != 1 || sessions.steered != 0) {
					t.Fatalf("ask=%d steer=%d", sessions.asked, sessions.steered)
				}
			})
		}
	}
}

func TestConversationExplainsUnsupportedAttachments(t *testing.T) {
	for _, pending := range []bool{false, true} {
		s, _ := conversationStore(t)
		sessions := &testSessions{}
		c, err := openConversations(s, sessions)
		if err != nil {
			t.Fatal(err)
		}
		conn := &recordingConnection{}
		msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}
		if err := c.receive(t.Context(), conn, msg); err != nil {
			t.Fatal(err)
		}
		if pending {
			sessions.set(plugin.SessionEvent{Kind: plugin.SessionAsked})
		}
		msg.Text = ""
		msg.Attachments = []plugin.Attachment{{Name: "file", Data: []byte("fixture")}}
		if err := c.receive(t.Context(), conn, msg); err != nil {
			t.Fatal(err)
		}
		if got := conn.messages(); len(got) != 1 || got[0] != "!room:example.org @owner:example.org This session backend does not support attachments." {
			t.Fatalf("messages=%v", got)
		}
	}
}
