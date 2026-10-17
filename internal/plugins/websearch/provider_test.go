package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestProviderSettings(t *testing.T) {
	s := &search{}
	configured, err := s.configure(settings{})
	if err != nil || configured.provider != "exa" || configured.endpoint != endpoint || configured.private {
		t.Fatalf("default: %+v, %v", configured, err)
	}
	tool := s.tool()
	if tool.Configure == nil || len(tool.Options) != 2 || tool.Run == nil {
		t.Fatal("missing configuration or default runner")
	}
	for _, raw := range []string{
		`{"provider":"other"}`, `{"provider":"searxng"}`,
		`{"provider":true}`, `{"unknown":1}`,
	} {
		if _, err := tool.Configure(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, endpoint := range []string{
		"/search", "ftp://host/search", "https:///search", "https://user:secret@host/search",
		"https://host/search#fragment", "https://host:99999/search", "https://host:abc/search", ":bad",
		"https://host:0/search", "https://host:65536/search",
	} {
		if _, err := s.configure(settings{Provider: "searxng", Endpoint: endpoint}); err == nil {
			t.Errorf("accepted endpoint %q", endpoint)
		}
	}
	for _, endpoint := range []string{"https://host:1/search", "https://host:65535/search", "http://host/search"} {
		if _, err := s.configure(settings{Provider: "searxng", Endpoint: endpoint}); err != nil {
			t.Errorf("refused endpoint %q: %v", endpoint, err)
		}
	}
	cfg, _ := json.Marshal(settings{Provider: "searxng", Endpoint: "http://localhost:8888/search"})
	private, err := tool.Configure(cfg)
	if err != nil || private.Description != tool.Description || !strings.Contains(private.Description, "owner-configured provider") || !strings.Contains(private.Description, "no public fallback") {
		t.Fatalf("description/settings: %+v, %v", private, err)
	}
	if s.provider != "" || s.endpoint != "" || s.client != nil {
		t.Fatal("configuration mutated shared search")
	}
}

// An explicit endpoint must never ask any public engine, even on an error.
func forbidFallback(t *testing.T) {
	t.Helper()
	old := engines
	engines = []engine{{Name: "forbidden", Ask: func(context.Context, *http.Client, string, int) ([]hit, error) {
		t.Error("private search reached public fallback")
		return nil, fmt.Errorf("forbidden")
	}}}
	t.Cleanup(func() { engines = old })
}

func TestSearXNG(t *testing.T) {
	forbidFallback(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/search" || r.URL.Query().Get("q") != "a & b" || r.URL.Query().Get("format") != "json" || r.URL.Query().Get("language") != "en" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("request: %s %s %v", r.Method, r.URL, r.Header)
		}
		fmt.Fprint(w, `{"results":[{"title":"bad","url":"javascript:alert(1)"},{"title":"One","url":"https://example.com/one#part","content":"excerpt"},{"title":"Two","url":"https://example.com/two"}]}`)
	}))
	defer server.Close()
	tool := (&search{client: server.Client()}).tool()
	cfg, _ := json.Marshal(settings{Provider: "searxng", Endpoint: server.URL + "/search?language=en&q=old&format=html"})
	configured, err := tool.Configure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	out, err := configured.Run(context.Background(), json.RawMessage(`{"query":" a & b ","limit":1}`))
	if err != nil || !strings.Contains(out, "One") || !strings.Contains(out, "excerpt") || !strings.Contains(out, "#part") || strings.Contains(out, "Two") || strings.Contains(out, "javascript:") {
		t.Fatalf("%q, %v", out, err)
	}
}

func TestPrivateSearchFailures(t *testing.T) {
	forbidFallback(t)
	for _, tc := range []struct {
		name, provider, body, want string
		status                     int
	}{
		{"unavailable", "searxng", "", "HTTP 503", 503},
		{"json disabled", "searxng", "", "HTTP 403", 403},
		{"malformed", "searxng", "<html>not JSON</html>", "invalid SearXNG", 200},
		{"missing", "searxng", "{}", "no results array", 200},
		{"large", "searxng", strings.Repeat("x", maxResponse+1), "exceeds 1 MiB", 200},
		{"custom exa", "exa", "", "HTTP 503", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			s, err := (&search{client: server.Client()}).configure(settings{Provider: tc.provider, Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.run(context.Background(), args{Query: "private"}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s: %v", tc.want, err)
			}
		})
	}
}

func TestSearXNGEmptyBoundsAndCancellation(t *testing.T) {
	forbidFallback(t)
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprint(large), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !large {
					fmt.Fprint(w, `{"results":[]}`)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"results": []map[string]string{{"title": "long", "url": "https://example.com", "content": strings.Repeat("ä", maxOutput)}}})
			}))
			defer server.Close()
			s, err := (&search{client: server.Client()}).configure(settings{Provider: "searxng", Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			out, err := s.run(context.Background(), args{Query: "private"})
			if err != nil || len(out) > maxOutput || !utf8.ValidString(out) || (large && !strings.Contains(out, "truncated")) || (!large && out != "no search results found") {
				t.Fatalf("output length %d, error %v", len(out), err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := s.run(ctx, args{Query: "private"}); err == nil || !strings.Contains(err.Error(), "context canceled") {
				t.Fatalf("cancellation: %v", err)
			}
		})
	}
}

