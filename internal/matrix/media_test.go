package matrix

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func imageFixture(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDownloadImage(t *testing.T) {
	plain := imageFixture(t)
	for _, mode := range []string{"plain", "encrypted", "tampered", "bad metadata", "unsupported", "oversized", "http URI"} {
		t.Run(mode, func(t *testing.T) {
			payload := append([]byte(nil), plain...)
			content := &event.MessageEventContent{MsgType: event.MsgImage, URL: "mxc://test/image"}
			if mode == "encrypted" || mode == "tampered" || mode == "bad metadata" {
				file := attachment.NewEncryptedFile()
				payload = file.Encrypt(payload)
				raw, _ := json.Marshal(file)
				file = &attachment.EncryptedFile{}
				if err := json.Unmarshal(raw, file); err != nil {
					t.Fatal(err)
				}
				content.File = &event.EncryptedFileInfo{EncryptedFile: *file, URL: content.URL}
				if mode == "tampered" {
					payload[0] ^= 1
				}
				if mode == "bad metadata" {
					content.File.Version = "v0"
				}
			}
			if mode == "unsupported" {
				payload = []byte("not an image")
			}
			if mode == "oversized" {
				payload = bytes.Repeat([]byte("x"), maxImageBytes+1)
			}
			if mode == "http URI" {
				content.URL = "https://example.org/image"
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/_matrix/client/v1/media/download/test/image" || r.Header.Get("Authorization") == "" {
					t.Errorf("unauthenticated or wrong media request: %s", r.URL.Path)
				}
				w.Write(payload)
			}))
			defer server.Close()
			c := testClient(t, server.URL)
			got, err := c.downloadImage(context.Background(), content)
			if mode == "plain" || mode == "encrypted" {
				if err != nil || got.MIMEType != "image/png" || !bytes.Equal(got.Data, plain) {
					t.Fatalf("got %+v, %v", got, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid image")
			}
			if (mode == "http URI" || mode == "bad metadata") && requests != 0 {
				t.Fatal("downloaded invalid media")
			}
		})
	}
}

func TestRunDeliversImageAfterAuthorization(t *testing.T) {
	plain := imageFixture(t)
	file := attachment.NewEncryptedFile()
	encrypted := file.Encrypt(plain)
	raw, _ := json.Marshal(file)
	file = &attachment.EncryptedFile{}
	if err := json.Unmarshal(raw, file); err != nil {
		t.Fatal(err)
	}
	var requests int
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.Write(encrypted) }))
	defer media.Close()
	change := func(e *event.Event) {
		e.Content.Parsed = &event.MessageEventContent{MsgType: event.MsgImage, Body: "What is this?", FileName: "photo.png", File: &event.EncryptedFileInfo{EncryptedFile: *file, URL: "mxc://test/image"}}
		if e.ID == "$untrusted" {
			e.Mautrix.TrustSource = nil
		}
	}
	crypt := &fakeCrypto{change: map[id.EventID]func(*event.Event){"$image": change, "$untrusted": change, "$crowded": change}}
	useCrypto(t, crypt, nil)
	live := time.Now().Add(time.Hour).UnixMilli()
	h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{
		"!dm:test":      {encryptedEvent("$image", "@alice:test", live), encryptedEvent("$untrusted", "@alice:test", live)},
		"!crowded:test": {encryptedEvent("$crowded", "@alice:test", live)},
	}))
	h.room("!crowded:test", map[string]event.Membership{"@alice:test": event.MembershipJoin, "@eve:test": event.MembershipJoin})
	c := testClient(t, h.server.URL)
	// Route only media requests to the fixture server, preserving the normal
	// sync and membership checks of Run.
	c.cli.Client = &http.Client{Transport: mediaTransport{base: http.DefaultTransport, media: media.URL}}
	got, _ := runUntilIdle(t, h, c, nil)
	if requests != 1 || len(got) != 1 || got[0].Text != "What is this?" || len(got[0].Images) != 1 || !bytes.Equal(got[0].Images[0].Data, plain) {
		t.Fatalf("requests=%d delivered=%+v (fixture %s)", requests, got, base64.StdEncoding.EncodeToString(plain))
	}
}

type mediaTransport struct {
	base  http.RoundTripper
	media string
}

func (m mediaTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, "/media/download/") {
		clone := r.Clone(r.Context())
		u := *r.URL
		target, _ := http.NewRequest(http.MethodGet, m.media, nil)
		u.Scheme, u.Host = target.URL.Scheme, target.URL.Host
		clone.URL = &u
		return m.base.RoundTrip(clone)
	}
	return m.base.RoundTrip(r)
}
