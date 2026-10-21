package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestDownloadAttachment(t *testing.T) {
	for _, mode := range []string{"plain", "encrypted", "tampered", "bad metadata", "http URI", "empty URI", "declared oversized", "stream oversized", "limit", "empty", "sniff"} {
		t.Run(mode, func(t *testing.T) {
			plain := []byte("arbitrary file contents")
			if mode == "limit" {
				plain = bytes.Repeat([]byte("x"), maxImageBytes)
			}
			if mode == "empty" {
				plain = nil
			}
			payload := bytes.Clone(plain)
			msg := &event.MessageEventContent{MsgType: event.MsgFile, Body: "caption", FileName: "report.dat", URL: "mxc://test/file", Info: &event.FileInfo{MimeType: "application/x-custom"}}
			if mode == "encrypted" || mode == "tampered" || mode == "bad metadata" {
				file := attachment.NewEncryptedFile()
				file.EncryptInPlace(payload)
				raw, _ := json.Marshal(file)
				file = &attachment.EncryptedFile{}
				if err := json.Unmarshal(raw, file); err != nil {
					t.Fatal(err)
				}
				msg.File = &event.EncryptedFileInfo{EncryptedFile: *file, URL: msg.URL}
				if mode == "tampered" {
					payload[0] ^= 1
				}
				if mode == "bad metadata" {
					msg.File.Version = "v0"
				}
			}
			switch mode {
			case "http URI":
				msg.URL = "https://example.org/file"
			case "empty URI":
				msg.URL = ""
			case "declared oversized":
				msg.Info.Size = maxImageBytes + 1
			case "stream oversized":
				payload = bytes.Repeat([]byte("x"), maxImageBytes+1)
			case "sniff":
				msg.Info = nil
				msg.FileName = ""
				msg.Body = "file.txt"
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/_matrix/client/v1/media/download/test/file" || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Errorf("unexpected request: %s auth=%t", r.URL.Path, r.Header.Get("Authorization") != "")
				}
				w.Write(payload)
			}))
			defer server.Close()
			got, err := testClient(t, server.URL).downloadAttachment(context.Background(), msg)
			switch mode {
			case "plain", "encrypted", "limit", "empty", "sniff":
				if err != nil || !bytes.Equal(got.Data, plain) {
					t.Fatalf("bytes=%d err=%v", len(got.Data), err)
				}
				if mode == "sniff" {
					if got.Name != "file.txt" || got.MIMEType != "text/plain; charset=utf-8" {
						t.Fatalf("metadata: %+v", got)
					}
				} else if got.Name != "report.dat" || got.MIMEType != "application/x-custom" {
					t.Fatal("metadata lost")
				}
			default:
				if err == nil {
					t.Fatal("accepted invalid attachment")
				}
			}
			if (mode == "http URI" || mode == "empty URI" || mode == "bad metadata" || mode == "declared oversized") && requests != 0 {
				t.Fatal("downloaded invalid metadata")
			}
		})
	}
}

func TestRunDeliversGenericAttachmentsAfterAuthorization(t *testing.T) {
	for _, kind := range []event.MessageType{event.MsgFile, event.MsgAudio, event.MsgVideo} {
		t.Run(string(kind), func(t *testing.T) {
			plain := []byte("file fixture")
			file := attachment.NewEncryptedFile()
			sealed := bytes.Clone(plain)
			file.EncryptInPlace(sealed)
			raw, _ := json.Marshal(file)
			file = &attachment.EncryptedFile{}
			if err := json.Unmarshal(raw, file); err != nil {
				t.Fatal(err)
			}
			requests := 0
			media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.Write(sealed) }))
			defer media.Close()
			change := func(e *event.Event) {
				e.Content.Parsed = &event.MessageEventContent{MsgType: kind, Body: "caption", FileName: "clip.bin", File: &event.EncryptedFileInfo{EncryptedFile: *file, URL: "mxc://test/file"}}
				if e.ID == "$untrusted" {
					e.Mautrix.TrustSource = nil
				}
			}
			useCrypto(t, &fakeCrypto{change: map[id.EventID]func(*event.Event){"$file": change, "$untrusted": change, "$crowded": change}}, nil)
			live := time.Now().Add(time.Hour).UnixMilli()
			h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{
				"!dm:test":      {encryptedEvent("$file", "@alice:test", live), encryptedEvent("$untrusted", "@alice:test", live)},
				"!crowded:test": {encryptedEvent("$crowded", "@alice:test", live)},
			}))
			h.room("!crowded:test", map[string]event.Membership{"@alice:test": event.MembershipJoin, "@eve:test": event.MembershipJoin})
			c := testClient(t, h.server.URL)
			c.cli.Client = &http.Client{Transport: mediaTransport{base: http.DefaultTransport, media: media.URL}}
			got, _ := runUntilIdle(t, h, c, nil)
			if requests != 1 || len(got) != 1 {
				t.Fatalf("requests=%d delivered=%d", requests, len(got))
			}
			msg := got[0]
			if msg.Text != "caption" || len(msg.Images) != 0 || len(msg.Attachments) != 1 || msg.Attachments[0].Name != "clip.bin" || !bytes.Equal(msg.Attachments[0].Data, plain) {
				t.Fatalf("delivery: %+v", msg)
			}
		})
	}
}
