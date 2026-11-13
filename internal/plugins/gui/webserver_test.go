package gui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client, server
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
