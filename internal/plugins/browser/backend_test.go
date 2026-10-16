package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"ouroboros/internal/a2a"
)

func TestBackendSettings(t *testing.T) {
	for _, s := range []settings{
		{Provider: "firefox"}, {Provider: "cdp"}, {Provider: "camofox", Endpoint: "ws://localhost"},
		{Endpoint: "http://localhost"}, {Provider: "cdp", Endpoint: "file:///tmp/browser"},
		{Provider: "cdp", Endpoint: "http://secret@localhost"}, {Provider: "cdp", Endpoint: "http://localhost", Visible: true},
		{Provider: "cdp", Endpoint: "http://localhost", TokenEnv: "not a variable"},
	} {
		if _, err := (&browser{}).tool().Configure(mustJSON(t, s)); err == nil {
			t.Errorf("accepted %+v", s)
		}
	}
	for _, s := range []settings{{}, {Provider: "local"}, {Provider: "cdp", Endpoint: "wss://browser.test/devtools/browser/id?token=secret"}, {Provider: "camofox", Endpoint: "https://browser.test/api"}} {
		if _, err := (&browser{}).tool().Configure(mustJSON(t, s)); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	// Configurable contributes these fields to the existing settings UI.
	options := mustJSON(t, (&browser{}).tool().Options)
	for _, name := range []string{"provider", "endpoint", "token_env", "visible"} {
		if !strings.Contains(string(options), name) {
			t.Errorf("missing UI setting %s", name)
		}
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// wsBrowser speaks the same CDP messages as the existing pipe fake. The
// hosted process remains outside the adapter's ownership.
func wsBrowser(t *testing.T, f *fake) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	up := websocket.Upgrader{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("missing backend auth: %q", r.Header.Get("Authorization"))
			w.WriteHeader(401)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/json/version") {
			json.NewEncoder(w).Encode(map[string]any{"webSocketDebuggerUrl": "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/test"})
			return
		}
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.Close()
		for {
			var m struct {
				ID        int64
				Method    string
				Params    json.RawMessage
				SessionID string
			}
			if ws.ReadJSON(&m) != nil {
				return
			}
			send := func(v any) {
				if err := ws.WriteJSON(v); err != nil {
					return
				}
			}
			result, err := f.answer(m.Method, m.Params, func(method string, params any) {
				send(map[string]any{"method": method, "params": params, "sessionId": m.SessionID})
			})
			if errors.Is(err, errSilent) {
				continue
			}
			if err != nil {
				send(map[string]any{"id": m.ID, "error": err})
			} else {
				send(map[string]any{"id": m.ID, "result": result})
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRemoteCDPOwnershipAndIsolation(t *testing.T) {
	f := newFake(t)
	var next int
	f.on("Target.createBrowserContext", func(json.RawMessage) (any, error) {
		next++
		return map[string]any{"browserContextId": fmt.Sprintf("C%d", next)}, nil
	})
	server := wsBrowser(t, f)
	b := newBrowser(f)
	s := settings{Provider: "cdp", Endpoint: server.URL, Token: "test-secret"}
	ctx := context.Background()
	out, err := backendCall(ctx, b, s, `{"action":"open","url":"https://example.com"}`)
	if err != nil || !strings.Contains(out, "Hello") {
		t.Fatalf("open: %q %v", out, err)
	}
	peer := a2a.WithOrigin(ctx, a2a.Origin{PublicKey: "peer"})
	if _, err := backendCall(peer, b, s, `{"action":"read"}`); err != nil {
		t.Fatal(err)
	}
	owner2, ownerProcess, err := b.conversation(ctx, "/owner-2", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner2.ready(ctx, ownerProcess)
	owner2.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if next != 3 {
		t.Fatalf("contexts=%d, want owner and peer conversations isolated", next)
	}
	if len(f.started) != 0 {
		t.Fatal("remote fell back to local")
	}
	f.mu.Lock()
	for _, p := range f.params["Browser.setDownloadBehavior"] {
		var v map[string]any
		json.Unmarshal(p, &v)
		if v["browserContextId"] == nil {
			t.Error("changed hosted default context")
		}
	}
	f.mu.Unlock()
	if slices.Contains(f.got(), "Target.getTargets") {
		t.Fatal("discovered/adopted hosted tabs")
	}
	if _, err := backendCall(ctx, b, s, `{"action":"close"}`); err != nil {
		t.Fatal(err)
	}
	b.close()
	if slices.Contains(f.got(), "Browser.close") {
		t.Fatal("stopped hosted browser")
	}
	if !slices.Contains(f.got(), "Target.disposeBrowserContext") {
		t.Fatal("did not clean owned contexts")
	}
	// The endpoint remains usable after disconnect.
	p, err := connectCDP(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	p.end()
}

func TestRemoteCDPDirectAndCancellation(t *testing.T) {
	f := newFake(t)
	server := wsBrowser(t, f)
	p, err := connectCDP(context.Background(), settings{Provider: "cdp", Endpoint: "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/id", Token: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.end()
	f.on("Silent", func(json.RawMessage) (any, error) { return nil, errSilent })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := p.call(ctx, "", "Silent", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
	p.mu.Lock()
	pending := len(p.pending)
	p.mu.Unlock()
	if pending != 0 {
		t.Fatal("canceled request retained")
	}
	if err := p.call(context.Background(), "", "Browser.getVersion", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteDiscoveryNeverFallsBack(t *testing.T) {
	f := newFake(t)
	for _, body := range []string{`{}`, `{"webSocketDebuggerUrl":"ws://public.invalid/devtools/browser/id"}`, strings.Repeat("x", (1<<20)+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		b := newBrowser(f)
		_, err := backendCall(context.Background(), b, settings{Provider: "cdp", Endpoint: server.URL}, `{"action":"read"}`)
		if err == nil {
			t.Fatal("bad discovery accepted")
		}
		server.Close()
	}
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("followed redirect") }))
	defer destination.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer server.Close()
	_, err := backendCall(context.Background(), newBrowser(f), settings{Provider: "cdp", Endpoint: server.URL}, `{"action":"read"}`)
	if err == nil {
		t.Fatal("redirect accepted")
	}
	if len(f.started) != 0 {
		t.Fatal("fallback to local")
	}
}

func TestEndpointSwitchDrainsAndDropsTabs(t *testing.T) {
	f := newFake(t)
	first := wsBrowser(t, f)
	second := wsBrowser(t, f)
	b := newBrowser(f)
	ctx := context.Background()
	s := settings{Provider: "cdp", Endpoint: first.URL, Token: "test-secret"}
	if _, err := backendCall(ctx, b, s, `{"action":"read"}`); err != nil {
		t.Fatal(err)
	}
	s.Endpoint = second.URL
	out, err := backendCall(ctx, b, s, `{"action":"tabs"}`)
	is(t, "no old tabs at new endpoint", out, err, "the conversation has no tab")
	if slices.Contains(f.got(), "Browser.close") {
		t.Fatal("switch stopped server")
	}
}

func TestBackendLockCancellation(t *testing.T) {
	b := &browser{}
	b.backendMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := b.selectBackend(ctx, settings{Provider: "cdp", Endpoint: "http://localhost"})
	b.backendMu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	var mu sync.Mutex
	mu.Lock()
	if err := waitLock(ctx, mu.TryLock); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	mu.Unlock()
}

// Exercise adapter lifecycle directly; configured-tool coexistence and secret
// resolution are covered separately by instances_test and auth_test.
func backendCall(ctx context.Context, b *browser, s settings, raw string) (string, error) {
	var a args
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return "", err
	}
	return b.run(ctx, s, a)
}
