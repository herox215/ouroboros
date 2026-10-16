package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBoundedTo(t *testing.T) {
	const note = "\n[search output truncated; open the source URLs with browser for more]"
	for _, budget := range []int{minOutput, 257, maxOutput} {
		for _, text := range []string{"short", strings.Repeat("x", budget), strings.Repeat("x", budget+1), strings.Repeat("界🙂ä", budget)} {
			out := boundedTo(text, budget)
			if len(out) > budget || !utf8.ValidString(out) {
				t.Fatalf("budget %d: invalid output of %d bytes", budget, len(out))
			}
			if len(text) <= budget {
				if out != text {
					t.Fatal("changed output that fits")
				}
				continue
			}
			if !strings.HasSuffix(out, note) {
				t.Fatal("missing complete truncation note")
			}
			prefix := strings.TrimSuffix(out, note)
			if !strings.HasPrefix(text, prefix) || budget-len(out) >= utf8.UTFMax {
				t.Fatal("did not preserve the longest UTF-8 prefix that fits")
			}
		}
	}
}

func TestSearchOutputBudget(t *testing.T) {
	for _, mode := range []string{"exa", "sse", "custom", "searxng", "DuckDuckGo", "Brave"} {
		t.Run(mode, func(t *testing.T) {
			text := strings.Repeat("界🙂ä", maxOutput)
			old := engines
			engines = []engine{answering(mode, []hit{{Title: "fixture", URL: "https://example.com", Excerpt: text}}, nil)}
			if mode == "Brave" {
				engines = append([]engine{answering("DuckDuckGo", nil, errors.New("unavailable"))}, engines...)
			}
			t.Cleanup(func() { engines = old })
			providerBudgets := make(chan int, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "DuckDuckGo" || mode == "Brave" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if mode == "searxng" {
					json.NewEncoder(w).Encode(map[string]any{"results": []hitJSON{{"fixture", "https://example.com", text}}})
					return
				}
				var request struct {
					Params struct {
						Arguments struct {
							ContextLimit int `json:"contextMaxCharacters"`
						} `json:"arguments"`
					} `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				providerBudgets <- request.Params.Arguments.ContextLimit
				payload, _ := json.Marshal(map[string]any{"result": map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}})
				if mode == "sse" {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: %s\n\n", payload)
				} else {
					w.Write(payload)
				}
			}))
			defer server.Close()
			s := &search{client: server.Client(), endpoint: server.URL}
			tool := s.tool()
			if mode == "custom" || mode == "searxng" {
				provider := "exa"
				if mode == "searxng" {
					provider = mode
				}
				cfg, _ := json.Marshal(settings{Provider: provider, Endpoint: server.URL})
				var err error
				tool, err = tool.Configure(cfg)
				if err != nil {
					t.Fatal(err)
				}
			}
			full, err := tool.Run(context.Background(), json.RawMessage(`{"query":"fixture"}`))
			if err != nil || len(full) > maxOutput || !strings.Contains(full, "truncated") {
				t.Fatalf("default budget: %d bytes, %v", len(full), err)
			}
			checkProvider := func(want int) {
				t.Helper()
				if mode == "exa" || mode == "sse" || mode == "custom" {
					if got := <-providerBudgets; got != want {
						t.Errorf("provider context budget = %d, want %d", got, want)
					}
				}
			}
			checkProvider(10000)
			for _, budget := range []int{minOutput, 257, maxOutput} {
				raw := json.RawMessage(fmt.Sprintf(`{"query":"fixture","max_bytes":%d}`, budget))
				out, err := tool.Run(context.Background(), raw)
				checkProvider(min(10000, budget))
				if err != nil || out != boundedTo(full, budget) || len(out) > budget || !utf8.ValidString(out) {
					t.Fatalf("budget %d: %d bytes, %v", budget, len(out), err)
				}
			}
		})
	}
}

// hitJSON is the SearXNG wire format, not the internal fallback hit.
type hitJSON struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

func TestSearchRejectsInvalidBudgetBeforeRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid budget reached provider")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	s := &search{client: server.Client(), endpoint: server.URL, private: true}
	for _, value := range []string{"-1", "0", "127", "24577", "1.5", `"127"`} {
		raw := json.RawMessage(`{"query":"fixture","max_bytes":` + value + `}`)
		if _, err := s.tool().Run(context.Background(), raw); err == nil {
			t.Errorf("accepted max_bytes %s", value)
		}
	}
}
