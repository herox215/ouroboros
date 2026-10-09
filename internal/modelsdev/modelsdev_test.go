package modelsdev

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// listing is a catalog as models.dev answers it.
const listing = `{
	"opencode-go": {"api": "https://opencode.ai/zen/go/v1/", "models": {
		"deepseek-v4.1-flash": {"reasoning": true, "reasoning_options": [{"type": "effort", "values": ["low", "high", "max"]}]},
		"qwen3.7-max": {"reasoning": true, "reasoning_options": [{"type": "toggle"}, {"type": "budget_tokens", "max": 262144}]},
		"unsaid": {"reasoning": true},
		"plain": {"reasoning": false}
	}},
	"openai": {"models": {
		"gpt-5.5": {"reasoning": true, "reasoning_options": [{"type": "toggle"}, {"type": "effort", "values": ["none", "low", "xhigh"]}]}
	}}
}`

// logs is what a catalog logged.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) log(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logs) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// served is a catalog in a file of the test, fetched from a server that
// answers with answer, and counts how often it was asked. Its clock is at
// *now.
func served(t *testing.T, answer func(w http.ResponseWriter)) (*Catalog, *atomic.Int32, *time.Time, *logs) {
	t.Helper()
	asked := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		answer(w)
	}))
	t.Cleanup(srv.Close)
	l := &logs{}
	c := Open(filepath.Join(t.TempDir(), "catalog.json"), l.log)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	c.url, c.now = srv.URL, func() time.Time { return now }
	return c, asked, &now, l
}

// What models.dev lists of a model is known once the catalog was fetched,
// by the ID of its provider and by where its API is reached: the efforts it
// takes, none for one that takes another kind of thought or none at all. A
// model whose options it does not list is not known.
func TestEfforts(t *testing.T) {
	c, asked, _, l := served(t, func(w http.ResponseWriter) { w.Write([]byte(listing)) })
	if efforts, known := c.Efforts("opencode-go", "deepseek-v4.1-flash"); known || efforts != nil {
		t.Fatalf("before the fetch: %q, %v", efforts, known)
	}
	c.Wait()
	for _, tt := range []struct {
		provider, model string
		efforts         []string
		known           bool
	}{
		{"opencode-go", "deepseek-v4.1-flash", []string{"low", "high", "max"}, true},
		{"https://opencode.ai/zen/go/v1", "deepseek-v4.1-flash", []string{"low", "high", "max"}, true},
		{"https://opencode.ai/zen/go/v1/", "deepseek-v4.1-flash", []string{"low", "high", "max"}, true},
		{"opencode-go", "qwen3.7-max", nil, true},
		{"opencode-go", "plain", nil, true},
		{"opencode-go", "unsaid", nil, false},
		{"opencode-go", "none", nil, false},
		{"openai", "gpt-5.5", []string{"none", "low", "xhigh"}, true},
		{"https://elsewhere/v1", "gpt-5.5", nil, false},
	} {
		efforts, known := c.Efforts(tt.provider, tt.model)
		if !slices.Equal(efforts, tt.efforts) || known != tt.known {
			t.Errorf("%s %s: %q, %v", tt.provider, tt.model, efforts, known)
		}
	}
	if asked.Load() != 1 || l.all() != "" {
		t.Fatalf("asked %d times, log %q", asked.Load(), l.all())
	}

	// What is kept is read by the next catalog of the file, which fetches
	// nothing while it is new.
	next := Open(c.file, l.log)
	next.url, next.now = c.url, c.now
	if efforts, known := next.Efforts("openai", "gpt-5.5"); !known || len(efforts) != 3 {
		t.Fatalf("read: %q, %v", efforts, known)
	}
	next.Wait()
	if asked.Load() != 1 {
		t.Fatalf("fetched again: %d", asked.Load())
	}
	if info, _ := os.Stat(c.file); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode())
	}
}

