package matrix

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestRunRejectsUnreadableOrUnsafeAttachment(t *testing.T) {
	for _, mode := range []string{"invalid URI", "room changed"} {
		t.Run(mode, func(t *testing.T) {
			change := func(e *event.Event) {
				uri := id.ContentURIString("mxc://test/file")
				if mode == "invalid URI" {
					uri = "https://example.org/file"
				}
				e.Content.Parsed = &event.MessageEventContent{MsgType: event.MsgFile, Body: "file.txt", URL: uri}
			}
			crypt := &fakeCrypto{change: map[id.EventID]func(*event.Event){"$file": change}}
			useCrypto(t, crypt, nil)
			live := time.Now().Add(time.Hour).UnixMilli()
			h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{"!dm:test": {encryptedEvent("$file", "@alice:test", live)}}))
			requests := 0
			media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				h.changeRoomAfterRead("!dm:test", map[string]event.Membership{"@bob:test": event.MembershipJoin})
				w.Write([]byte("file fixture"))
			}))
			defer media.Close()
			c := testClient(t, h.server.URL)
			c.allowed["@bob:test"] = true
			c.cli.Client = &http.Client{Transport: mediaTransport{base: http.DefaultTransport, media: media.URL}}
			got, _ := runUntilIdle(t, h, c, nil)
			if len(got) != 0 {
				t.Fatal("delivered rejected attachment")
			}
			if mode == "invalid URI" {
				sent := crypt.sent()
				if requests != 0 || len(sent) != 1 || sent[0] != "The attachment could not be read: invalid Matrix media URI" {
					t.Fatalf("requests=%d sent=%v", requests, sent)
				}
			} else if requests != 1 || len(crypt.sent()) != 0 {
				t.Fatalf("requests=%d sent=%v", requests, crypt.sent())
			}
		})
	}
}
