package matrix

import (
	"bytes"
	"context"
	"strings"
	"testing"

	transport "ouroboros/internal/matrix"
)

func TestImagesOfKeepsTypeAndData(t *testing.T) {
	got := imagesOf(transport.Message{Images: []transport.Image{
		{MIMEType: "image/png", Data: []byte{1, 2}},
		{MIMEType: "image/jpeg", Data: []byte{3}},
	}})
	if len(got) != 2 || got[0].Type != "image/png" || !bytes.Equal(got[0].Data, []byte{1, 2}) {
		t.Fatalf("first image %+v", got)
	}
	if got[1].Type != "image/jpeg" || !bytes.Equal(got[1].Data, []byte{3}) {
		t.Fatalf("second image %+v", got)
	}
	if images := imagesOf(transport.Message{}); len(images) != 0 {
		t.Fatalf("images of a text message: %+v", images)
	}
}

// The connection hands images, audio and votes to the Matrix client, which
// refuses each without the peer it expects.
func TestConnectionPassesSendsToTheClient(t *testing.T) {
	conn, err := newTransport(context.Background(), configuration{Homeserver: "https://example.org", UserID: "@bot:example.org", Token: "token", DeviceID: "BOT"}, store{t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sdk := conn.(*sdkConnection)
	for name, send := range map[string]func() error{
		"image": func() error { return sdk.SendImageTo(context.Background(), "!a:test", "", "a.png", []byte("png")) },
		"audio": func() error { return sdk.SendAudioTo(context.Background(), "!a:test", "", "a.wav", []byte("wav")) },
		"vote":  func() error { return sdk.VotePollTo(context.Background(), "!a:test", "", "$poll", nil) },
	} {
		if err := send(); err == nil || !strings.Contains(err.Error(), "expected Matrix peer") {
			t.Errorf("%s without a peer: %v", name, err)
		}
	}
}
