package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"

	"ouroboros/internal/kernel"
	"ouroboros/plugin"
)

// withoutEngines makes the fallback find nothing at once: a test about the
// search of Exa asks no search engine, and none of these tests reaches the
// network.
func withoutEngines(t *testing.T) {
	t.Helper()
	old := engines
	engines = []engine{answering("test", nil, errors.New("no engine is asked in a test"))}
	t.Cleanup(func() { engines = old })
}

func TestStaticDocumentationGuidance(t *testing.T) {
	description := (&search{}).tool().Description
	for _, want := range []string{
		"Verify important claims against source content",
		"use an available HTTP mechanism first for static documentation",
		"browser only when JavaScript or interaction is needed",
	} {
		if !strings.Contains(description, want) {
			t.Errorf("description lacks %q", want)
		}
	}
	if strings.Contains(description, "Use browser to open sources") {
		t.Fatal("description still directs static sources to the browser")
	}
}

func TestSearch(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "application/json, text/event-stream" {
					t.Errorf("request: %s %v", r.Method, r.Header)
				}
				var request struct {
					Method string
					Params struct {
						Name      string
						Arguments struct {
							Query                string
							NumResults           int
							ContextMaxCharacters int
						}
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if request.Method != "tools/call" || request.Params.Name != "web_search_exa" || request.Params.Arguments.Query != "ouro" || request.Params.Arguments.NumResults != 5 || request.Params.Arguments.ContextMaxCharacters != 10000 {
					t.Errorf("request: %+v", request)
				}
				payload := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"Title: Ouro\nURL: https://example.com"},{"type":"text","text":"more"}]}}`
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					payload = ": keepalive\r\n\r\nevent: message\r\ndata: " + payload + "\r\n\r\n"
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				w.Write([]byte(payload))
			}))
			defer server.Close()
			s := &search{client: server.Client(), endpoint: server.URL}
			out, err := s.tool().Run(context.Background(), json.RawMessage(`{"query":" ouro "}`))
			if err != nil || !strings.Contains(out, "https://example.com") || !strings.HasSuffix(out, "more") {
				t.Fatalf("%q, %v", out, err)
			}
		})
	}
}

func TestSearchFailures(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		status                  int
		body, contentType, want string
	}{
		{"quota", 429, "", "", "Retry-After: 60"},
		{"unavailable", 503, "", "", "HTTP 503"},
		{"invalid", 200, "not json", "application/json", "invalid Exa"},
		{"rpc error", 200, `{"error":{"code":-32000,"message":"quota reached"}}`, "application/json", "quota reached"},
		{"tool error", 200, `{"result":{"isError":true,"content":[{"type":"text","text":"failed"}]}}`, "application/json", "Exa failed: failed"},
		{"missing", 200, `{}`, "application/json", "returned no result"},
		{"empty stream", 200, ": ping\n\n", "text/event-stream", "returned no response"},
		{"large", 200, strings.Repeat("x", maxResponse+1), "application/json", "exceeds 1 MiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withoutEngines(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			s := &search{client: server.Client(), endpoint: server.URL}
			_, err := s.run(context.Background(), args{Query: "ouro"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
}

func TestSearchArgumentsAndCancellation(t *testing.T) {
	withoutEngines(t)
	s := &search{client: http.DefaultClient, endpoint: "http://127.0.0.1:1"}
	for _, raw := range []string{
		`{"query":" "}`, `{"query":"ouro","limit":-1}`, `{"query":"ouro","limit":11}`,
		`{"query":"ouro","unknown":true}`, `{"query":true}`,
	} {
		if _, err := s.tool().Run(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if _, err := s.run(context.Background(), args{Query: strings.Repeat("x", 4097)}); err == nil {
		t.Error("accepted long query")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.run(ctx, args{Query: "ouro"}); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("%v", err)
	}
}

func TestResultAndView(t *testing.T) {
	out, err := result([]byte(`{"result":{"content":[]}}`))
	if err != nil || out != "no search results found" {
		t.Fatalf("%q %v", out, err)
	}
	// Only content that is text, and not empty, is taken.
	out, err = result([]byte(`{"result":{"content":[{"type":"image","text":"no text"},{"type":"text","text":"one"},{"type":"text","text":""},{"type":"text","text":"two"}]}}`))
	if err != nil || out != "one\n\ntwo" {
		t.Fatalf("%q %v", out, err)
	}
	text := bounded(strings.Repeat("ä", maxOutput))
	if len(text) > maxOutput || !utf8.ValidString(text) || !strings.Contains(text, "truncated") {
		t.Fatal("invalid truncation")
	}
	if Show(json.RawMessage(`{"query":"ouro"}`)) != "searching the web for ouro" || Show(json.RawMessage("bad")) != "" {
		t.Fatal("invalid tool view")
	}
}

// roundTrip is a transport of these tests: it answers every request itself,
// and none reaches the network.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// breaking is a client whose answers are 200 and break off while their body
// is read, as a connection that is reset does.
func breaking() *http.Client {
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := io.NopCloser(iotest.ErrReader(errors.New("connection reset")))
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: body, Request: r}, nil
	})}
}

// The plugin contributes the tool with its view, and the tool searches where
// its settings say.
func TestPlugin(t *testing.T) {
	var ctx *plugin.Context
	k := kernel.New()
	if err := k.Start(Plugin(), plugin.New("spy", func(c *plugin.Context, _ struct{}) error { ctx = c; return nil })); err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	tools, views := plugin.All[plugin.Tool](ctx), plugin.All[plugin.ToolView](ctx)
	if len(tools) != 1 || tools[0].Name != Tool || tools[0].Configure == nil || len(views) != 1 || views[0].Tool != Tool || views[0].Detail == nil {
		t.Fatalf("tools %+v, views %+v", tools, views)
	}
	if got := views[0].Show(json.RawMessage(`{"query":"ouro"}`)); got != "searching the web for ouro" {
		t.Fatalf("view: %q", got)
	}
	forbidFallback(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"result":{"content":[{"type":"text","text":"Title: Ouro"}]}}`))
	}))
	defer server.Close()
	cfg, _ := json.Marshal(settings{Endpoint: server.URL})
	configured, err := tools[0].Configure(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := configured.Run(context.Background(), json.RawMessage(`{"query":"ouro"}`)); err != nil || out != "Title: Ouro" {
		t.Fatalf("%q, %v", out, err)
	}
}