func TestPrivateSearchRejectsRedirect(t *testing.T) {
	forbidFallback(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("followed private search redirect")
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	s, err := (&search{client: server.Client()}).configure(settings{Provider: "searxng", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(context.Background(), args{Query: "private"}); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("redirect: %v", err)
	}
}

// What breaks before the answer of SearXNG is read is an error, and never a
// public fallback.
func TestSearXNGRequestFailures(t *testing.T) {
	forbidFallback(t)
	// Settings are checked when they are configured: one that is not reaches
	// no request.
	s := &search{client: http.DefaultClient, provider: "searxng", endpoint: "ftp://searx.test/search", private: true}
	if _, err := s.run(context.Background(), args{Query: "private"}); err == nil || !strings.Contains(err.Error(), "endpoint must be an absolute HTTP(S) URL") {
		t.Fatalf("an endpoint that is not checked: %v", err)
	}
	// The zone of an IPv6 address is taken as it is written, but not once it
	// is written again with the query.
	s, err := (&search{client: http.DefaultClient}).configure(settings{Provider: "searxng", Endpoint: "http://[fe80::1%25ä]/search"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(context.Background(), args{Query: "private"}); err == nil || !strings.Contains(err.Error(), `invalid URL escape "%C3"`) {
		t.Fatalf("an endpoint that cannot be written again: %v", err)
	}
	s, err = (&search{client: breaking()}).configure(settings{Provider: "searxng", Endpoint: "http://searx.test/search"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.run(context.Background(), args{Query: "private"}); err == nil || err.Error() != "reading SearXNG search: connection reset" {
		t.Fatalf("an answer that breaks off: %v", err)
	}
}

// Every configuration has a client of its own, which follows no redirect,
// with the timeout of the client it was made from, or else 30 seconds.
func TestProviderClient(t *testing.T) {
	for _, tc := range []struct {
		from *http.Client
		want time.Duration
	}{
		{nil, 30 * time.Second},
		{&http.Client{}, 30 * time.Second},
		{&http.Client{Timeout: 5 * time.Second}, 5 * time.Second},
	} {
		configured, err := (&search{client: tc.from}).configure(settings{})
		if err != nil || configured.client == tc.from || configured.client.Timeout != tc.want || configured.client.CheckRedirect == nil {
			t.Fatalf("from %+v: %+v, %v", tc.from, configured, err)
		}
		if tc.from != nil && (tc.from.CheckRedirect != nil || tc.from.Timeout == 30*time.Second) {
			t.Fatal("configuration changed the client it was made from")
		}
	}
}

// Of the results of SearXNG only those with an HTTP(S) URL of a host and
// without credentials are taken.
func TestSearXNGTakesOnlyWebResults(t *testing.T) {
	forbidFallback(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"results": []hitJSON{
			{"Unparsable", "http://[::1", ""},
			{"FTP", "ftp://example.com/file", ""},
			{"No host", "https:///path", ""},
			{"Credentials", "https://user:pw@example.com/", ""},
			{"Plain", "http://example.com/plain", "plain"},
			{"Secure", "https://example.com/secure", ""},
		}})
	}))
	defer server.Close()
	s, err := (&search{client: server.Client()}).configure(settings{Provider: "searxng", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.run(context.Background(), args{Query: "web"})
	want := hitsText([]hit{{Title: "Plain", URL: "http://example.com/plain", Excerpt: "plain"}, {Title: "Secure", URL: "https://example.com/secure"}})
	if err != nil || out != want {
		t.Fatalf("got %q, %v; want %q", out, err, want)
	}
}

// An answer of exactly 1 MiB is read, from Exa as from SearXNG.
func TestSearchReadsAnAnswerOf1MiB(t *testing.T) {
	forbidFallback(t)
	for _, provider := range []string{"exa", "searxng"} {
		t.Run(provider, func(t *testing.T) {
			head, tail := `{"result":{"content":[{"type":"text","text":"`, `"}]}}`
			if provider == "searxng" {
				head, tail = `{"results":[{"title":"Large","url":"https://example.com/large","content":"`, `"}]}`
			}
			body := head + strings.Repeat("x", maxResponse-len(head)-len(tail)) + tail
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(body))
			}))
			defer server.Close()
			s, err := (&search{client: server.Client()}).configure(settings{Provider: provider, Endpoint: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			out, err := s.run(context.Background(), args{Query: "large"})
			if err != nil || len(out) != maxOutput || !strings.HasSuffix(out, "for more]") {
				t.Fatalf("%d bytes, %v", len(out), err)
			}
		})
	}
}
