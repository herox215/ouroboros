package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
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
		{Provider: "cdp", Endpoint: "http:///json/version"}, {Provider: "cdp", Endpoint: "http://[::1"}, {Provider: "cdp", Endpoint: "ftp://localhost"},
	} {
		if _, err := (&browser{}).tool().Configure(mustJSON(t, s)); err == nil {
			t.Errorf("accepted %+v", s)
		}
	}
	for _, s := range []settings{{}, {Provider: "local"}, {Provider: "cdp", Endpoint: "wss://browser.test/devtools/browser/id?token=secret"}, {Provider: "cdp", Endpoint: "ws://localhost:9222/devtools/browser/id"}, {Provider: "camofox", Endpoint: "https://browser.test/api"}} {
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

// A token with a line break never reaches a header.
func TestSettingsRefuseALineBreakInTheToken(t *testing.T) {
	for _, token := range []string{"test\nsecret", "test\rsecret"} {
		if _, err := (settings{Provider: "cdp", Endpoint: "http://localhost", Token: token}).normalized(); err == nil || err.Error() != "browser token must not contain newlines" {
			t.Errorf("%q: %v", token, err)
		}
	}
	if _, err := (settings{Provider: "camofox", Endpoint: "http://localhost", Token: "test-secret"}).normalized(); err != nil {
		t.Fatal(err)
	}
}

// A call with other settings waits for the calls that run to end, and gives
// up when its context ends, with the settings as they were.
func TestBackendSwitchWaitsForCalls(t *testing.T) {
	b := &browser{}
	b.backendMu.RLock() // a call with the local browser runs
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := b.selectBackend(ctx, settings{Provider: "cdp", Endpoint: "http://localhost"})
	b.backendMu.RUnlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if b.active != (settings{}) {
		t.Fatalf("switched to %+v", b.active)
	}
}

// What the browser of a run is with is checked before the call: settings
// that are not valid, and a token that is not there.
func TestConfiguredRunChecksSettings(t *testing.T) {
	b := newBrowser(newFake(t))
	_, err := b.configuredRun(context.Background(), settings{Provider: "chrome"}, args{Action: "read"})
	fails(t, "provider", err, `unknown browser provider "chrome"`)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("asked without its token") }))
	defer server.Close()
	const env = "OURO_BROWSER_MISSING_TEST_TOKEN"
	t.Setenv(env, "")
	os.Unsetenv(env)
	_, err = call(context.Background(), b, settings{Provider: "cdp", Endpoint: server.URL, TokenEnv: env}, `{"action":"read"}`)
	fails(t, "token", err, `browser token environment variable "OURO_BROWSER_MISSING_TEST_TOKEN" is missing or empty`)
}

// The pipe of a WebSocket ends with it.
func TestWSPipeCloses(t *testing.T) {
	up := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.Close()
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	p := &wsPipe{ws: ws}
	if n, err := p.Write([]byte("{}\x00")); n != 3 || err != nil {
		t.Fatalf("wrote %d, %v", n, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := p.Write([]byte("{}\x00")); n != 0 || err == nil {
		t.Fatalf("wrote %d to a closed pipe, %v", n, err)
	}
	if n, err := p.Read(make([]byte, 8)); n != 0 || err == nil {
		t.Fatalf("read %d of a closed pipe, %v", n, err)
	}
}

// The discovery of a remote browser fails at the step that fails, and never
// goes on to another host or to a connection without TLS.
func TestRemoteDiscoveryFailures(t *testing.T) {
	ctx := context.Background()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	for _, tc := range []struct{ name, endpoint, want string }{
		// The zone of an IPv6 address is taken as it is written, but not
		// once it is written again with the path of the discovery.
		{"zone", "http://[fe80::1%25ä]:9222", `invalid URL escape "%C3"`},
		{"unreachable", closed.URL, "CDP HTTP discovery failed (check endpoint and connectivity)"},
		{"no WebSocket", "ws" + strings.TrimPrefix(closed.URL, "http") + "/devtools/browser/id", "CDP WebSocket connection failed (check browser endpoint and authentication)"},
	} {
		if p, err := connectCDP(ctx, settings{Provider: "cdp", Endpoint: tc.endpoint}); err == nil || !strings.Contains(err.Error(), tc.want) || p != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	var advertised string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": advertised})
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	for _, ws := range []string{"http://" + host + "/devtools/browser/id", "ws://user@" + host + "/devtools/browser/id", "ws://" + host + "/devtools/browser/id#x", "ws:///devtools/browser/id", "ws://[::1"} {
		advertised = ws
		if _, err := connectCDP(ctx, settings{Provider: "cdp", Endpoint: server.URL}); err == nil || err.Error() != "invalid CDP webSocketDebuggerUrl" {
			t.Errorf("%s: %v", ws, err)
		}
	}
}

// A browser found over HTTPS is not used over a WebSocket without TLS.
func TestRemoteDiscoveryKeepsTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": "ws://" + r.Host + "/devtools/browser/id"})
	}))
	defer server.Close()
	// The discovery trusts the certificate of the test server.
	old := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = old })
	if _, err := connectCDP(context.Background(), settings{Provider: "cdp", Endpoint: server.URL}); err == nil || err.Error() != "CDP discovery would downgrade TLS" {
		t.Fatal(err)
	}
}

