package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
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

// roundTrip is a transport of these tests: it answers every request itself,
// and none reaches the network.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// replying is a client whose every answer is 200 with body.
func replying(body io.Reader) *http.Client {
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(body), Request: r}, nil
	})}
}

// nativeCamo is a Camofox server of these tests: it answers "METHOD /path"
// with the status and the body its answer holds, like "200 {}", and fails
// the test for any other request.
type nativeCamo struct {
	mu      sync.Mutex
	answers map[string]string
	asked   []string
}

// serveNative starts a nativeCamo with answers, and returns it with a
// backend that uses it.
func serveNative(t *testing.T, answers map[string]string) (*nativeCamo, *camoBackend) {
	t.Helper()
	n := &nativeCamo{answers: answers}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := r.Method + " " + r.URL.Path
		n.mu.Lock()
		n.asked = append(n.asked, route)
		answer, ok := n.answers[route]
		n.mu.Unlock()
		if !ok {
			t.Errorf("unexpected %s", route)
			http.NotFound(w, r)
			return
		}
		status, body, _ := strings.Cut(answer, " ")
		code, _ := strconv.Atoi(status)
		w.WriteHeader(code)
		io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return n, newCamo(settings{Endpoint: server.URL})
}

// answer makes the server answer route with answer from now on.
func (n *nativeCamo) answer(route, answer string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.answers[route] = answer
}

// got are the routes the server was asked, in order.
func (n *nativeCamo) got() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.asked)
}

// What a request of the native API fails with: an answer that is no JSON,
// says it failed or is too large, a status that is no success, with its
// message cut and without the token, and what is never sent.
func TestCamofoxRequestFailures(t *testing.T) {
	native, b := serveNative(t, map[string]string{
		"GET /tabs":         "200 not JSON",
		"POST /tabs":        `200 {"ok":false}`,
		"DELETE /tabs/t1":   `200 {"error":"gone"}`,
		"GET /tabs/t1/view": `200 {"ok":true,"url":"https://example.com/"}`,
		"GET /moved":        "300 {}",
	})
	b.settings.Token = "test-secret"
	ctx := context.Background()
	var out map[string]any
	_, err := b.request(ctx, http.MethodGet, "/tabs", "u", nil, &out)
	fails(t, "no JSON", err, "invalid Camofox JSON response")
	_, err = b.request(ctx, http.MethodPost, "/tabs", "u", nil, &out)
	fails(t, "not ok", err, "the Camofox server reported an unsuccessful operation")
	_, err = b.request(ctx, http.MethodDelete, "/tabs/t1", "u", nil, &out)
	fails(t, "an error", err, "the Camofox server reported an unsuccessful operation")
	_, err = b.request(ctx, http.MethodGet, "/moved", "u", nil, nil)
	fails(t, "no success", err, "the Camofox GET request returned HTTP 300")
	if _, err := b.request(ctx, http.MethodGet, "/tabs/t1/view", "u", nil, &out); err != nil || out["url"] != "https://example.com/" {
		t.Fatalf("ok: %v, %v", out, err)
	}
	// Without out the answer is handed back as it is.
	if data, err := b.request(ctx, http.MethodGet, "/tabs", "u", nil, nil); err != nil || string(data) != "not JSON" {
		t.Fatalf("raw: %q, %v", data, err)
	}

	native.answer("GET /tabs", `500 {"error":"denied for test-secret: `+strings.Repeat("x", 2000)+`"}`)
	_, err = b.request(ctx, http.MethodGet, "/tabs", "u", nil, nil)
	want := "the Camofox GET request returned HTTP 500: denied for [redacted]: " + strings.Repeat("x", 1000-len("denied for test-secret: "))
	if err == nil || err.Error() != want {
		t.Fatalf("a long error: %v", err)
	}

	asked := len(native.got())
	_, err = b.request(ctx, http.MethodPost, "/tabs", "u", map[string]any{"wait": make(chan int)}, nil)
	fails(t, "params that are no JSON", err, "unsupported type")
	if len(native.got()) != asked {
		t.Fatal("params that are no JSON were sent")
	}

	b.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := io.NopCloser(iotest.ErrReader(errors.New("connection reset")))
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body, Request: r}, nil
	})}
	_, err = b.request(ctx, http.MethodGet, "/tabs", "u", nil, nil)
	fails(t, "an answer that breaks off", err, "connection reset")
	b.client = replying(strings.NewReader(strings.Repeat("x", maxResponse+1)))
	_, err = b.request(ctx, http.MethodGet, "/tabs/t1/screenshot", "u", nil, nil)
	fails(t, "a large answer", err, "the Camofox response exceeds 16 MiB")
	b.client = replying(strings.NewReader(strings.Repeat("x", maxResponse)))
	if data, err := b.request(ctx, http.MethodGet, "/tabs/t1/screenshot", "u", nil, nil); err != nil || len(data) != maxResponse {
		t.Fatalf("an answer of 16 MiB: %d bytes, %v", len(data), err)
	}

	// Only a request with a body says that it is JSON.
	var types []string
	b.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		types = append(types, r.Method+" "+r.Header.Get("Content-Type"))
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	})}
	b.request(ctx, http.MethodGet, "/tabs", "u", nil, nil)
	b.request(ctx, http.MethodPost, "/tabs", "u", nil, nil)
	if strings.Join(types, ",") != "GET ,POST application/json" {
		t.Fatalf("content types %q", types)
	}

	// The zone of an IPv6 address is taken as it is written, but not once
	// it is written again with the path.
	_, err = newCamo(settings{Endpoint: "http://[fe80::1%25ä]:9/api"}).request(ctx, http.MethodGet, "/tabs", "u", nil, nil)
	fails(t, "an endpoint that cannot be written again", err, `invalid URL escape "%C3"`)
}