// What breaks before the answer of Exa is read is an error, and an explicit
// endpoint does not fall back on it.
func TestExaRequestFailures(t *testing.T) {
	forbidFallback(t)
	s := &search{client: http.DefaultClient, endpoint: "http://[::1", private: true}
	if _, err := s.run(context.Background(), args{Query: "ouro"}); err == nil || !strings.Contains(err.Error(), "missing ']' in host") {
		t.Fatalf("an endpoint that is no URL: %v", err)
	}
	s = &search{client: breaking(), endpoint: "http://exa.test/mcp", private: true}
	if _, err := s.run(context.Background(), args{Query: "ouro"}); err == nil || err.Error() != "reading Exa search: connection reset" {
		t.Fatalf("an answer that breaks off: %v", err)
	}
}

// A query of up to 4096 bytes and a limit of 1 to 10 are taken as they are;
// others are refused with what is wrong, and never sent.
func TestSearchArgumentBounds(t *testing.T) {
	forbidFallback(t)
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Params struct {
				Arguments struct {
					Query      string
					NumResults int
				}
			}
		}
		json.NewDecoder(r.Body).Decode(&request)
		asked = append(asked, fmt.Sprintf("%d %d", len(request.Params.Arguments.Query), request.Params.Arguments.NumResults))
		w.Write([]byte(`{"result":{"content":[{"type":"text","text":"found"}]}}`))
	}))
	defer server.Close()
	s := &search{client: server.Client(), endpoint: server.URL, private: true}
	for _, tc := range []struct {
		a    args
		want string
	}{
		{args{Query: strings.Repeat("x", 4096), Limit: 1}, ""},
		{args{Query: "ouro", Limit: 10}, ""},
		{args{Query: "ouro"}, ""},
		{args{Query: strings.Repeat("x", 4097)}, "the search query is longer than 4096 bytes"},
		{args{Query: "ouro", Limit: -1}, "limit must be between 1 and 10"},
		{args{Query: "ouro", Limit: 11}, "limit must be between 1 and 10"},
		{args{Query: " \t"}, "web_search needs a query"},
	} {
		out, err := s.run(context.Background(), tc.a)
		if tc.want == "" && (err != nil || out != "found") || tc.want != "" && (err == nil || err.Error() != tc.want) {
			t.Errorf("%.20q, limit %d: %q, %v", tc.a.Query, tc.a.Limit, out, err)
		}
	}
	if strings.Join(asked, ",") != "4096 1,4 10,4 5" {
		t.Fatalf("asked %v", asked)
	}
}
