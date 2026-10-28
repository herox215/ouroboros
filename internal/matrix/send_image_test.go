package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestSendImageToEncryptsTheFile(t *testing.T) {
	h := newHomeserver(t)
	c := testClient(t, h.server.URL)
	plain := imageFixture(t)
	if err := c.SendImageTo(context.Background(), "!dm:test", "@alice:test", "dot.png", plain); err == nil {
		t.Fatal("sent before Run")
	}
	crypt := &fakeCrypto{}
	c.crypto, c.running = crypt, true
	defer func() { c.running = false }()
	for name, data := range map[string][]byte{"empty": nil, "text": []byte("not an image"), "oversized": make([]byte, MaxMediaBytes+1)} {
		if err := c.SendImageTo(context.Background(), "!dm:test", "@alice:test", name, data); err == nil {
			t.Fatalf("sent %s", name)
		}
	}
	// Nothing is uploaded for a room whose peer is not the expected one.
	if err := c.SendImageTo(context.Background(), "!dm:test", "@bob:test", "dot.png", plain); err == nil {
		t.Fatal("sent the image of Bob to Alice")
	}
	if len(h.uploads) != 0 || crypt.encrypted != 0 {
		t.Fatal("uploaded before the room was checked")
	}
	if err := c.SendImageTo(context.Background(), "!dm:test", "@alice:test", "dot.png", plain); err != nil {
		t.Fatal(err)
	}
	if len(h.uploads) != 1 || bytes.Equal(h.uploads[0], plain) || bytes.Contains(h.uploads[0], []byte("PNG")) {
		t.Fatal("the upload is not encrypted")
	}
	if sent, _, _ := h.requests(); !slices.Equal(sent, []string{"!dm:test"}) || len(crypt.contents) != 1 {
		t.Fatalf("sent to %v", sent)
	}
	content := crypt.contents[0]
	if content.MsgType != event.MsgImage || content.Body != "dot.png" || content.URL != "" || content.File == nil ||
		content.File.URL != "mxc://test/upload" || content.Info == nil || content.Info.MimeType != "image/png" || content.Info.Width != 2 {
		t.Fatalf("content %+v", content)
	}
	// The peer decrypts what was uploaded with the key of the message, as it
	// comes in the event.
	raw, _ := json.Marshal(content)
	var received event.MessageEventContent
	if err := json.Unmarshal(raw, &received); err != nil {
		t.Fatal(err)
	}
	sealed := bytes.Clone(h.uploads[0])
	if err := received.File.DecryptInPlace(sealed); err != nil || !bytes.Equal(sealed, plain) {
		t.Fatalf("decrypted: %v", err)
	}
	if err := c.SendImageTo(context.Background(), "!dm:test", "", "dot.png", plain); err == nil || !strings.Contains(err.Error(), "peer") {
		t.Fatalf("no peer: %v", err)
	}
}

// An image whose header claims a format it does not hold is not sent, an
// image without a name is called image, and nothing is sent for a room whose
// state cannot be read or when the upload brings back no address.
func TestSendImageToRefusesWhatCannotBeSent(t *testing.T) {
	h := newHomeserver(t)
	empty := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if empty && r.URL.Path == "/_matrix/media/v3/upload" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		h.serve(w, r)
	}))
	defer server.Close()
	c := testClient(t, server.URL)
	crypt := &fakeCrypto{}
	c.crypto, c.running = crypt, true
	defer func() { c.running = false }()
	broken := []byte("\x89PNG\r\n\x1a\nnot really")
	if err := c.SendImageTo(context.Background(), "!dm:test", "@alice:test", "dot.png", broken); err == nil || !strings.Contains(err.Error(), "could not be read") {
		t.Fatalf("a broken image: %v", err)
	}
	if err := c.SendImageTo(context.Background(), "!dm:test", "@alice:test", "", imageFixture(t)); err != nil {
		t.Fatal(err)
	}
	if len(crypt.contents) != 1 || crypt.contents[0].Body != "image" {
		t.Fatalf("contents %+v", crypt.contents)
	}
	h.breakRoom("!dm:test", true)
	if err := c.SendImageTo(context.Background(), "!dm:test", "@alice:test", "dot.png", imageFixture(t)); err == nil {
		t.Fatal("sent to a room whose state cannot be read")
	}
	h.breakRoom("!dm:test", false)
	empty = true
	if err := c.SendImageTo(context.Background(), "!dm:test", "@alice:test", "dot.png", imageFixture(t)); err == nil || !strings.Contains(err.Error(), "upload failed") {
		t.Fatalf("an upload without an address: %v", err)
	}
	if len(crypt.contents) != 1 {
		t.Fatal("sent without an upload")
	}
}
