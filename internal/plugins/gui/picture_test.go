package gui

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
	"sync"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/chat"
	"ouroboros/internal/guard"
	"ouroboros/internal/home"
	"ouroboros/plugin"
)

// picture returns the bytes of a small PNG, as a screenshot of a tool would
// be.
func picture(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// shownSessions is a sessions service that records the pictures a tool shows.
type shownSessions struct {
	*fakeSessions
	mu    sync.Mutex
	shown []sent
	err   error
}

func (s *shownSessions) ShowPicture(_ context.Context, id, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shown = append(s.shown, sent{id, name, ""})
	return s.err
}

// showingLLM asks for the picture tool once, and then answers.
type showingLLM struct {
	mu    sync.Mutex
	calls int
}

func (l *showingLLM) Complete(_ context.Context, _ string, _ []chat.Message, _ []chat.Tool) (chat.Message, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.calls > 1 {
		return chat.Message{Role: "assistant", Content: "there it is"}, nil
	}
	return chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{{
		ID: "c1", Type: "function",
		Function: chat.FunctionCall{Name: plugin.GatewaySendImageTool, Arguments: `{}`},
	}}}, nil
}

// The window is a channel of the pictures an agent shows: the gateway tool
// reaches it, the picture is kept in the profile, and the window serves it.
// It is shown in the thread, also when the answer that made it works in a
// session of its own, which the window does not show.
func TestAPictureIsShownThroughTheGatewayTool(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "shot.png")
	data := picture(t)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := home.Dir(filepath.Join(root, "profile"))
	store := pictures{dir: dir.Pictures()}
	files := &guard.Guard{Home: root, Workspace: root, Protected: func() ([]string, error) { return nil, nil }}
	sessions := &shownSessions{fakeSessions: &fakeSessions{}}
	out := pictureOutput(sessions, files, store)
	bot := agent.New(&showingLLM{}, "test", "show pictures", plugin.NewTool(plugin.GatewaySendImageTool, "show a picture", nil,
		func(ctx context.Context, _ struct{}) (string, error) {
			return out.SendImage(ctx, plugin.GatewayImageRequest{Path: file})
		}))
	if _, err := bot.Run(context.Background(), "s1", "show me", agent.Observer{}); err != nil {
		t.Fatal(err)
	}
	if len(sessions.shown) != 1 || sessions.shown[0].id != plugin.ThreadID || !pictureName.MatchString(sessions.shown[0].text) {
		t.Fatalf("shown %+v", sessions.shown)
	}
	name := sessions.shown[0].text
	if kept, err := os.ReadFile(filepath.Join(store.dir, name)); err != nil || !bytes.Equal(kept, data) {
		t.Fatalf("kept %d bytes, %v", len(kept), err)
	}
	res := httptest.NewRecorder()
	store.handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, mediaPath+name, nil))
	if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "image/png" || !bytes.Equal(res.Body.Bytes(), data) {
		t.Fatalf("served %d, %q, %d bytes", res.Code, res.Header().Get("Content-Type"), res.Body.Len())
	}
}

// A picture is kept once, under the name of what it is.
func TestPicturesKeepOneFilePerPicture(t *testing.T) {
	store := pictures{dir: filepath.Join(t.TempDir(), "pictures")}
	data := picture(t)
	first, err := store.keep(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.keep(data)
	if err != nil || first != second {
		t.Fatalf("kept %q and %q, %v", first, second, err)
	}
	if entries, err := os.ReadDir(store.dir); err != nil || len(entries) != 1 || entries[0].Name() != first {
		t.Fatalf("the directory holds %+v, %v", entries, err)
	}
}

// What is no picture the browser shows is refused, as is what is too large.
func TestPicturesRefuseWhatTheyCannot(t *testing.T) {
	store := pictures{dir: t.TempDir()}
	if _, err := store.keep(nil); err == nil {
		t.Fatal("an empty picture was kept")
	}
	if _, err := store.keep([]byte("<html>not a picture</html>")); err == nil {
		t.Fatal("what is no picture was kept")
	}
	big := make([]byte, maxPictureBytes+1)
	copy(big, picture(t))
	if _, err := store.keep(big); err == nil {
		t.Fatal("a picture that is too large was kept")
	}
}

// The window serves the pictures it kept, and nothing else: no other name,
// no path, and nothing that is not a file there.
func TestPicturesServeOnlyThePictures(t *testing.T) {
	store := pictures{dir: t.TempDir()}
	name, err := store.keep(picture(t))
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(store.dir, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/app.js",
		mediaPath,
		mediaPath + "..%2Fapp.js",
		mediaPath + strings.Repeat("a", 64) + ".png", // not kept
		mediaPath + name + "/x",
	} {
		res := httptest.NewRecorder()
		store.handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != http.StatusNotFound {
			t.Fatalf("%s is served as %d", path, res.Code)
		}
	}
}

// A picture is shown in the conversation that works, and only through a node
// that can show it.
func TestShowPictureNeedsAPathOrAnAttachment(t *testing.T) {
	store := pictures{dir: t.TempDir()}
	out := pictureOutput(&fakeSessions{}, nil, store)
	if _, err := out.SendImage(context.Background(), plugin.GatewayImageRequest{}); err == nil || !strings.Contains(err.Error(), "either a path or an attachment") {
		t.Fatalf("nothing to show: %v", err)
	}
	if _, err := out.SendImage(context.Background(), plugin.GatewayImageRequest{Path: "a.png", Attachment: 1}); err == nil || !strings.Contains(err.Error(), "either a path or an attachment") {
		t.Fatalf("both a path and an attachment: %v", err)
	}
}

// The result of the call is what the agent is told: the name it can show
// again, not the picture.
func TestShowPictureAnswersWithTheName(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "shot.png")
	data := picture(t)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := pictures{dir: filepath.Join(root, "pictures")}
	files := &guard.Guard{Home: root, Workspace: root, Protected: func() ([]string, error) { return nil, nil }}
	sessions := &shownSessions{fakeSessions: &fakeSessions{}}
	out := pictureOutput(sessions, files, store)
	var result string
	bot := agent.New(&showingLLM{}, "test", "show pictures", plugin.NewTool(plugin.GatewaySendImageTool, "show a picture", nil,
		func(ctx context.Context, _ struct{}) (string, error) {
			var err error
			result, err = out.SendImage(ctx, plugin.GatewayImageRequest{Path: file})
			return result, err
		}))
	if _, err := bot.Run(context.Background(), "s1", "show me", agent.Observer{}); err != nil {
		t.Fatal(err)
	}
	var told struct {
		Picture string `json:"picture"`
		Bytes   int    `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(result), &told); err != nil {
		t.Fatalf("the result %q: %v", result, err)
	}
	if told.Picture != sessions.shown[0].text || told.Bytes != len(data) {
		t.Fatalf("told %+v, shown %+v", told, sessions.shown)
	}
}
