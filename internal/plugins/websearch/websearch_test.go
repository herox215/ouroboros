package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

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
	text := bounded(strings.Repeat("ä", maxOutput))
	if len(text) > maxOutput || !utf8.ValidString(text) || !strings.Contains(text, "truncated") {
		t.Fatal("invalid truncation")
	}
	if Show(json.RawMessage(`{"query":"ouro"}`)) != "searching the web for ouro" || Show(json.RawMessage("bad")) != "" {
		t.Fatal("invalid tool view")
	}
}
