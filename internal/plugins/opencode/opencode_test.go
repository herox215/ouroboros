package opencode_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"ouroboros/internal/cli"
	"ouroboros/internal/llm/llmtest"
	client "ouroboros/internal/openai"
	"ouroboros/internal/plugins/opencode"
	"ouroboros/plugin"
)

const key = "sk-go-123"

// access is an access with the credentials c, or one that cannot be read.
type access struct {
	c   plugin.Credentials
	err error
}

func (a access) Credentials() (plugin.Credentials, error)    { return a.c, a.err }
func (a access) Renew(func(*plugin.Credentials) error) error { return nil }

// zen is a fake OpenCode Go: it lists listed for the key and answers chat
// completions, and keeps the headers of the last request.
type zen struct {
	URL    string
	mu     sync.Mutex
	listed []string
	status int // of the list of models, if not 0
	header http.Header
}

func newZen(t *testing.T, listed ...string) *zen {
	t.Helper()
	z := &zen{listed: listed}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /zen/go/v1/models", func(w http.ResponseWriter, r *http.Request) {
		z.mu.Lock()
		defer z.mu.Unlock()
		z.header = r.Header.Clone()
		switch {
		case z.status != 0:
			w.WriteHeader(z.status)
			w.Write([]byte(`{"error":{"message":"down"}}`))
		case r.Header.Get("Authorization") != "Bearer "+key:
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"bad key"}}`))
		default:
			data := []map[string]string{}
			for _, id := range z.listed {
				data = append(data, map[string]string{"id": id})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
		}
	})
	mux.HandleFunc("POST /zen/go/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		z.mu.Lock()
		defer z.mu.Unlock()
		z.header = r.Header.Clone()
		var in struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "from " + in.Model}}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	z.URL = srv.URL + "/zen/go/v1"
	return z
}

func (z *zen) set(change func()) {
	z.mu.Lock()
	defer z.mu.Unlock()
	change()
}

// seen is the header name of the last request.
func (z *zen) seen(name string) string {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.header.Get(name)
}

// The plugin contributes the provider for OpenCode Go, under its own name
// and with a title to pick it by, and it is where OpenCode Go is.
func TestContributesTheProvider(t *testing.T) {
	var ctx *plugin.Context
	spy := plugin.New("spy", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	})
	if _, err := cli.NewWith(opencode.Plugin(opencode.Options{}), spy); err != nil {
		t.Fatal(err)
	}
	providers := plugin.All[plugin.Provider](ctx)
	if len(providers) != 1 || providers[0].Name != "opencode-go" || providers[0].Name != opencode.Plugin(opencode.Options{}).Name || providers[0].Title != "OpenCode Go" {
		t.Fatalf("providers %+v", providers)
	}
	if opencode.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("OpenCode Go is at %s", opencode.BaseURL)
	}
}

// Signing in asks for the API key alone: where OpenCode Go is reached is
// known. The key is a secret, and sent as a bearer token.
func TestLogin(t *testing.T) {
	z := newZen(t, "glm-5.3", "claude-haiku-5-5", "gpt-6-luna", "grok-4.7", "kimi-k3", "minimax-m3", "muse-spark-1.3-contributor", "qwen3.8-max")
	p := opencode.Provider(opencode.Options{BaseURL: z.URL + "/"})
	d := &llmtest.Dialog{Answers: map[string][]string{"OpenCode Go API key": {" ", "sk-bad", " " + key + " "}}}
	c, err := p.Login(context.Background(), d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "OpenCode Go" || c.About != z.URL || !reflect.DeepEqual(c.Secrets(), []string{key}) || strings.Contains(string(c.Data), key) {
		t.Fatalf("kept %+v", c)
	}
	if !reflect.DeepEqual(d.Rejected(), []string{"the API key is needed", "API key rejected, try again"}) {
		t.Fatalf("rejected %v", d.Rejected())
	}
	asked := d.Asked()
	if len(asked) != 3 || asked[0].Title != "OpenCode Go API key" || !asked[0].Secret || asked[0].Known || asked[0].Default != "" || asked[0].Hint != "the key of the OpenCode console" {
		t.Fatalf("asked %+v", asked)
	}
	if z.seen("Authorization") != "Bearer "+key {
		t.Fatalf("the key was sent as %q", z.seen("Authorization"))
	}

	// Of its models, those are offered that answer chat completions: the
	// others speak the form of another API.
	models, err := p.Models(context.Background(), access{c: c})
	want := []string{"glm-5.3", "kimi-k3"}
	if err != nil || !reflect.DeepEqual(models, plugin.Models{Chat: want, All: want, Default: "glm-5.3"}) {
		t.Fatalf("models %+v, %v", models, err)
	}
	// The agents answer through chat completions, with the session of the
	// conversation, as OpenCode asks for.
	ctx := client.WithSession(context.Background(), "conversation-1")
	reply, err := p.Model(access{c: c}).Complete(ctx, "kimi-k3", []plugin.ChatMessage{{Role: "user", Content: "hi"}}, nil)
	if err != nil || reply.Content != "from kimi-k3" {
		t.Fatalf("got %+v, %v", reply, err)
	}
	if z.seen("X-Opencode-Session") != "conversation-1" || z.seen("User-Agent") != client.UserAgent || z.seen("Authorization") != "Bearer "+key {
		t.Fatalf("sent session %q as %q", z.seen("X-Opencode-Session"), z.seen("User-Agent"))
	}

	// Signing in again offers the key that is saved, until it is refused.
	again := &llmtest.Dialog{Answers: map[string][]string{"OpenCode Go API key": {"sk-bad", key}}}
	if _, err := p.Login(context.Background(), again, &c); err != nil {
		t.Fatal(err)
	}
	if asked := again.Asked(); len(asked) != 2 || asked[0].Default != key || !asked[0].Known || asked[0].Hint != "Enter keeps the saved key" || asked[1].Known || asked[1].Default != "" {
		t.Fatalf("asked %+v", asked)
	}
}

// What goes wrong is told: an owner who gives up, an OpenCode Go that does
// not list its models or none that this ouro answers with, and an access
// that cannot be read.
func TestWhatGoesWrong(t *testing.T) {
	z := newZen(t, "claude-haiku-5-5", "gpt-6-luna")
	p := opencode.Provider(opencode.Options{BaseURL: z.URL})
	ctx := context.Background()
	gone, leave := context.WithCancel(ctx)
	leave()
	if _, err := p.Login(gone, &llmtest.Dialog{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("an owner who gives up: %v", err)
	}
	answer := func() *llmtest.Dialog {
		return &llmtest.Dialog{Answers: map[string][]string{"OpenCode Go API key": {key}}}
	}
	if _, err := p.Login(ctx, answer(), nil); err == nil || !strings.Contains(err.Error(), "lists no model this ouro can answer with") {
		t.Fatalf("no model for chat completions: %v", err)
	}
	z.set(func() { z.status = http.StatusBadGateway })
	_, err := p.Login(ctx, answer(), nil)
	var api *client.APIError
	if err == nil || !strings.HasPrefix(err.Error(), "cannot list the models of OpenCode Go: ") || !errors.As(err, &api) || errors.Is(err, plugin.ErrKeyRejected) {
		t.Fatalf("OpenCode Go is down: %v", err)
	}
	z.set(func() { z.status = http.StatusForbidden })
	c := client.Credentials(z.URL, key)
	if _, err := p.Models(ctx, access{c: c}); !errors.Is(err, plugin.ErrKeyRejected) {
		t.Fatalf("a key that was revoked: %v", err)
	}

	unreadable := access{err: errors.New("the credentials cannot be read")}
	if _, err := p.Models(ctx, unreadable); err != unreadable.err {
		t.Fatalf("models of an access that cannot be read: %v", err)
	}
	if _, err := p.Model(unreadable).Complete(ctx, "kimi-k3", nil, nil); err != unreadable.err {
		t.Fatalf("an answer through an access that cannot be read: %v", err)
	}
	if _, err := p.Models(ctx, access{}); err == nil {
		t.Fatal("an access without an endpoint: want error")
	}
	// An access that cannot be read offers no key to sign in again with.
	d := answer()
	z.set(func() { z.status, z.listed = 0, []string{"kimi-k3"} })
	if _, err := p.Login(ctx, d, &plugin.Credentials{}); err != nil || d.Asked()[0].Known {
		t.Fatalf("signing in to an access that cannot be read: %v, asked %+v", err, d.Asked())
	}
}
