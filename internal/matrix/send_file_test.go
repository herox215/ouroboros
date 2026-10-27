package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"

	"maunium.net/go/mautrix/event"
)

// wavFixture is a RIFF header, which is as much as sniffing needs.
func wavFixture() []byte {
	return []byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x40\x1f\x00\x00\x80>\x00\x00\x02\x00\x10\x00data\x00\x00\x00\x00")
}

func TestSendFileToSendsEncryptedAudio(t *testing.T) {
	h := newHomeserver(t)
	c := testClient(t, h.server.URL)
	wav := wavFixture()
	if err := c.SendFileTo(context.Background(), "!dm:test", "@alice:test", "click.wav", wav); err == nil {
		t.Fatal("sent before Run")
	}
	crypt := &fakeCrypto{}
	c.crypto, c.running = crypt, true
	defer func() { c.running = false }()
	for name, data := range map[string][]byte{"empty": nil, "text": []byte("not audio"), "oversized": make([]byte, maxImageBytes+1)} {
		if err := c.SendFileTo(context.Background(), "!dm:test", "@alice:test", name, data); err == nil {
			t.Fatalf("sent %s", name)
		}
	}
	if err := c.SendFileTo(context.Background(), "!dm:test", "@bob:test", "click.wav", wav); err == nil {
		t.Fatal("sent the file of Bob to Alice")
	}
	if len(h.uploads) != 0 || crypt.encrypted != 0 {
		t.Fatal("uploaded before the room was checked")
	}
	if err := c.SendFileTo(context.Background(), "!dm:test", "@alice:test", "click.wav", wav); err != nil {
		t.Fatal(err)
	}
	if len(h.uploads) != 1 || bytes.Equal(h.uploads[0], wav) || bytes.Contains(h.uploads[0], []byte("WAVE")) {
		t.Fatal("the upload is not encrypted")
	}
	if sent, _, _ := h.requests(); !slices.Equal(sent, []string{"!dm:test"}) || len(crypt.contents) != 1 {
		t.Fatalf("sent to %v", sent)
	}
	content := crypt.contents[0]
	if content.MsgType != event.MsgAudio || content.Body != "click.wav" || content.File == nil ||
		content.Info == nil || content.Info.MimeType != "audio/wave" || content.Info.Size != len(wav) {
		t.Fatalf("content %+v", content)
	}
	raw, _ := json.Marshal(content)
	var received event.MessageEventContent
	if err := json.Unmarshal(raw, &received); err != nil {
		t.Fatal(err)
	}
	sealed := bytes.Clone(h.uploads[0])
	if err := received.File.DecryptInPlace(sealed); err != nil || !bytes.Equal(sealed, wav) {
		t.Fatalf("decrypted: %v", err)
	}
}