// The handshake of a remote browser fails for a browser that does not answer
// it, and for Firefox, which speaks too little of CDP.
func TestRemoteHandshake(t *testing.T) {
	f := newFake(t)
	server := wsBrowser(t, f)
	s := settings{Provider: "cdp", Endpoint: server.URL, Token: "test-secret"}
	f.fail("Browser.getVersion", "not now")
	_, err := connectCDP(context.Background(), s)
	if err == nil || err.Error() != "remote Chromium CDP handshake failed (Firefox/Camofox is not CDP compatible): Browser.getVersion: not now" {
		t.Fatal(err)
	}
	f.on("Browser.getVersion", func(json.RawMessage) (any, error) { return map[string]string{"product": "Firefox/128.0"}, nil })
	_, err = connectCDP(context.Background(), s)
	if err == nil || err.Error() != "the Firefox browser is not a supported CDP backend; use the native Camofox REST API" {
		t.Fatal(err)
	}
	f.on("Browser.getVersion", func(json.RawMessage) (any, error) { return map[string]string{"product": "Chrome/150.0"}, nil })
	p, err := connectCDP(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	p.end()
}

// A command that cannot be written, as the browser reads no more, ends with
// its context: the connection is closed under the write.
func TestRemoteWriteEndsWithItsContext(t *testing.T) {
	up := websocket.Upgrader{}
	reading := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer ws.Close()
		// Little is kept of what is not read, so the write of a large
		// command stops.
		ws.UnderlyingConn().(*net.TCPConn).SetReadBuffer(4 << 10)
		var m struct{ ID int64 }
		if ws.ReadJSON(&m) != nil {
			return
		}
		ws.WriteJSON(map[string]any{"id": m.ID, "result": map[string]string{"product": "Chrome/150.0"}})
		_, next, err := ws.NextReader()
		if err != nil {
			return
		}
		next.Read(make([]byte, 1))
		close(reading)
		<-release
	}))
	defer server.Close()
	defer close(release)
	p, err := connectCDP(context.Background(), settings{Provider: "cdp", Endpoint: "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/id"})
	if err != nil {
		t.Fatal(err)
	}
	defer p.end()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- p.call(ctx, "", "Runtime.evaluate", map[string]string{"expression": strings.Repeat("x", 8<<20)}, nil)
	}()
	<-reading
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("a write that cannot end: %v", err)
	}
	p.mu.Lock()
	pending := len(p.pending)
	p.mu.Unlock()
	if pending != 0 {
		t.Fatal("a command that was not written waits for its reply")
	}
}

// watched is a context that calls on with the number of every look at
// whether it ended.
type watched struct {
	context.Context
	looks int
	on    func(look int)
}

func (w *watched) Err() error {
	w.looks++
	w.on(w.looks)
	return w.Context.Err()
}

// A call that waited for another call to switch to its settings finds them
// switched, and keeps the tabs of that call.
func TestBackendSwitchedWhileWaiting(t *testing.T) {
	b := &browser{convs: map[string]*conversation{"/": {}}}
	s := settings{Provider: "cdp", Endpoint: "http://localhost"}
	b.backendMu.RLock() // a call with the local browser runs
	ctx := &watched{Context: context.Background(), on: func(look int) {
		if look == 2 { // the call waits to switch: the other one switched, and ended
			b.active = s
			b.backendMu.RUnlock()
		}
	}}
	if err := b.selectBackend(ctx, s); err != nil {
		t.Fatal(err)
	}
	b.backendMu.RUnlock()
	if b.convs["/"] == nil {
		t.Fatal("dropped the tabs of the call that switched")
	}
}

// What the discovery of a remote browser answers is read up to 1 MiB, and
// said wrong as it is.
func TestRemoteDiscoveryAnswers(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	defer server.Close()
	advertise := `{"webSocketDebuggerUrl":"ws://` + strings.TrimPrefix(server.URL, "http://") + `/devtools/browser/id"}`
	for _, tc := range []struct{ body, want string }{
		{`{}`, "CDP discovery lacks webSocketDebuggerUrl; Camofox needs provider camofox, not cdp"},
		{advertise + strings.Repeat(" ", (1<<20)+1-len(advertise)), "invalid or oversized CDP discovery response"},
		// Found: the browser at the address is then no WebSocket.
		{advertise + strings.Repeat(" ", (1<<20)-len(advertise)), "CDP WebSocket connection failed (check browser endpoint and authentication)"},
	} {
		body = tc.body
		if _, err := connectCDP(context.Background(), settings{Provider: "cdp", Endpoint: server.URL}); err == nil || err.Error() != tc.want {
			t.Errorf("%d bytes: %v", len(tc.body), err)
		}
	}
}