// What the native API cannot do, or the conversation does not have, is
// refused before anything is asked, and leaves the references of the page.
func TestCamofoxRefusesBeforeAsking(t *testing.T) {
	b := newCamo(settings{Endpoint: "http://camofox.test"})
	b.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		t.Errorf("asked %s %s", r.Method, r.URL)
		return nil, errors.New("not here")
	})}
	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	small := 3
	page := &camoTab{id: "t1", url: "https://example.com/", snapshot: "[1]", refs: map[int]string{1: "e1"}}
	b.conversation("one").tabs = []*camoTab{page}
	for _, tc := range []struct {
		name, key string
		ctx       context.Context
		a         args
		want      string
	}{
		{"budget", "one", ctx, args{Action: "read", MaxBytes: &small}, "max_bytes must be between 4 and 24576"},
		{"canceled", "one", canceled, args{Action: "tabs"}, "context canceled"},
		{"no tab", "empty", ctx, args{Action: "read"}, "the Camofox browser has no current tab: open a URL first"},
		{"no such tab", "one", ctx, args{Action: "switch", Tab: 2}, "there is no tab 2: the conversation has 1"},
		{"no tab 0", "one", ctx, args{Action: "switch"}, "there is no tab 0: the conversation has 1"},
		{"key", "one", ctx, args{Action: "key", Key: "F13"}, `there is no key "F13"`},
		{"scroll", "one", ctx, args{Action: "scroll", Direction: "left"}, `scroll goes down or up, not "left"`},
		{"action", "one", ctx, args{Action: "hover"}, `the Camofox backend does not support browser action "hover"`},
	} {
		if out, err := b.run(tc.ctx, tc.key, "", tc.a); err == nil || err.Error() != tc.want {
			t.Errorf("%s: %q, %v", tc.name, out, err)
		}
	}
	out, err := b.run(ctx, "empty", "", args{Action: "close"})
	is(t, "close without a tab", out, err, "there is no tab to close")
	if page.snapshot != "[1]" || page.refs[1] != "e1" {
		t.Fatal("a refused action dropped the references of the page")
	}
}

