package matrix

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"slices"
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
				file.EncryptInPlace(payload)
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
	encrypted := bytes.Clone(plain)
	file.EncryptInPlace(encrypted)
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

// failingBody fails every read, as a download cut off mid-body would.
type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingBody) Close() error             { return nil }

// statusTransport answers with a status but no transport error.
type statusTransport struct{ status int }

func (s statusTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: s.status, Status: http.StatusText(s.status), Body: http.NoBody, Header: make(http.Header), Request: r}, nil
}

func TestDownloadImageFails(t *testing.T) {
	content := func() *event.MessageEventContent {
		return &event.MessageEventContent{MsgType: event.MsgImage, URL: "mxc://test/image"}
	}
	t.Run("declared size", func(t *testing.T) {
		c := testClient(t, "https://matrix.test")
		msg := content()
		msg.Info = &event.FileInfo{Size: maxImageBytes + 1}
		if _, err := c.downloadImage(context.Background(), msg); err == nil || !strings.Contains(err.Error(), "10 MiB") {
			t.Fatalf("download: %v", err)
		}
	})
	t.Run("request failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		c := testClient(t, server.URL)
		c.cli.DefaultHTTPRetries = 0
		server.Close()
		if _, err := c.downloadImage(context.Background(), content()); err == nil || !strings.Contains(err.Error(), "download failed") {
			t.Fatalf("download: %v", err)
		}
	})
	t.Run("unexpected status", func(t *testing.T) {
		c := testClient(t, "https://matrix.test")
		c.cli.Client = &http.Client{Transport: statusTransport{status: http.StatusNoContent}}
		if _, err := c.downloadImage(context.Background(), content()); err == nil || !strings.Contains(err.Error(), "download failed") {
			t.Fatalf("download: %v", err)
		}
	})
	t.Run("body read failure", func(t *testing.T) {
		c := testClient(t, "https://matrix.test")
		c.cli.Client = &http.Client{Transport: bodyTransport{http.StatusOK}}
		if _, err := c.downloadImage(context.Background(), content()); err == nil || !strings.Contains(err.Error(), "download failed") {
			t.Fatalf("download: %v", err)
		}
	})
}

// bodyTransport answers every request with a body that cannot be read.
type bodyTransport struct{ status int }

func (b bodyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: b.status, Status: http.StatusText(b.status), Body: failingBody{}, Header: make(http.Header), Request: r}, nil
}

func TestRunReportsImagesItCannotRead(t *testing.T) {
	live := time.Now().Add(time.Hour).UnixMilli()
	change := func(e *event.Event) {
		e.Content.Parsed = &event.MessageEventContent{MsgType: event.MsgImage, Body: "What is this?", URL: "mxc://test/image"}
	}
	for _, tc := range []struct {
		name    string
		handler http.Handler
		want    []string
	}{
		{"download fails", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "gone", http.StatusInternalServerError) }),
			[]string{"The image could not be read: media download failed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			crypt := &fakeCrypto{change: map[id.EventID]func(*event.Event){"$image": change}}
			useCrypto(t, crypt, nil)
			media := httptest.NewServer(tc.handler)
			defer media.Close()
			h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{"!dm:test": {encryptedEvent("$image", "@alice:test", live)}}))
			c := testClient(t, h.server.URL)
			c.cli.Client = &http.Client{Transport: mediaTransport{base: http.DefaultTransport, media: media.URL}}
			delivered, err := runUntilIdle(t, h, c, nil)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run: %v", err)
			}
			if len(delivered) != 0 || !slices.Equal(crypt.sent(), tc.want) {
				t.Fatalf("delivered %+v, sent %q", delivered, crypt.sent())
			}
			if sent, _, _ := h.requests(); !slices.Equal(sent, []string{"!dm:test"}) {
				t.Fatalf("sent to %v", sent)
			}
		})
	}
}

func TestRunDropsAnImageWhoseRoomChangedWhileDownloading(t *testing.T) {
	live := time.Now().Add(time.Hour).UnixMilli()
	plain := imageFixture(t)
	change := func(e *event.Event) {
		e.Content.Parsed = &event.MessageEventContent{MsgType: event.MsgImage, Body: "What is this?", URL: "mxc://test/image"}
	}
	for name, during := range map[string]func(t *testing.T, h *homeserver){
		// The state of the room cannot be read any more.
		"unreadable": func(t *testing.T, h *homeserver) { h.breakRoom("!dm:test", true) },
		// Alice leaves and Bob becomes the peer: the sender is no longer the
		// one it is safe to answer.
		"changed hands": func(t *testing.T, h *homeserver) {
			h.changeRoomAfterRead("!dm:test", map[string]event.Membership{"@bob:test": event.MembershipJoin})
		},
	} {
		t.Run(name, func(t *testing.T) {
			crypt := &fakeCrypto{change: map[id.EventID]func(*event.Event){"$image": change}}
			useCrypto(t, crypt, nil)
			h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{"!dm:test": {encryptedEvent("$image", "@alice:test", live)}}))
			requests := 0
			media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				// The room changes while the image is on its way.
				during(t, h)
				w.Write(plain)
			}))
			defer media.Close()
			c := testClient(t, h.server.URL)
			c.allowed["@bob:test"] = true
			c.cli.Client = &http.Client{Transport: mediaTransport{base: http.DefaultTransport, media: media.URL}}
			delivered, err := runUntilIdle(t, h, c, nil)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run: %v", err)
			}
			if requests != 1 || len(delivered) != 0 || len(crypt.sent()) != 0 {
				t.Fatalf("requests=%d delivered=%+v sent=%q", requests, delivered, crypt.sent())
			}
			if sent, _, _ := h.requests(); len(sent) != 0 {
				t.Fatalf("sent to %v", sent)
			}
		})
	}
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
