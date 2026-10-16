package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/a2a"
)

type camoRequest struct {
	method, path, user string
	params             map[string]any
}
type camoFake struct {
	mu       sync.Mutex
	requests []camoRequest
	users    map[string]string // tabId -> owner
	next     int
	snapshot string
	hook     func(http.ResponseWriter, *http.Request) bool
}

func serveCamo(t *testing.T) (*camoFake, *httptest.Server) {
	t.Helper()
	f := &camoFake{users: map[string]string{}, snapshot: "- textbox \"Name\" [e1]\n- button \"Submit\" [e2]"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("missing token: %q", r.Header.Get("Authorization"))
			w.WriteHeader(401)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Query().Get("tenant") != "test" {
			t.Errorf("lost endpoint base path/query: %s", r.URL)
			w.WriteHeader(400)
			return
		}
		user := r.URL.Query().Get("userId")
		params := map[string]any{}
		if r.Method != "GET" {
			if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
				t.Error(err)
			}
			if params["userId"] != user {
				t.Error("userId not consistently supplied")
			}
		}
		if user == "" {
			t.Error("missing user identity")
		}
		f.mu.Lock()
		f.requests = append(f.requests, camoRequest{r.Method, r.URL.Path, user, params})
		hook := f.hook
		f.mu.Unlock()
		if hook != nil && hook(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/api")
		if r.Method == "POST" && path == "/tabs" {
			f.next++
			id := fmt.Sprintf("t%d", f.next)
			f.users[id] = user
			json.NewEncoder(w).Encode(map[string]any{"tabId": id, "url": params["url"]})
			return
		}
		if strings.HasPrefix(path, "/sessions/") && r.Method == "DELETE" {
			if strings.TrimPrefix(path, "/sessions/") != user {
				t.Error("cleanup touched unowned session")
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}

		if r.Method == "GET" && path == "/tabs" {
			tabs := []map[string]any{}
			for id, owner := range f.users {
				if owner == user {
					tabs = append(tabs, map[string]any{"tabId": id, "url": "https://example.com/"})
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"tabs": tabs})
			return
		}
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) < 2 || parts[0] != "tabs" || f.users[parts[1]] != user {
			t.Errorf("unowned route %s by %s", path, user)
			w.WriteHeader(404)
			return
		}
		if len(parts) == 2 && r.Method == "DELETE" {
			delete(f.users, parts[1])
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		switch parts[2] {
		case "snapshot":
			json.NewEncoder(w).Encode(map[string]any{"url": "https://example.com/", "snapshot": f.snapshot})
		case "screenshot":
			w.Header().Set("Content-Type", "image/png")
			png.Encode(w, image.NewRGBA(image.Rect(0, 0, 2, 3)))
		case "click", "type", "press", "scroll", "navigate", "back":
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			t.Errorf("unexpected native API route %s", path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return f, server
}
func (f *camoFake) got() []camoRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]camoRequest(nil), f.requests...)
}
func camoSettings(server *httptest.Server) settings {
	return settings{Provider: "camofox", Endpoint: server.URL + "/api?tenant=test", Token: "test-secret"}
}

func TestCamofoxNativeActionsAndIsolation(t *testing.T) {
	local := newFake(t)
	f, server := serveCamo(t)
	b := newBrowser(local)
	s := camoSettings(server)
	ctx := context.Background()
	out, err := backendCall(ctx, b, s, `{"action":"open","url":"https://example.com"}`)
	if err != nil || !strings.Contains(out, `[1]`) || strings.Contains(out, `[e1]`) {
		t.Fatalf("snapshot: %q %v", out, err)
	}
	for _, a := range []string{
		`{"action":"click","element":2}`,
		`{"action":"type","element":1,"text":"replacement","enter":true}`,
		`{"action":"key","key":"Space"}`,
		`{"action":"scroll","direction":"up"}`,
		`{"action":"back"}`,
		`{"action":"open","url":"https://example.org"}`,
		`{"action":"open","url":"https://example.com","new_tab":true}`,
		`{"action":"switch","tab":1}`,
	} {
		if _, err := backendCall(ctx, b, s, a); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
	}
	out, err = backendCall(ctx, b, s, `{"action":"tabs"}`)
	if err != nil || !strings.Contains(out, "[2]") {
		t.Fatalf("tabs: %s %v", out, err)
	}
	peer := a2a.WithOrigin(ctx, a2a.Origin{PublicKey: "peer"})
	if _, err := backendCall(peer, b, s, `{"action":"open","url":"https://example.com"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.camo.run(ctx, "second-owner-conversation", "https://example.com", args{Action: "open"}); err != nil {
		t.Fatal(err)
	}
	users := map[string]bool{}
	for _, r := range f.got() {
		if r.method == "POST" && r.path == "/api/tabs" {
			users[r.user] = true
			if r.params["sessionKey"] != "ouro" {
				t.Error("no sessionKey")
			}
		}
		switch r.path {
		case "/api/tabs/t1/click":
			if r.params["ref"] != "e2" {
				t.Errorf("click ref: %v", r.params)
			}
		case "/api/tabs/t1/type":
			if r.params["ref"] != "e1" || r.params["text"] != "replacement" || r.params["mode"] != "fill" || r.params["pressEnter"] != true {
				t.Errorf("type contract: %v", r.params)
			}
		case "/api/tabs/t1/press":
			if r.params["key"] != " " {
				t.Error("Space not mapped")
			}
		}
	}
	if len(users) != 3 {
		t.Fatalf("owner/peer conversations share contexts: %v", users)
	}
	if len(local.started) != 0 {
		t.Fatal("Camofox launched Chromium")
	}
	for _, a := range []string{`{"action":"dialog","accept":true}`, `{"action":"console"}`, `{"action":"open","element":1}`, `{"action":"open","url":"https://example.com","visible":false}`} {
		if _, err := backendCall(ctx, b, s, a); err == nil {
			t.Errorf("unsupported action accepted: %s", a)
		}
	}
	if _, err := backendCall(ctx, b, s, `{"action":"click","element":999}`); err == nil {
		t.Fatal("invented ref accepted")
	}
	if _, err := backendCall(ctx, b, s, `{"action":"close"}`); err != nil {
		t.Fatal(err)
	}
	b.close()
	cleaned := map[string]bool{}
	for _, r := range f.got() {
		if strings.HasPrefix(r.path, "/api/sessions/") {
			cleaned[r.user] = true
		}
		if strings.HasSuffix(r.path, "/stop") {
			t.Fatal("stopped hosted engine")
		}
	}
	if len(cleaned) != len(users) {
		t.Fatalf("session cleanup: %v", cleaned)
	}
}

func TestCamofoxPaginationUsesNativeCharacterOffsets(t *testing.T) {
	local := newFake(t)
	f, server := serveCamo(t)
	b := newBrowser(local)
	s := camoSettings(server)
	first := strings.Repeat("é", maxPage/2) + " [e12]"
	second := "\n- link [e15] End"
	var offsets []int
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/snapshot") {
			return false
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		offsets = append(offsets, offset)
		if offset == 0 {
			json.NewEncoder(w).Encode(map[string]any{"snapshot": first, "hasMore": true, "nextOffset": 12345})
		} else if offset == 12345 {
			json.NewEncoder(w).Encode(map[string]any{"snapshot": second, "hasMore": false})
		} else {
			t.Errorf("wrong native offset %d", offset)
		}
		return true
	}
	out, err := backendCall(context.Background(), b, s, `{"action":"open","url":"https://example.com"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, fmt.Sprintf("continue with offset %d", maxPage)) {
		t.Fatalf("no local byte continuation: %q", out[len(out)-100:])
	}
	more, err := backendCall(context.Background(), b, s, fmt.Sprintf(`{"action":"read","offset":%d}`, maxPage))
	if err != nil || !strings.Contains(more, "[12]") || !strings.Contains(more, "[15]") {
		t.Fatalf("continuation: %s %v", more, err)
	}
	if len(offsets) != 2 || offsets[1] != 12345 {
		t.Fatalf("unnecessary refresh / wrong offsets: %v", offsets)
	}
	c := b.camo.conversation("/")
	if c.tabs[0].refs[15] != "e15" {
		t.Fatal("pagination lost refs")
	}
	if _, err := backendCall(context.Background(), b, s, `{"action":"read","offset":1}`); err == nil {
		t.Fatal("split UTF-8 accepted")
	}
}

func TestCamofoxFailureCancellationAndSwitch(t *testing.T) {
	local := newFake(t)
	f, server := serveCamo(t)
	b := newBrowser(local)
	s := camoSettings(server)
	entered := make(chan struct{})
	release := make(chan struct{})
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/snapshot") {
			close(entered)
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return true
		}
		return false
	}
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := backendCall(ctx, b, s, `{"action":"open","url":"https://example.com"}`); done <- err }()
	<-entered
	_, second := serveCamo(t)
	switched := make(chan error, 1)
	go func() {
		out, err := backendCall(context.Background(), b, camoSettings(second), `{"action":"tabs"}`)
		if err == nil && out != "the conversation has no tab" {
			err = fmt.Errorf("old tabs crossed endpoint: %s", out)
		}
		switched <- err
	}()
	select {
	case err := <-switched:
		t.Fatalf("switched during a call: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := <-switched; err != nil {
		t.Fatal(err)
	}
	if len(local.started) != 0 {
		t.Fatal("fallback")
	}
	cleaned := false
	for _, r := range f.got() {
		cleaned = cleaned || strings.HasPrefix(r.path, "/api/sessions/")
	}
	if !cleaned {
		t.Fatal("old endpoint resources leaked")
	}
}

