package matrix

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"ouroboros/plugin"
)

func TestAudioTranscriptionIsOrdinaryInput(t *testing.T) {
	for _, pending := range []bool{false, true} {
		for _, transcript := range []string{"/cancel", "/new", "/answer Allow once", "Allow once"} {
			t.Run(transcript+map[bool]string{true: " pending", false: " idle"}[pending], func(t *testing.T) {
				s, _ := conversationStore(t)
				sessions := &testSessions{asked: make(chan string, 1)}
				if pending {
					sessions.set(plugin.SessionEvent{Kind: plugin.SessionAsked})
				}
				c, err := openConversations(s, sessions)
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				c.transcribe = func(ctx context.Context, name, mime string, data []byte) (string, error) {
					calls++
					if ctx.Err() != nil || name != "voice.ogg" || mime != "audio/ogg; codecs=opus" || string(data) != "fixture" {
						t.Fatalf("unexpected transcription arguments: %s %s %s", name, mime, data)
					}
					return transcript, nil
				}
				conn := &recordingConnection{}
				msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Attachments: []plugin.Attachment{{Name: "voice.ogg", Type: "audio/ogg; codecs=opus", Data: []byte("fixture")}}}
				if err := c.receive(t.Context(), conn, msg); err != nil {
					t.Fatal(err)
				}
				if calls != 1 || sessions.asks != 1 || sessions.replies != 0 || sessions.cancels != 0 {
					t.Fatalf("calls=%d asks=%d replies=%d cancels=%d", calls, sessions.asks, sessions.replies, sessions.cancels)
				}
				if got := <-sessions.asked; got != "[Audio transcription]\n"+transcript {
					t.Fatalf("agent text %q", got)
				}
				if got := conn.messages(); len(got) != 1 || !strings.Contains(got[0], "[Audio transcription]\n"+transcript) {
					t.Fatalf("sender output %v", got)
				}
			})
		}
	}
}

func TestAudioPreservesOtherContent(t *testing.T) {
	s, _ := conversationStore(t)
	sessions := &attachmentSessions{}
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	c.transcribe = func(context.Context, string, string, []byte) (string, error) { return "spoken words", nil }
	file := plugin.Attachment{Name: "notes.txt", Type: "text/plain", Data: []byte("notes")}
	msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "caption", Attachments: []plugin.Attachment{file, {Name: "voice", Type: "audio/ogg", Data: []byte("voice")}}}
	if err := c.receive(t.Context(), &recordingConnection{}, msg); err != nil {
		t.Fatal(err)
	}
	if sessions.text != "caption\n\n[Audio transcription]\nspoken words" || !reflect.DeepEqual(sessions.files, []plugin.Attachment{file}) {
		t.Fatalf("text=%q files=%+v", sessions.text, sessions.files)
	}
	// Images survive preprocessing, including when there are no remaining files.
	msg.Images = []plugin.Image{{Type: "image/png", Data: []byte("image")}}
	msg.Attachments = msg.Attachments[1:]
	got, _, err := c.prepareAudio(t.Context(), &recordingConnection{}, msg, "1")
	if err != nil || !reflect.DeepEqual(got.Images, msg.Images) || len(got.Attachments) != 0 {
		t.Fatalf("images lost: %+v %v", got, err)
	}
}

func TestAudioFailureDoesNotCallAgent(t *testing.T) {
	for _, failure := range []string{"disabled", "model missing", "transcription error", "empty"} {
		t.Run(failure, func(t *testing.T) {
			s, _ := conversationStore(t)
			sessions := &testSessions{}
			c, err := openConversations(s, sessions)
			if err != nil {
				t.Fatal(err)
			}
			c.transcribe = func(context.Context, string, string, []byte) (string, error) {
				if failure == "empty" {
					return "  ", nil
				}
				return "", errors.New(failure)
			}
			conn := &recordingConnection{}
			msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "keep me", Attachments: []plugin.Attachment{{Type: "audio/wav"}}}
			if err := c.receive(t.Context(), conn, msg); err != nil {
				t.Fatal(err)
			}
			if sessions.asks != 0 || sessions.replies != 0 || len(c.entries) != 0 {
				t.Fatal("failed audio reached conversation")
			}
			if got := conn.messages(); len(got) != 1 || !strings.Contains(got[0], "Audio transcription failed:") || !strings.Contains(got[0], "not sent to the agent") {
				t.Fatalf("output %v", got)
			}
		})
	}
}

func TestAudioAuthorizationRevocationAndCancellation(t *testing.T) {
	for _, mode := range []string{"unauthorized", "stale guard", "canceled", "revoked during transcription", "canceled during transcription"} {
		t.Run(mode, func(t *testing.T) {
			s, cfg := conversationStore(t)
			sessions := &testSessions{}
			c, err := openConversations(s, sessions)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Attachments: []plugin.Attachment{{Type: "audio/ogg"}}}
			conn := &recordingConnection{}
			var connection connection = conn
			if mode == "unauthorized" {
				msg.Sender = "@stranger:example.org"
			}
			if mode == "canceled" {
				cancel()
			}
			if mode == "stale guard" {
				connection = guardedConnection{connection: conn, store: s, revision: "old"}
			}
			calls := 0
			c.transcribe = func(context.Context, string, string, []byte) (string, error) {
				calls++
				if mode == "revoked during transcription" {
					cfg.AllowedUsers = nil
					cfg.Revision = "2"
					if err := s.save(cfg); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "canceled during transcription" {
					cancel()
				}
				return "Allow once", nil
			}
			if err := c.receive(ctx, connection, msg); err == nil {
				t.Fatal("expected refusal")
			}
			wantCalls := 0
			if strings.Contains(mode, "during") {
				wantCalls = 1
			}
			if calls != wantCalls || sessions.asks != 0 || sessions.replies != 0 || len(conn.messages()) != 0 {
				t.Fatalf("calls=%d asks=%d replies=%d output=%v", calls, sessions.asks, sessions.replies, conn.messages())
			}
		})
	}
}

func TestOnlyAudioMIMEIsTranscribed(t *testing.T) {
	for _, mime := range []string{"application/octet-stream", "video/ogg", "text/plain", "audio/ogg; invalid"} {
		if audioAttachment(plugin.Attachment{Name: "voice.ogg", Type: mime}) {
			t.Fatalf("eligible: %s", mime)
		}
	}
	for _, mime := range []string{"audio/ogg", "audio/mp4", "AUDIO/WAV", "audio/ogg; codecs=opus"} {
		if !audioAttachment(plugin.Attachment{Type: mime}) {
			t.Fatalf("ineligible: %s", mime)
		}
	}
}
