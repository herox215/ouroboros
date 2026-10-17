package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/guard"
	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

func TestAvatarToolSetsAndVerifiesConfiguredAccount(t *testing.T) {
	var uploaded bool
	var avatar string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/upload") && r.Method == http.MethodPost:
			img, err := png.Decode(r.Body)
			if err != nil || img.Bounds().Dx() != 2 {
				t.Errorf("bad upload: %v", err)
			}
			uploaded = true
			_, _ = w.Write([]byte(`{"content_uri":"mxc://example.org/avatar"}`))
		case strings.HasSuffix(r.URL.Path, "/profile/@luna:example.org/avatar_url") && r.Method == http.MethodPut:
			if !uploaded {
				t.Error("set before upload")
			}
			var body struct {
				AvatarURL string `json:"avatar_url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			avatar = body.AvatarURL
			_, _ = w.Write([]byte("{}"))
		case strings.HasSuffix(r.URL.Path, "/profile/@luna:example.org/avatar_url") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]string{"avatar_url": avatar})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	s := store{filepath.Join(root, "matrix")}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	if err := s.save(configuration{Enabled: true, Homeserver: server.URL, UserID: "@luna:example.org", Token: "secret", DeviceID: "device", Revision: "one"}); err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "avatar.png")
	if err := os.WriteFile(file, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	tool := avatarTool(s, &guard.Guard{Home: filepath.Join(root, "node"), Workspace: root, Protected: func() ([]string, error) { return nil, nil }})
	args, _ := json.Marshal(map[string]string{"path": "avatar.png"})
	result, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	// The same flow must accept original chat bytes without any file access.
	uploaded = false
	avatar = ""
	model := &avatarAttachmentModel{}
	bot := agent.New(model, "test", "", avatarTool(s, nil))
	input := agent.WithInputMessages(context.Background(), []openai.Message{{Role: "user", Content: "use this", Images: []openai.Image{{Type: "image/png", Data: data.Bytes()}}}})
	if _, err := bot.Respond(input, "attachment-chat", ""); err != nil {
		t.Fatal(err)
	}
	if !uploaded || avatar != "mxc://example.org/avatar" || !model.verified {
		t.Fatal("attachment upload and verification failed")
	}
	var got struct {
		UserID    string `json:"user_id"`
		AvatarURL string `json:"avatar_url"`
		Verified  bool   `json:"verified"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatal(err)
	}
	if got.UserID != "@luna:example.org" || got.AvatarURL != "mxc://example.org/avatar" || !got.Verified {
		t.Fatalf("result %s", result)
	}
}

func TestAvatarToolRefusesUnauthorizedAndProtectedFiles(t *testing.T) {
	root := t.TempDir()
	s := store{filepath.Join(root, "matrix")}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	cfg := configuration{Enabled: true, Homeserver: "https://example.org", UserID: "@luna:example.org", Token: "secret", DeviceID: "device", Revision: "one"}
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "private.png")
	if err := os.WriteFile(file, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	g := &guard.Guard{Home: filepath.Join(root, "node"), Workspace: root, Protected: func() ([]string, error) { return []string{file}, nil }}
	tool := avatarTool(s, g)
	if !tool.OwnerOnly {
		t.Fatal("tool must be owner-only")
	}
	args := json.RawMessage(`{"path":"private.png"}`)
	peer := a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"})
	if _, err := tool.Run(peer, args); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("peer: %v", err)
	}
	if _, err := tool.Run(context.Background(), args); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("protected: %v", err)
	}
	cfg.Enabled = false
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(context.Background(), args); err == nil || !strings.Contains(err.Error(), "log in") {
		t.Fatalf("disabled: %v", err)
	}
	cfg.Enabled = true
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(root, "large.png")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxAvatarBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"path":"large.png"}`)); err == nil || !strings.Contains(err.Error(), "5 MiB") {
		t.Fatalf("oversized: %v", err)
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"path":"."}`)); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("directory: %v", err)
	}
}

func TestPluginContributesAvatarTool(t *testing.T) {
	k := kernel.New()
	dir := home.Dir(t.TempDir())
	if err := k.Start(sessionProvider(&testSessions{}), Plugin(dir, &guard.Guard{Home: string(dir)})); err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	tools := plugin.All[plugin.Tool](k.Context())
	if len(tools) != 1 || tools[0].Name != setAvatar || !tools[0].OwnerOnly {
		t.Fatalf("tools: %+v", tools)
	}
}