// The tabs follow what the native API lists: a tab whose page changed
// elsewhere loses its references, and a listing that cannot be trusted
// changes nothing.
func TestCamofoxListsTabs(t *testing.T) {
	native, b := serveNative(t, map[string]string{"GET /tabs": `503 {"error":"busy"}`})
	ctx := context.Background()
	c := b.conversation("one")
	page := &camoTab{id: "t1", url: "https://example.com/", snapshot: "[1]", refs: map[int]string{1: "e1"}, selects: map[int]bool{1: true}}
	c.tabs = []*camoTab{page}
	_, err := b.run(ctx, "one", "", args{Action: "tabs"})
	fails(t, "listing fails", err, "HTTP 503: busy")
	for listing, want := range map[string]string{
		`{"tabs":[{"tabId":"../t1","url":"https://example.org/"}]}`:                      "the Camofox tabs response lacks a safe tabId",
		`{"tabs":[{"tabId":"t1","url":"https://example.org/"},{"tabId":"t1","url":""}]}`: "the Camofox tabs response has a duplicate tabId",
	} {
		native.answer("GET /tabs", "200 "+listing)
		_, err := b.run(ctx, "one", "", args{Action: "tabs"})
		fails(t, listing, err, want)
	}
	if len(c.tabs) != 1 || page.url != "https://example.com/" || page.refs[1] != "e1" {
		t.Fatalf("a listing that failed changed the tabs: %+v", c.tabs)
	}
	native.answer("GET /tabs", `200 {"tabs":[{"tabId":"t1","url":"https://example.org/"}]}`)
	out, err := b.run(ctx, "one", "", args{Action: "tabs"})
	is(t, "moved", out, err, "[1] https://example.org/ (current)")
	if page.snapshot != "" || page.refs != nil || page.selects != nil {
		t.Fatalf("a page that changed kept its references: %+v", page)
	}
	c.tabs, c.current = []*camoTab{page, {id: "t2", url: "https://example.net/"}}, 1
	native.answer("GET /tabs", `200 {"tabs":[{"tabId":"t1","url":"https://example.org/"},{"tabId":"t2","url":"https://example.net/"}]}`)
	out, err = b.run(ctx, "one", "", args{Action: "tabs"})
	is(t, "the second is current", out, err, "[1] https://example.org/\n[2] https://example.net/ (current)")
}

// A tab is closed once the native API closed it, and a tab it creates needs
// an ID that is safe in a path.
func TestCamofoxOpensAndClosesTabs(t *testing.T) {
	native, b := serveNative(t, map[string]string{
		"DELETE /tabs/t1": `500 {"error":"locked"}`,
		"DELETE /tabs/t2": `200 {"ok":true}`,
		"POST /tabs":      `200 {"tabId":"../t9"}`,
	})
	ctx := context.Background()
	c := b.conversation("one")
	c.tabs, c.current = []*camoTab{{id: "t1", url: "https://example.com/"}, {id: "t2", url: "https://example.org/"}}, 1
	out, err := b.run(ctx, "one", "", args{Action: "close"})
	is(t, "a tab of two", out, err, "closed the tab; tab 1 of 1 is the current one, at https://example.com/")
	_, err = b.run(ctx, "one", "", args{Action: "close"})
	fails(t, "refused", err, "HTTP 500: locked")
	if len(c.tabs) != 1 {
		t.Fatal("a tab the native API did not close is gone")
	}
	native.answer("DELETE /tabs/t1", `200 {"ok":true}`)
	out, err = b.run(ctx, "one", "", args{Action: "close"})
	is(t, "the last tab", out, err, "closed the tab")
	if len(c.tabs) != 0 || c.current != 0 {
		t.Fatalf("tabs %v, current %d", c.tabs, c.current)
	}
	_, err = b.run(ctx, "one", "https://example.com/", args{Action: "open"})
	fails(t, "an unsafe ID", err, "the Camofox create response lacks a safe tabId")
	if len(c.tabs) != 0 {
		t.Fatal("a tab with an unsafe ID was kept")
	}
}

