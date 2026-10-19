package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
)

func avatarFixture(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	var b bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	var err error
	switch format {
	case "png":
		err = png.Encode(&b, img)
	case "jpeg":
		err = jpeg.Encode(&b, img, nil)
	case "gif":
		err = gif.Encode(&b, img, nil)
	default:
		t.Fatalf("unknown fixture format %s", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func avatarClient(t *testing.T, server string) *Client {
	t.Helper()
	cli, err := mautrix.NewClient(server, "@avatar-bot:test", "secret-avatar-token")
	if err != nil {
		t.Fatal(err)
	}
	cli.DefaultHTTPRetries = 0
	return &Client{cli: cli}
}

func TestSetAvatar(t *testing.T) {
	for _, format := range []string{"png", "jpeg", "gif"} {
		t.Run(format, func(t *testing.T) {
			data := avatarFixture(t, format, 2, 2)
			var flow []string
			profile := ""
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Header.Get("Authorization") != "Bearer secret-avatar-token" || r.URL.Query().Get("access_token") != "" {
					t.Error("missing bearer authentication or token in URL")
				}
				step := r.Method + " " + r.URL.Path
				flow = append(flow, step)
				switch step {
				case "POST /_matrix/media/v3/upload":
					got, err := io.ReadAll(r.Body)
					if err != nil || !bytes.Equal(got, data) || r.Header.Get("Content-Type") != "image/"+format {
						t.Error("upload did not preserve image bytes and content type")
					}
					io.WriteString(w, `{"content_uri":"mxc://test/avatar"}`)
				case "PUT /_matrix/client/v3/profile/@avatar-bot:test/avatar_url":
					var body struct {
						AvatarURL string `json:"avatar_url"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					profile = body.AvatarURL
					if profile != "mxc://test/avatar" {
						t.Errorf("profile URL = %q", profile)
					}
					io.WriteString(w, "{}")
				case "GET /_matrix/client/v3/profile/@avatar-bot:test/avatar_url":
					json.NewEncoder(w).Encode(map[string]string{"avatar_url": profile})
				default:
					t.Errorf("unexpected request %s", step)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			got, err := avatarClient(t, server.URL).SetAvatar(context.Background(), data)
			if err != nil || got != "mxc://test/avatar" {
				t.Fatalf("SetAvatar = %q, %v", got, err)
			}
			want := []string{"POST /_matrix/media/v3/upload", "PUT /_matrix/client/v3/profile/@avatar-bot:test/avatar_url", "GET /_matrix/client/v3/profile/@avatar-bot:test/avatar_url"}
			if !reflect.DeepEqual(flow, want) {
				t.Fatalf("flow = %v, want %v", flow, want)
			}
		})
	}
}

func TestSetAvatarRejectsInvalidImages(t *testing.T) {
	valid := avatarFixture(t, "png", 2, 2)
	cases := map[string][]byte{
		"empty":                              nil,
		"text":                               []byte("not an image"),
		"oversized":                          make([]byte, maxAvatarBytes+1),
		"truncated":                          valid[:len(valid)/2],
		"valid header with truncated pixels": valid[:len(valid)-20],
		"dimension":                          avatarFixture(t, "png", maxAvatarDimension+1, 1),
		"pixels":                             avatarFixture(t, "png", 2049, 2048),
		// WebP is deliberately unsupported: this package has no WebP decoder.
		"webp header": []byte("RIFFxxxxWEBPVP8 "),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				t.Error("invalid image reached server")
				http.Error(w, "unexpected", http.StatusBadRequest)
			}))
			defer server.Close()
			got, err := avatarClient(t, server.URL).SetAvatar(context.Background(), data)
			if err == nil || got != "" || requests != 0 {
				t.Fatalf("SetAvatar = %q, %v; requests=%d", got, err, requests)
			}
		})
	}
}

func TestSetAvatarErrors(t *testing.T) {
	data := avatarFixture(t, "png", 2, 2)
	for _, tc := range []struct {
		name, upload, readback, want string
		failAt, steps                int
	}{
		{name: "upload failure", failAt: 1, steps: 1, want: "avatar upload failed"},
		{name: "invalid upload JSON", upload: "not JSON", steps: 1, want: "avatar upload failed"},
		{name: "non mxc upload", upload: `{"content_uri":"https://test/avatar"}`, steps: 1, want: "avatar upload failed"},
		{name: "empty upload URI", upload: "{}", steps: 1, want: "avatar upload returned an invalid Matrix media URI"},
		{name: "PUT failure", failAt: 2, steps: 2, want: "avatar profile update failed"},
		{name: "GET failure", failAt: 3, steps: 3, want: "avatar profile verification failed"},
		{name: "invalid readback JSON", readback: "not JSON", steps: 3, want: "avatar profile verification failed"},
		{name: "mismatch", readback: `{"avatar_url":"mxc://test/other"}`, steps: 3, want: "avatar profile verification mismatch"},
		{name: "missing readback", readback: "{}", steps: 3, want: "avatar profile verification mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				steps++
				w.Header().Set("Content-Type", "application/json")
				if tc.failAt == steps {
					w.WriteHeader(http.StatusForbidden)
					io.WriteString(w, `{"errcode":"M_FORBIDDEN","error":"secret-avatar-token sensitive server detail"}`)
					return
				}
				switch steps {
				case 1:
					response := tc.upload
					if response == "" {
						response = `{"content_uri":"mxc://test/avatar"}`
					}
					io.WriteString(w, response)
				case 2:
					io.WriteString(w, "{}")
				case 3:
					io.WriteString(w, tc.readback)
				default:
					t.Error("unexpected extra request")
					http.Error(w, "unexpected", http.StatusBadRequest)
				}
			}))
			defer server.Close()
			got, err := avatarClient(t, server.URL).SetAvatar(context.Background(), data)
			if got != "" || err == nil {
				t.Fatalf("SetAvatar = %q, %v", got, err)
			}
			if err.Error() != tc.want || strings.Contains(err.Error(), "secret-avatar-token") || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("unexpected or unsanitized error %q", err)
			}
			if steps != tc.steps {
				t.Fatalf("requests=%d, want %d", steps, tc.steps)
			}
		})
	}
}

func TestSetAvatarRejectsAnUnsupportedFormat(t *testing.T) {
	// A format this package can read its header from but does not upload.
	image.RegisterFormat("testonly", "OURO", func(io.Reader) (image.Image, error) {
		return image.NewGray(image.Rect(0, 0, 1, 1)), nil
	}, func(io.Reader) (image.Config, error) {
		return image.Config{Width: 1, Height: 1}, nil
	})
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Error("unsupported image reached server")
	}))
	defer server.Close()
	got, err := avatarClient(t, server.URL).SetAvatar(context.Background(), []byte("OURO"))
	if got != "" || err == nil || err.Error() != "supported avatars are PNG, JPEG and GIF" || requests != 0 {
		t.Fatalf("SetAvatar = %q, %v; requests=%d", got, err, requests)
	}
}