// A catalog older than a day is fetched anew in the background, while the
// one kept answers; one that cannot be fetched is logged, and tried again an
// hour later.
func TestRefresh(t *testing.T) {
	var answer atomic.Value
	answer.Store(listing)
	c, asked, now, l := served(t, func(w http.ResponseWriter) {
		if a := answer.Load().(string); a == "down" {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.Write([]byte(a))
		}
	})
	c.Efforts("openai", "gpt-5.5")
	c.Wait()
	answer.Store(`{"openai": {"models": {"gpt-5.5": {"reasoning": true, "reasoning_options": [{"type": "effort", "values": ["high"]}]}}}}`)
	*now = now.Add(maxAge - time.Minute)
	c.Efforts("openai", "gpt-5.5")
	c.Wait()
	if asked.Load() != 1 {
		t.Fatalf("fetched before a day went by: %d", asked.Load())
	}
	*now = now.Add(time.Minute)
	if efforts, _ := c.Efforts("openai", "gpt-5.5"); len(efforts) != 3 {
		t.Fatalf("the kept one answers while the fetch is on its way: %q", efforts)
	}
	c.Wait()
	if efforts, _ := c.Efforts("openai", "gpt-5.5"); !slices.Equal(efforts, []string{"high"}) || asked.Load() != 2 {
		t.Fatalf("fetched anew: %q, asked %d", efforts, asked.Load())
	}

	answer.Store("down")
	*now = now.Add(maxAge)
	c.Efforts("openai", "gpt-5.5")
	c.Wait()
	if efforts, known := c.Efforts("openai", "gpt-5.5"); !known || len(efforts) != 1 || !strings.Contains(l.all(), "the catalog of models.dev could not be fetched: HTTP 503") {
		t.Fatalf("after a failure: %q, %v, log %q", efforts, known, l.all())
	}
	c.Wait()
	*now = now.Add(waitTime - time.Minute)
	c.Efforts("openai", "gpt-5.5")
	c.Wait()
	if asked.Load() != 3 {
		t.Fatalf("tried again too soon: %d", asked.Load())
	}
	answer.Store(listing)
	*now = now.Add(time.Minute)
	c.Efforts("openai", "gpt-5.5")
	c.Wait()
	if efforts, _ := c.Efforts("openai", "gpt-5.5"); len(efforts) != 3 || asked.Load() != 4 {
		t.Fatalf("tried again: %q, asked %d", efforts, asked.Load())
	}
}

// What is no catalog, too much of one, or cannot be fetched or kept, is
// logged, and nothing is known.
func TestFetchFails(t *testing.T) {
	defer func(was int) { maxSize = was }(maxSize)
	maxSize = len(listing)
	for name, answer := range map[string]func(w http.ResponseWriter){
		"it is no catalog": func(w http.ResponseWriter) { w.Write([]byte("<html>")) },
		"larger than 0 MiB": func(w http.ResponseWriter) {
			w.Write([]byte(listing + " "))
		},
		"unexpected EOF": func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", "100")
			w.Write([]byte("{"))
		},
	} {
		c, _, _, l := served(t, answer)
		c.Efforts("openai", "gpt-5.5")
		c.Wait()
		if _, known := c.Efforts("openai", "gpt-5.5"); known || !strings.Contains(l.all(), name) {
			t.Errorf("%s: known %v, log %q", name, known, l.all())
		}
	}

	// A catalog of just the size it may have is taken.
	c, _, _, l := served(t, func(w http.ResponseWriter) { w.Write([]byte(listing)) })
	c.Efforts("openai", "gpt-5.5")
	c.Wait()
	if _, known := c.Efforts("openai", "gpt-5.5"); !known || l.all() != "" {
		t.Errorf("of the size it may have: %v, log %q", known, l.all())
	}

	// Without a server, or a place to keep it.
	c, _, _, l = served(t, func(w http.ResponseWriter) { w.Write([]byte(listing)) })
	c.url = "http://127.0.0.1:0"
	c.Efforts("openai", "gpt-5.5")
	c.Wait()
	if !strings.Contains(l.all(), "could not be fetched") {
		t.Errorf("no server: %q", l.all())
	}
	c, _, _, l = served(t, func(w http.ResponseWriter) { w.Write([]byte(listing)) })
	c.file = filepath.Join(c.file, "inside a file")
	os.WriteFile(filepath.Dir(c.file), nil, 0o600)
	c.Efforts("openai", "gpt-5.5")
	c.Wait()
	if !strings.Contains(l.all(), "could not be fetched") {
		t.Errorf("not kept: %q", l.all())
	}
	c, _, _, _ = served(t, func(w http.ResponseWriter) {})
	c.url = "http://bad host/"
	c.log = nil // nobody to tell
	c.Efforts("openai", "gpt-5.5")
	c.Wait()

	// A file that is no catalog is not read: the catalog is fetched,
	// however new the file says it is.
	c, asked, _, _ := served(t, func(w http.ResponseWriter) { w.Write([]byte(listing)) })
	os.WriteFile(c.file, []byte(`{"at": "2026-10-09T12:00:00Z"}`), 0o600)
	if _, known := c.Efforts("openai", "gpt-5.5"); known {
		t.Error("a file without providers was read")
	}
	c.Wait()
	if asked.Load() != 1 {
		t.Errorf("not fetched: %d", asked.Load())
	}
}

// The environment says where the catalog is fetched from, or that it is not.
func TestEnv(t *testing.T) {
	t.Setenv(Env, "")
	if c := Open("catalog.json", nil); c.url != URL {
		t.Fatalf("url %q", c.url)
	}
	t.Setenv(Env, "http://mirror/api.json")
	if c := Open("catalog.json", nil); c.url != "http://mirror/api.json" {
		t.Fatalf("url %q", c.url)
	}
	t.Setenv(Env, "off")
	c := Open(filepath.Join(t.TempDir(), "catalog.json"), nil)
	if _, known := c.Efforts("openai", "gpt-5.5"); known {
		t.Fatal("known without a catalog")
	}
	c.Wait()
	if _, err := os.Stat(c.file); !os.IsNotExist(err) {
		t.Fatalf("fetched: %v", err)
	}
}