// After an action went through, what fails of listing the tabs or reading
// the page says that it did; a click that closed its tab says so.
func TestCamofoxAfterAnAction(t *testing.T) {
	native, b := serveNative(t, map[string]string{
		"POST /tabs/t1/click":     `200 {"ok":true}`,
		"POST /tabs/t1/back":      `200 {"ok":true}`,
		"GET /tabs":               `503 {"error":"busy"}`,
		"GET /tabs/t1/snapshot":   `500 {"error":"crashed"}`,
		"GET /tabs/t1/screenshot": `500 {"error":"no display"}`,
	})
	ctx := context.Background()
	c := b.conversation("one")
	page := &camoTab{id: "t1", url: "https://example.com/"}
	c.tabs = []*camoTab{page}
	_, err := b.run(ctx, "one", "", args{Action: "screenshot"})
	fails(t, "screenshot", err, "HTTP 500: no display")
	_, err = b.run(ctx, "one", "", args{Action: "back"})
	fails(t, "back", err, "the Camofox back succeeded, but reading the resulting page failed: the Camofox GET request returned HTTP 500: crashed")
	page.refs = map[int]string{2: "e2"}
	_, err = b.run(ctx, "one", "", args{Action: "click", Element: 2})
	fails(t, "click", err, "the Camofox click succeeded, but listing resulting tabs failed: the Camofox GET request returned HTTP 503: busy")
	native.answer("GET /tabs", `200 {"tabs":[]}`)
	page.refs = map[int]string{2: "e2"}
	out, err := b.run(ctx, "one", "", args{Action: "click", Element: 2})
	is(t, "a click that closed its tab", out, err, "Camofox click; the current tab was closed (use tabs to find remaining tabs)")
	if len(c.tabs) != 0 {
		t.Fatalf("tabs %v", c.tabs)
	}
	if got := native.got(); got[len(got)-1] != "GET /tabs" {
		t.Fatalf("read a closed tab: %v", got)
	}
}

// A screenshot of the native API is shown to the LLM, with its size in
// words.
func TestCamofoxScreenshot(t *testing.T) {
	_, server := serveCamo(t)
	b := newBrowser(newFake(t))
	s := camoSettings(server)
	tool := agent.Tool{Name: Tool, Schema: `{"type":"object"}`, Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
		if _, err := backendCall(ctx, b, s, `{"action":"open","url":"https://example.com"}`); err != nil {
			return "", err
		}
		return backendCall(ctx, b, s, string(raw))
	}}
	llm := &looking{call: `{"action":"screenshot"}`}
	if _, err := agent.New(llm, "m", "sys", tool).Run(context.Background(), "c", "look", agent.Observer{}); err != nil {
		t.Fatal(err)
	}
	if llm.result.Content != "a screenshot of https://example.com/, 2×3 pixels" {
		t.Fatalf("got %q", llm.result.Content)
	}
	if len(llm.result.Images) != 1 || llm.result.Images[0].Type != "image/png" || !bytes.HasPrefix(llm.result.Images[0].Data, []byte("\x89PNG")) {
		t.Fatalf("shown %v", llm.result.Images)
	}
}

// A snapshot is refused when it is missing, too large or its windows do not
// fit; a snapshot the server cut says so, and of its references only those
// that are numbers are taken, older ones with their role.
func TestCamofoxSnapshots(t *testing.T) {
	native, b := serveNative(t, map[string]string{})
	ctx := context.Background()
	c := b.conversation("one")
	page := &camoTab{id: "t1"}
	_, err := b.read(ctx, c, page, 0, 3)
	fails(t, "budget", err, "max_bytes must be between 4 and 24576")
	for _, tc := range []struct{ name, answer, want string }{
		{"missing", `200 {"url":"https://example.com/"}`, "the Camofox response lacks a snapshot"},
		{"window", `200 {"snapshot":"x","truncated":true,"totalChars":90000,"hasMore":true,"nextOffset":74800}`, "invalid Camofox snapshot window"},
		{"large", `200 {"snapshot":"` + strings.Repeat("x", 2<<20+1) + `"}`, "the Camofox snapshot exceeds 2 MiB"},
	} {
		native.answer("GET /tabs/t1/snapshot", tc.answer)
		_, err := b.read(ctx, c, page, 0)
		fails(t, tc.name, err, tc.want)
		if page.snapshot != "" || page.refs != nil {
			t.Fatalf("%s: kept %+v", tc.name, page)
		}
	}
	native.answer("GET /tabs/t1/snapshot", `200 {"url":"https://example.com/","snapshot":"- link \"Old\" [link e3]\n- combobox [e5]\n- combobox [e99999999999999999999]","truncated":true,"totalChars":80000}`)
	out, err := b.read(ctx, c, page, 0)
	is(t, "cut", out, err, "https://example.com/\n\n- link \"Old\" [3] link\n- combobox [5]\n- combobox [e99999999999999999999]\n[Camofox server truncated this snapshot]")
	if len(page.refs) != 2 || page.refs[3] != "e3" || page.refs[5] != "e5" || len(page.selects) != 1 || !page.selects[5] {
		t.Fatalf("refs %v, selects %v", page.refs, page.selects)
	}
	// A read from the start asks for the page again; one of 2 MiB is read.
	native.answer("GET /tabs/t1/snapshot", `200 {"snapshot":"`+strings.Repeat("x", 2<<20)+`"}`)
	out, err = b.read(ctx, c, page, 0, 4)
	is(t, "2 MiB", out, err, fmt.Sprintf("https://example.com/\n\nxxxx\n[bytes 0 to 4 of %d: continue with offset 4]", 2<<20))
}

