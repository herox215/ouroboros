package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"

	"maunium.net/go/mautrix/event"
)

// wavFixture is a RIFF header, which is as much as the format check needs.
func wavFixture() []byte {
	return []byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x40\x1f\x00\x00\x80>\x00\x00\x02\x00\x10\x00data\x00\x00\x00\x00")
}

func TestSendAudioToSendsEncryptedAudio(t *testing.T) {
	h := newHomeserver(t)
	c := testClient(t, h.server.URL)
	wav := wavFixture()
	if err := c.SendAudioTo(context.Background(), "!dm:test", "@alice:test", "click.wav", wav); err == nil {
		t.Fatal("sent before Run")
	}
	crypt := &fakeCrypto{}
	c.crypto, c.running = crypt, true
	defer func() { c.running = false }()
	rejected := map[string][]byte{
		"empty.wav": nil, "text.mp3": []byte("not audio"), "oversized.wav": make([]byte, MaxMediaBytes+1),
		"click": wav, "click.flac": wav, "click.ogg": wav,
	}
	for name, data := range rejected {
		if err := c.SendAudioTo(context.Background(), "!dm:test", "@alice:test", name, data); err == nil {
			t.Fatalf("sent %s", name)
		}
	}
	if err := c.SendAudioTo(context.Background(), "!dm:test", "@bob:test", "click.wav", wav); err == nil {
		t.Fatal("sent the file of Bob to Alice")
	}
	if len(h.uploads) != 0 || crypt.encrypted != 0 {
		t.Fatal("uploaded before the room was checked")
	}
	if err := c.SendAudioTo(context.Background(), "!dm:test", "@alice:test", "click.wav", wav); err != nil {
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
		content.Info == nil || content.Info.MimeType != "audio/wav" || content.Info.Size != len(wav) {
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

// Each documented format is sent as audio with its own MIME type, though
// sniffing alone would call OGG application/ogg, M4A video/mp4 and a bare
// MP3 frame application/octet-stream.
func TestSendAudioToSendsEveryDocumentedFormat(t *testing.T) {
	h := newHomeserver(t)
	c := testClient(t, h.server.URL)
	crypt := &fakeCrypto{}
	c.crypto, c.running = crypt, true
	defer func() { c.running = false }()
	for _, tc := range []struct {
		name string
		data []byte
		mime string
	}{
		{"click.wav", wavFixture(), "audio/wav"},
		{"click.ogg", []byte("OggS\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x13OpusHead"), "audio/ogg"},
		{"click.mp3", []byte("ID3\x04\x00\x00\x00\x00\x00\x00\xff\xfb\x90\x00"), "audio/mpeg"},
		{"bare.MP3", []byte("\xff\xfb\x90\x00\x00\x00\x00\x00"), "audio/mpeg"},
		{"click.m4a", []byte("\x00\x00\x00\x20ftypM4A \x00\x00\x00\x00M4A mp42isom\x00\x00\x00\x00"), "audio/mp4"},
	} {
		crypt.contents = nil
		if err := c.SendAudioTo(context.Background(), "!dm:test", "@alice:test", tc.name, tc.data); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(crypt.contents) != 1 {
			t.Fatalf("%s: sent %d messages", tc.name, len(crypt.contents))
		}
		content := crypt.contents[0]
		if content.MsgType != event.MsgAudio || content.Body != tc.name || content.Info == nil || content.Info.MimeType != tc.mime {
			t.Fatalf("%s: content %+v", tc.name, content)
		}
	}
}
