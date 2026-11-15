package gui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/web"
)

// pageFor returns a page of the thread in a home dir that is enabled for the
// user "owner" with the password "correct horse battery", and its server.
func pageFor(t *testing.T) (*http.Client, *httptest.Server) {
	t.Helper()
	dir := home.Dir(t.TempDir())
	hash, err := web.Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(string(dir), config.Config{Model: "m",
		Web: config.Web{Enabled: true, User: "owner", Password: hash}}); err != nil {
		t.Fatal(err)
	}
	front := newWebFront(dir, fakeProfile("pythia"))
	server := httptest.NewServer(front.handler)
	t.Cleanup(server.Close)
	return stillClient(), server
}

// stillClient is a client that does not follow the redirect of a login, so
// that the cookie it sets can be read from the response itself.
func stillClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// signIn posts the form and returns the response.
func signIn(t *testing.T, client *http.Client, base, user, password string) *http.Response {
	t.Helper()
	res, err := client.PostForm(base+"/login", url.Values{"user": {user}, "password": {password}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// session is the cookie of the login in res.
func session(t *testing.T, res *http.Response) *http.Cookie {
	t.Helper()
	for _, cookie := range res.Cookies() {
		if cookie.Name == sessionCookie {
			return cookie
		}
	}
	return nil
}

// do makes a request with the session of a login, if there is one.
func do(t *testing.T, client *http.Client, method, raw string, cookie *http.Cookie, header map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for name, value := range header {
		req.Header.Set(name, value)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func body(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A page that nobody is signed in to sends the browser to the login, and its
// API answers no browser at all.
func TestPageNeedsALogin(t *testing.T) {
	client, server := pageFor(t)
	// The files of the page are the same code for everyone, and the worker
	// of a phone cannot cache them through a redirect.
	if res, err := client.Get(server.URL + "/app.js"); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("app.js: %v, %v", res.StatusCode, err)
	}
	if res := do(t, client, "GET", server.URL+"/", nil, nil); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/login" {
		t.Fatalf("the page without a login answers %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	if res := do(t, client, "GET", server.URL+"/login", nil, nil); res.StatusCode != http.StatusOK {
		t.Fatalf("the login form answers %d", res.StatusCode)
	} else if text := body(t, res); !strings.Contains(text, `rel="manifest"`) {
		// A phone installs the app from the page it is on, and a browser
		// without a session sees only this one.
		t.Fatalf("the login form does not let a phone install the app: %q", text)
	}
	res := do(t, client, "GET", server.URL+"/api/initial", nil, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the API without a login answers %d", res.StatusCode)
	}
	if text := body(t, res); !strings.Contains(text, "signed out") {
		t.Fatalf("the API says %q", text)
	}
}

// The login asks for what the configuration keeps, and lets the browser in
// with a cookie only when both are right.
func TestLoginAndCookie(t *testing.T) {
	client, server := pageFor(t)
	res := signIn(t, client, server.URL, "owner", "wrong password")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a wrong password is answered %d", res.StatusCode)
	}
	if text := body(t, res); !strings.Contains(text, "Wrong user or password") {
		t.Fatalf("the form does not say what was wrong: %q", text)
	}
	if cookie := session(t, res); cookie != nil {
		t.Fatal("a wrong password got a session")
	}

	res = signIn(t, client, server.URL, "owner", "correct horse battery")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("the right password is answered %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	cookie := session(t, res)
	if cookie == nil || !cookie.HttpOnly || cookie.Value == "" {
		t.Fatalf("no session cookie, or one a script reads: %v", res.Header.Values("Set-Cookie"))
	}

	page := do(t, client, "GET", server.URL+"/", cookie, nil)
	text := body(t, page)
	if page.StatusCode != http.StatusOK || !strings.Contains(text, "./shim.js") || !strings.Contains(text, "./app.js") {
		t.Fatalf("the page is answered %d: %q", page.StatusCode, text)
	}
	initial := do(t, client, "GET", server.URL+"/api/initial", cookie, nil)
	if state := body(t, initial); !strings.Contains(state, `"profile":"pythia"`) || !strings.Contains(state, `"thread":"owner/`) {
		t.Fatalf("the page is told %q", state)
	}
}

// A change has to come from the page itself: the header is what a form of
// another site cannot set.
func TestSendNeedsThePage(t *testing.T) {
	client, server := pageFor(t)
	res := signIn(t, client, server.URL, "owner", "correct horse battery")
	cookie := session(t, res)
	if cookie == nil {
		t.Fatalf("no session: %d", res.StatusCode)
	}
	post := do(t, client, "POST", server.URL+"/api/send", cookie, nil)
	if post.StatusCode != http.StatusForbidden {
		t.Fatalf("a message without the header is answered %d", post.StatusCode)
	}
	post.Body.Close()
}

// An event of the stream is dropped by EventSource when it has no data line at
// all: the two that say everything with their name need an empty one.
func TestStreamEventsCarryData(t *testing.T) {
	var out strings.Builder
	if err := writeEvent(&out, sseEvent{event: "ended"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "data:") || !strings.Contains(got, "event: ended") {
		t.Fatalf("an event without a body is written as %q", got)
	}
	out.Reset()
	if err := writeEvent(&out, sseEvent{event: "events", data: []byte("[]")}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "data: []") {
		t.Fatalf("the body of an event is written as %q", got)
	}
}

// A tab names itself, so that two tabs of one browser do not share one
// history cursor: the header names it on the requests of the app, the query on
// the stream, and a request that names no tab gets a window of its own.
func TestEachTabNamesItself(t *testing.T) {
	front := newWebFront(home.Dir(t.TempDir()), fakeProfile("pythia"))
	tab := func(request *http.Request) *webClient {
		t.Helper()
		return front.client(httptest.NewRecorder(), request)
	}
	named := func(path, id string, header bool) *webClient {
		t.Helper()
		request := httptest.NewRequest("GET", path, nil)
		if header {
			request.Header.Set(clientHeader, id)
		}
		client := tab(request)
		if client == nil {
			t.Fatalf("no window for %q", path)
		}
		return client
	}
	first, second := strings.Repeat("a", 43), strings.Repeat("b", 43)
	if client := named("/api/initial", first, true); client.id != first {
		t.Fatalf("the header named the tab %q", client.id)
	}
	if client := named("/api/events?client="+second, "", false); client.id != second {
		t.Fatalf("the query named the tab %q", client.id)
	}
	if front.clients.size() != 2 {
		t.Fatalf("two tabs share a window: %d windows", front.clients.size())
	}
	// The same tab says the same name again, and keeps its window.
	if client := named("/api/history?before=1", first, true); client.id != first {
		t.Fatalf("the first tab is now %q", client.id)
	}
	if front.clients.size() != 2 {
		t.Fatalf("a tab got a second window: %d", front.clients.size())
	}
	// A request that says nothing is named here, and told so in a cookie.
	request := httptest.NewRequest("GET", "/api/initial", nil)
	recorder := httptest.NewRecorder()
	if client := front.client(recorder, request); client == nil || !validClientID(client.id) {
		t.Fatalf("a request without a name got %v", client)
	} else if cookie := recorder.Result().Cookies(); len(cookie) != 1 || cookie[0].Name != clientCookie {
		t.Fatalf("a request without a name is told no name: %v", recorder.Header())
	}
}

// A tab with a stream open is not idle, however quiet the thread is: reaping
// it would take the window its stream is on away.
func TestReapSparesATabWithAStream(t *testing.T) {
	front := newWebFront(home.Dir(t.TempDir()), fakeProfile("pythia"))
	request := httptest.NewRequest("GET", "/api/initial", nil)
	request.Header.Set(clientHeader, strings.Repeat("a", 43))
	client := front.client(httptest.NewRecorder(), request)
	stream, cancel := client.subscribe()
	defer cancel()
	client.mu.Lock()
	client.last = time.Now().Add(-3 * webIdle)
	client.mu.Unlock()
	front.clients.reap(webIdle)
	if front.clients.size() != 1 {
		t.Fatal("a tab with a stream open was reaped")
	}
	cancel()
	// The idle time counts from when the stream ended.
	front.clients.reap(webIdle)
	if front.clients.size() != 1 {
		t.Fatal("a tab whose stream just ended was reaped")
	}
	client.mu.Lock()
	client.last = time.Now().Add(-3 * webIdle)
	client.mu.Unlock()
	front.clients.reap(webIdle)
	if front.clients.size() != 0 {
		t.Fatal("a tab that said nothing for hours is kept")
	}
	if _, ok := <-stream; ok {
		t.Fatal("the stream of a tab that is let go is still open")
	}
}

// Closing the page ends the streams of the browsers on it: a stream that stays
// open keeps its request, and the goroutine that reads it, for good.
func TestCloseEndsTheStreams(t *testing.T) {
	front := newWebFront(home.Dir(t.TempDir()), fakeProfile("pythia"))
	request := httptest.NewRequest("GET", "/api/initial", nil)
	request.Header.Set(clientHeader, strings.Repeat("a", 43))
	client := front.client(httptest.NewRecorder(), request)
	stream, cancel := client.subscribe()
	defer cancel()
	front.clients.closeAll()
	if _, ok := <-stream; ok {
		t.Fatal("the stream of a closed page is still open")
	}
	if front.clients.size() != 0 {
		t.Fatalf("%d windows are left after closing the page", front.clients.size())
	}
}

// What the thread told while no stream was open is in the snapshot of an Open,
// and is dropped with it: a stream that replayed it would show it twice.
func TestForgetDropsTheQueue(t *testing.T) {
	front := newWebFront(home.Dir(t.TempDir()), fakeProfile("pythia"))
	request := httptest.NewRequest("GET", "/api/initial", nil)
	request.Header.Set(clientHeader, strings.Repeat("a", 43))
	client := front.client(httptest.NewRecorder(), request)
	client.broadcast(sseEvent{event: "events", data: []byte("[]")})
	client.forget()
	stream, cancel := client.subscribe()
	defer cancel()
	select {
	case message := <-stream:
		t.Fatalf("an event the snapshot holds came again: %+v", message)
	default:
	}
}

// The caller of a login is the address it came from, also behind the proxy of
// the tailnet, where every request comes from the host itself.
func TestCallerKeyNamesTheCaller(t *testing.T) {
	named := func(remote string, forwarded ...string) (string, string) {
		t.Helper()
		request := httptest.NewRequest("POST", "/login", nil)
		request.RemoteAddr = remote
		for _, line := range forwarded {
			request.Header.Add("X-Forwarded-For", line)
		}
		caller, key := callerKey(request, "owner")
		if caller != "" && key != caller+"|owner" {
			t.Fatalf("the key %q is not the caller %q and the name", key, caller)
		}
		return caller, key
	}
	// A request of the host that names no caller is everyone behind the
	// proxy: it has no address of its own to count, only the name.
	if got, key := named("127.0.0.1:1234"); got != "" || key != "127.0.0.1|owner" {
		t.Fatalf("a caller of the host is named %q, with the key %q", got, key)
	}
	// A proxy that adds a line of its own puts it after the caller's.
	if got, _ := named("127.0.0.1:1234", "203.0.113.9", "100.64.0.7"); got != "100.64.0.7" {
		t.Fatalf("a forwarded line the caller wrote is used: %q", got)
	}
	// An address with a port is still the address.
	if got, _ := named("127.0.0.1:1234", "100.64.0.7:41000"); got != "100.64.0.7" {
		t.Fatalf("a forwarded address with a port is named %q", got)
	}
	// What the proxy appends is what it saw, and the last entry is its own.
	if got, _ := named("127.0.0.1:1234", "100.64.0.7"); got != "100.64.0.7" {
		t.Fatalf("a caller behind the proxy is named %q", got)
	}
	// A caller can write the entries before the one the proxy appended, to
	// pass for another address or to hold the owner up. It is not believed.
	if got, _ := named("127.0.0.1:1234", "203.0.113.9, 100.64.0.7"); got != "100.64.0.7" {
		t.Fatalf("a forwarded entry the caller wrote is used: %q", got)
	}
	if got, _ := named("127.0.0.1:1234", "not an address"); got != "" {
		t.Fatalf("what is no address is taken for one: %q", got)
	}
	// What a request from the network says about its caller is not believed
	// either: it would let one address pass for another, and block it.
	if got, _ := named("100.64.0.9:5555", "203.0.113.9"); got != "100.64.0.9" {
		t.Fatalf("a caller of the network is named %q", got)
	}
}

// A spray of names from one address is held up as an address, and not only as
// a name: otherwise every name starts a count of its own and the node hashes
// for as long as the spray lasts.
func TestLoginHoldsUpAnAddressWhateverNameItTries(t *testing.T) {
	dir := home.Dir(t.TempDir())
	hash, err := web.Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(string(dir), config.Config{Model: "m",
		Web: config.Web{Enabled: true, User: "owner", Password: hash}}); err != nil {
		t.Fatal(err)
	}
	front := newWebFront(dir, fakeProfile("pythia"))
	front.callers = web.NewThrottle(2, time.Minute, time.Hour)
	// As behind the proxy of the tailnet, which says whom it came for.
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Forwarded-For", "100.64.0.7")
		front.handler.ServeHTTP(rw, r)
	}))
	defer server.Close()
	client := stillClient()
	for _, user := range []string{"owner", "nobody", "anyone"} {
		res := signIn(t, client, server.URL, user, "wrong password")
		res.Body.Close()
	}
	res := signIn(t, client, server.URL, "owner", "correct horse battery")
	defer res.Body.Close()
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a spray of names is answered %d, not held up", res.StatusCode)
	}
}

// A tab that was reloaded leaves its window behind with no stream on it. What
// the thread tells does not keep that window: only the browser does.
func TestReapTakesAWindowTheThreadAloneKeeps(t *testing.T) {
	front := newWebFront(home.Dir(t.TempDir()), fakeProfile("pythia"))
	request := httptest.NewRequest("GET", "/api/initial", nil)
	request.Header.Set(clientHeader, strings.Repeat("b", 43))
	client := front.client(httptest.NewRecorder(), request)
	client.mu.Lock()
	client.last = time.Now().Add(-2 * webIdle)
	client.mu.Unlock()
	client.broadcast(sseEvent{event: "events", data: []byte("[]")})
	front.clients.reap(webIdle)
	client.mu.Lock()
	closed := client.closed
	client.mu.Unlock()
	if !closed {
		t.Fatal("a window nobody looks at is kept because the thread spoke")
	}
}

// Behind a proxy that names no caller, every caller shares the address of the
// host: a spray of names from one of them must not hold the owner up.
func TestLoginOfTheHostWithoutAnAddressCountsOnlyTheName(t *testing.T) {
	request := httptest.NewRequest("POST", "/login", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	caller, _ := callerKey(request, "x")
	if caller != "" {
		t.Fatalf("the host is counted as a caller: %q", caller)
	}
	var none *web.Throttle
	if _, ok := none.Start("anything"); !ok {
		t.Fatal("no count held an attempt up")
	}
	none.Cancel("anything")
	none.Failed("anything")
	none.Cleared("anything")
}