// The address of a tab is where open went, or where it was, unless the
// snapshot says another.
func TestCamofoxKeepsTheAddressOfThePage(t *testing.T) {
	_, b := serveNative(t, map[string]string{
		"POST /tabs/t1/navigate": `200 {"ok":true}`,
		"POST /tabs/t1/back":     `200 {"ok":true}`,
		"GET /tabs/t1/snapshot":  `200 {"snapshot":"page"}`,
	})
	ctx := context.Background()
	b.conversation("one").tabs = []*camoTab{{id: "t1", url: "https://example.com/"}}
	out, err := b.run(ctx, "one", "https://example.org/", args{Action: "open"})
	is(t, "open", out, err, "Camofox open; https://example.org/\n\npage")
	out, err = b.run(ctx, "one", "", args{Action: "back"})
	is(t, "back", out, err, "Camofox back; https://example.org/\n\npage")
}

// A window of a snapshot is taken as the native API cuts it, up to its
// largest size and to its last window.
func TestCamoWindowContentTakesItsWindows(t *testing.T) {
	for _, tc := range []struct{ total, offset int }{{90000, 0}, {2 << 20, 0}, {154600, 74800}, {154601, 74800}} {
		full := strings.Repeat("a", tc.total)
		window := contractWindow(full, tc.offset)
		more := window["hasMore"].(bool)
		end := tc.total
		if more {
			end = tc.offset + 74800
		}
		if got, err := camoWindowContent(window["snapshot"].(string), tc.offset, tc.total, more, window["nextOffset"].(int)); err != nil || got != full[tc.offset:end] {
			t.Errorf("total %d, offset %d: %d bytes, %v", tc.total, tc.offset, len(got), err)
		}
	}
}

// A window of a snapshot is taken only as the native API cuts it.
func TestCamoWindowContentRefusesOtherWindows(t *testing.T) {
	const total = 90000
	window := contractWindow(strings.Repeat("a", total), 0)
	text := window["snapshot"].(string)
	if got, err := camoWindowContent(text, 0, total, true, 74800); err != nil || got != strings.Repeat("a", 74800) {
		t.Fatalf("a window as it is cut: %d bytes, %v", len(got), err)
	}
	for _, tc := range []struct {
		name                string
		text                string
		offset, total, next int
		more                bool
	}{
		{"too large", text, 0, 2<<20 + 1, 74800, true},
		{"before the start", text, -1, total, 74800, true},
		{"in the tail", strings.Repeat("a", 5000) + "\n" + strings.Repeat("a", 5000), total - 5000, total, total, false},
		{"larger than 2 MiB", contractWindow(strings.Repeat("a", 2<<20+1), 0)["snapshot"].(string), 0, 2<<20 + 1, 74800, true},
		{"no more", text, 0, total, 74800, false},
		{"next elsewhere", text, 0, total, 74801, true},
		{"shorter", text[1:], 0, total, 74800, true},
		{"another marker", strings.Replace(text, "truncated at char", "truncated at chat", 1), 0, total, 74800, true},
	} {
		if _, err := camoWindowContent(tc.text, tc.offset, tc.total, tc.more, tc.next); err == nil || err.Error() != "invalid Camofox snapshot window" {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