func TestCamofoxErrorsAndScreenshotValidation(t *testing.T) {
	local := newFake(t)
	f, server := serveCamo(t)
	b := newBrowser(local)
	s := camoSettings(server)
	ctx := context.Background()
	if _, err := backendCall(ctx, b, s, `{"action":"open","url":"https://example.com"}`); err != nil {
		t.Fatal(err)
	}
	_, err := backendCall(ctx, b, s, `{"action":"screenshot"}`)
	fails(t, "valid native PNG reaches image sink", err, "only to an agent")
	f.mu.Lock()
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/screenshot") {
			w.Write([]byte("not a PNG"))
			return true
		}
		if strings.HasSuffix(r.URL.Path, "/click") {
			w.WriteHeader(409)
			fmt.Fprint(w, `{"error":"stale refs"}`)
			return true
		}
		return false
	}
	f.mu.Unlock()
	_, err = backendCall(ctx, b, s, `{"action":"screenshot"}`)
	fails(t, "invalid screenshot", err, "valid PNG")
	_, err = backendCall(ctx, b, s, `{"action":"click","element":1}`)
	fails(t, "native error", err, "HTTP 409: stale refs")
	before := len(f.got())
	_, err = backendCall(ctx, b, s, `{"action":"type","element":1,"text":"never replay"}`)
	fails(t, "invalidated refs after failed mutation", err, "read the page first")
	if len(f.got()) != before {
		t.Fatal("reused refs after ambiguous mutation")
	}
}

func TestCamofoxNoRedirectOrMalformedSnapshot(t *testing.T) {
	local := newFake(t)
	f, server := serveCamo(t)
	b := newBrowser(local)
	s := camoSettings(server)
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/snapshot") {
			fmt.Fprint(w, `{"snapshot":"x","hasMore":true,"nextOffset":0}`)
			return true
		}
		return false
	}
	_, err := backendCall(context.Background(), b, s, `{"action":"open","url":"https://example.com"}`)
	fails(t, "stuck pagination", err, "did not advance")
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("followed redirect") }))
	defer destination.Close()
	f.mu.Lock()
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		http.Redirect(w, r, destination.URL, http.StatusFound)
		return true
	}
	f.mu.Unlock()
	_, err = backendCall(context.Background(), b, s, `{"action":"read"}`)
	fails(t, "redirect", err, "HTTP 302")
	if len(local.started) != 0 {
		t.Fatal("local fallback")
	}
}

// Keep PNG generation honest without requiring a live hosted Firefox engine.
func TestCamofoxPNGFixture(t *testing.T) {
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	if _, err := png.DecodeConfig(&data); err != nil {
		t.Fatal(err)
	}
}
