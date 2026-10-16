package websearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	} {
		if _, err := s.configure(settings{Provider: "searxng", Endpoint: endpoint}); err == nil {
			t.Errorf("accepted endpoint %q", endpoint)
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
