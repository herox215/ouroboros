package matrix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// avatarFixtureTool makes a store with cfg and an avatar.png beside it.
func avatarFixtureTool(t *testing.T, cfg configuration, files *guard.Guard) (plugin.Tool, store, string) {
	t.Helper()
	root := t.TempDir()
	s := store{filepath.Join(root, "matrix")}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	if cfg.Revision != "" {
		if err := s.save(cfg); err != nil {
			t.Fatal(err)
		}
	}
	var image bytes.Buffer
	if err := png.Encode(&image, pngImage(2, 2)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "avatar.png"), image.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return avatarTool(s, files), s, root
}

func pngImage(width, height int) image.Image {
	return image.NewRGBA(image.Rect(0, 0, width, height))
}

func validAvatarConfig(server string) configuration {
	return configuration{Enabled: true, Homeserver: server, UserID: "@luna:example.org", Token: "secret", DeviceID: "device", Revision: "one"}
}

func TestAvatarToolReportsUnreadableSettings(t *testing.T) {
	s := store{t.TempDir()}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path("settings.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := avatarTool(s, nil).Run(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "cannot read Matrix settings") {
		t.Fatalf("run: %v", err)
	}
}

type avatarResultModel struct {
	called bool
	result string
}

func (m *avatarResultModel) Complete(_ context.Context, _ string, messages []openai.Message, _ []openai.Tool) (openai.Message, error) {
	if !m.called {
		m.called = true
		return openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "avatar", Type: "function", Function: openai.FunctionCall{Name: setAvatar, Arguments: "{}"}}}}, nil
	}
	m.result = messages[len(messages)-1].Content
	return openai.Message{Role: "assistant", Content: "done"}, nil
}

func runAvatarTool(t *testing.T, tool plugin.Tool, images []openai.Image) string {
	t.Helper()
	model := &avatarResultModel{}
	bot := agent.New(model, "test", "", tool)
	input := agent.WithInputMessages(context.Background(), []openai.Message{{Role: "user", Content: "use this", Images: images}})
	if _, err := bot.Respond(input, "image-chat", ""); err != nil {
		t.Fatal(err)
	}
	return model.result
}

func TestAvatarToolRefusesAnOversizedAttachment(t *testing.T) {
	tool, _, _ := avatarFixtureTool(t, validAvatarConfig("https://example.org"), nil)
	result := runAvatarTool(t, tool, []openai.Image{{Type: "image/png", Data: make([]byte, maxAvatarBytes+1)}})
	if !strings.Contains(result, "exceeds 5 MiB") {
		t.Fatalf("tool said %q", result)
	}
}

func TestAvatarToolNeedsFileAccessForAPath(t *testing.T) {
	tool, _, _ := avatarFixtureTool(t, validAvatarConfig("https://example.org"), nil)
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"path":"avatar.png"}`)); err == nil || !strings.Contains(err.Error(), "file access is unavailable") {
		t.Fatalf("run: %v", err)
	}
}

func TestAvatarToolRejectsSettingsChangedWhileReading(t *testing.T) {
	root := t.TempDir()
	s := store{filepath.Join(root, "matrix")}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	cfg := validAvatarConfig("https://example.org")
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	var image bytes.Buffer
	if err := png.Encode(&image, pngImage(2, 2)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "avatar.png"), image.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	changed := cfg
	changed.Revision = "two"
	files := &guard.Guard{Home: filepath.Join(root, "node"), Workspace: root, Protected: func() ([]string, error) {
		return nil, s.save(changed)
	}}
	_, err := avatarTool(s, files).Run(context.Background(), json.RawMessage(`{"path":"avatar.png"}`))
	if err == nil || !strings.Contains(err.Error(), "settings changed") {
		t.Fatalf("run: %v", err)
	}
}

func TestAvatarToolReportsRulesItCannotRead(t *testing.T) {
	root := t.TempDir()
	s := store{filepath.Join(root, "matrix")}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	if err := s.save(validAvatarConfig("https://example.org")); err != nil {
		t.Fatal(err)
	}
	var image bytes.Buffer
	if err := png.Encode(&image, pngImage(2, 2)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "avatar.png"), image.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	files := &guard.Guard{Home: filepath.Join(root, "node"), Workspace: root, Protected: func() ([]string, error) {
		return nil, errors.New("no rules")
	}}
	if _, err := avatarTool(s, files).Run(context.Background(), json.RawMessage(`{"path":"avatar.png"}`)); err == nil || !strings.Contains(err.Error(), "no rules") {
		t.Fatalf("run: %v", err)
	}
}

func TestAvatarToolFailsToConnectAnIncompleteLogin(t *testing.T) {
	for name, change := range map[string]func(*configuration){
		"no device": func(c *configuration) { c.DeviceID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validAvatarConfig("https://example.org")
			change(&cfg)
			tool, _, _ := avatarFixtureTool(t, cfg, nil)
			result := runAvatarTool(t, tool, []openai.Image{{Type: "image/png", Data: []byte("image")}})
			if !strings.Contains(result, "incomplete or invalid") {
				t.Fatalf("tool said %q", result)
			}
		})
	}
}

func TestAvatarToolReportsAnUploadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"refused"}`))
	}))
	defer server.Close()
	tool, _, root := avatarFixtureTool(t, validAvatarConfig(server.URL), nil)
	files := &guard.Guard{Home: filepath.Join(root, "node"), Workspace: root, Protected: func() ([]string, error) { return nil, nil }}
	tool = avatarTool(store{filepath.Join(root, "matrix")}, files)
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"path":"avatar.png"}`)); err == nil || !strings.Contains(err.Error(), "avatar upload failed") {
		t.Fatalf("run: %v", err)
	}
}

func TestAvatarDataReportsAnUnopenableFile(t *testing.T) {
	root := t.TempDir()
	s := store{filepath.Join(root, "matrix")}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	if err := s.save(validAvatarConfig("https://example.org")); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(root, "locked.png")
	if err := os.WriteFile(locked, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	g := &guard.Guard{Home: filepath.Join(root, "node"), Workspace: root, Protected: func() ([]string, error) { return nil, nil }}
	if _, err := avatarTool(s, g).Run(context.Background(), json.RawMessage(`{"path":"locked.png"}`)); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("run: %v", err)
	}
}

func TestAvatarDataReportsAReadFailure(t *testing.T) {
	root := t.TempDir()
	s := store{filepath.Join(root, "matrix")}
	if err := s.create(); err != nil {
		t.Fatal(err)
	}
	if err := s.save(validAvatarConfig("https://example.org")); err != nil {
		t.Fatal(err)
	}
	g := &guard.Guard{Home: filepath.Join(root, "node"), Workspace: root, Protected: func() ([]string, error) { return nil, nil }}
	if _, err := avatarTool(s, g).Run(context.Background(), json.RawMessage(`{"path":"/proc/self/mem"}`)); err == nil || !strings.Contains(err.Error(), "input/output error") {
		t.Fatalf("run: %v", err)
	}
}
