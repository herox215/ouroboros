package matrix

import (
	"bytes"
	"context"
	"encoding/json"
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
	for name, data := range map[string][]byte{"empty": nil, "text": []byte("not an image"), "oversized": make([]byte, maxImageBytes+1)} {
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
