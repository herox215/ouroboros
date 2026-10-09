package openai_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"ouroboros/internal/cli"
	"ouroboros/internal/llm/llmtest"
	client "ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
	"ouroboros/internal/plugins/openai"
	"ouroboros/plugin"
)

// answers is the dialog of an owner who answers the endpoint and the key.
func answers(endpoint []string, key ...string) *llmtest.Dialog {
	return &llmtest.Dialog{Answers: map[string][]string{"LLM endpoint": endpoint, "API key": key}}
}

// The plugin contributes the provider for OpenAI-compatible APIs, under its
// own name.
func TestContributesTheProvider(t *testing.T) {
	var ctx *plugin.Context
	spy := plugin.New("spy", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	})
	if _, err := cli.NewWith(openai.Plugin(openai.Offer{}), spy); err != nil {
		t.Fatal(err)
	}
	providers := plugin.All[plugin.Provider](ctx)
	if len(providers) != 1 || providers[0].Name != "openai" || providers[0].Name != openai.Plugin(openai.Offer{}).Name || providers[0].Title == "" {
		t.Fatalf("providers %+v", providers)
	}
}

// Signing in asks for the endpoint and the key, as the setup always did, and
// keeps the two apart: the endpoint is shown, the key is a secret.
func TestLogin(t *testing.T) {
	url := openaitest.NewServer(t)
	p := openai.Provider(openai.Offer{BaseURL: url + "/"})
	d := answers([]string{" ", ""}, "sk-bad", openaitest.Key)
	c, err := p.Login(context.Background(), d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.About != url || c.Title == "" || !reflect.DeepEqual(c.Secrets(), []string{openaitest.Key}) || strings.Contains(string(c.Data), openaitest.Key) {
		t.Fatalf("kept %+v", c)
	}
	if !reflect.DeepEqual(d.Rejected(), []string{"the endpoint is needed", "API key rejected, try again"}) {
		t.Fatalf("rejected %v", d.Rejected())
	}
	asked := d.Asked()
	if len(asked) != 4 || asked[0].Title != "LLM endpoint" || asked[0].Default != url || asked[0].Secret ||
		asked[2].Title != "API key" || !asked[2].Secret || asked[2].Known || asked[2].Hint != "Enter for none" || asked[2].Text[0] != "for "+url {
		t.Fatalf("asked %+v", asked)
	}

	// The models of the access, and the model the agents answer with.
	models, err := p.Models(context.Background(), plugin.Access(access{c: c}))
	want := plugin.Models{Chat: []string{"gpt-4o", "gpt-4o-mini", "o3-mini"}, All: openaitest.Models, Default: "gpt-4o-mini"}
	if err != nil || !reflect.DeepEqual(models, want) {
		t.Fatalf("models %+v, %v", models, err)
	}
	reply, err := p.Model(access{c: c}).Complete(context.Background(), "gpt-4o", []plugin.ChatMessage{{Role: "user", Content: "hi"}}, nil)
	if err != nil || reply.Content != "[gpt-4o] turns=0 last=hi" {
		t.Fatalf("got %+v, %v", reply, err)
	}
}

// A key that is known is offered where it belongs and nowhere else: the key
// of the access at its endpoint, the one offered at the endpoint offered.
func TestLoginOffersAKeyOnlyForItsEndpoint(t *testing.T) {
	offered := openaitest.NewServer(t)
	saved := openaitest.NewServerWithKey(t, "sk-saved")
	other := openaitest.NewServerWithKey(t, "")
	p := openai.Provider(openai.Offer{BaseURL: offered, APIKey: openaitest.Key})
	have := client.Credentials(saved, "sk-saved")

	for _, tt := range []struct {
		name     string
		endpoint string
		have     *plugin.Credentials
		key      string // what is offered as known, "" if nothing
		at       string
	}{
		{"the endpoint offered", "", nil, openaitest.Key, offered},
		{"the endpoint of the access", "", &have, "sk-saved", saved},
		{"the endpoint offered, for an access of another", offered, &have, openaitest.Key, offered},
		{"another endpoint", other, &have, "", other},
		{"another endpoint, for a new access", other, nil, "", other},
	} {
		d := answers([]string{tt.endpoint}, "")
		c, err := p.Login(context.Background(), d, tt.have)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if len(d.Rejected()) != 0 {
			t.Errorf("%s: an answer that was taken was rejected: %v", tt.name, d.Rejected())
		}
		q := d.Asked()[1]
		if q.Default != tt.key || q.Known != (tt.key != "") || c.About != tt.at {
			t.Errorf("%s: offered %q (known %v), signed in at %s", tt.name, q.Default, q.Known, c.About)
		}
		if tt.key != "" && q.Hint != "Enter keeps the saved key" {
			t.Errorf("%s: the hint is %q", tt.name, q.Hint)
		}
		if url, key, _ := client.Saved(c); url != tt.at || key != tt.key {
			t.Errorf("%s: kept %q with %q", tt.name, url, key)
		}
	}

	// A key that is known and refused is asked for anew, and not offered again.
	gone := client.Credentials(saved, "sk-old")
	d := answers([]string{""}, "", "sk-saved")
	if _, err := p.Login(context.Background(), d, &gone); err != nil {
		t.Fatal(err)
	}
	if asked := d.Asked(); len(asked) != 3 || !asked[1].Known || asked[2].Known || asked[2].Default != "" {
		t.Fatalf("asked %+v", asked)
	}
	// An access that cannot be read offers nothing, and is signed in anew.
	broken := plugin.Credentials{Data: []byte(`{`)}
	d = answers([]string{""}, "")
	if c, err := p.Login(context.Background(), d, &broken); err != nil || c.About != offered || d.Asked()[0].Default != offered {
		t.Fatalf("signed in to %+v, %v", c, err)
	}
}

func TestLoginFails(t *testing.T) {
	p := openai.Provider(openai.Offer{})
	ctx := context.Background()
	if _, err := p.Login(ctx, answers([]string{"http://127.0.0.1:1"}, ""), nil); err == nil || !strings.Contains(err.Error(), "cannot list models at http://127.0.0.1:1") {
		t.Fatalf("no endpoint: %v", err)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer empty.Close()
	if _, err := p.Login(ctx, answers([]string{empty.URL}, ""), nil); err == nil || !strings.Contains(err.Error(), "no models available") {
		t.Fatalf("no models: %v", err)
	}
	// The owner gave up, at the endpoint or at the key.
	given, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.Login(given, answers(nil), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("given up at the endpoint: %v", err)
	}
	if _, err := p.Login(given, answers([]string{"http://x"}), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("given up at the key: %v", err)
	}
}

// access is an access of a test: what it holds, or why it cannot be read.
type access struct {
	c   plugin.Credentials
	err error
}

func (a access) Credentials() (plugin.Credentials, error) { return a.c, a.err }
func (a access) Renew(func(*plugin.Credentials) error) error {
	return errors.New("an API key is not renewed")
}

// An access that cannot be read reaches nothing, and says why.
func TestUnreadableAccess(t *testing.T) {
	p := openai.Provider(openai.Offer{})
	boom := errors.New("boom")
	for _, a := range []access{{err: boom}, {c: plugin.Credentials{Data: []byte(`{}`)}}} {
		if _, err := p.Models(context.Background(), a); err == nil {
			t.Errorf("Models of %+v should fail", a)
		}
		if _, err := p.Model(a).Complete(context.Background(), "m", nil, nil); err == nil {
			t.Errorf("the model of %+v should fail", a)
		}
	}
	if _, err := p.Model(access{err: boom}).Complete(context.Background(), "m", nil, nil); !errors.Is(err, boom) {
		t.Errorf("the model tells %v", err)
	}
	// Nor is it known to models.dev, which knows a provider by its endpoint.
	if got := p.Catalog(access{err: boom}); got != "" {
		t.Errorf("catalog of an unreadable access %q", got)
	}
	if got := p.Catalog(access{c: client.Credentials("https://opencode.ai/zen/go/v1/", "sk")}); got != "https://opencode.ai/zen/go/v1" {
		t.Errorf("catalog %q", got)
	}
	// A key the endpoint refuses is told as that.
	url := openaitest.NewServer(t)
	if _, err := p.Models(context.Background(), access{c: client.Credentials(url, "sk-bad")}); !errors.Is(err, plugin.ErrKeyRejected) {
		t.Errorf("a refused key: %v", err)
	}
}

// Models that cannot chat are not offered, unless there are no others, and a
// preferred one is offered first.
func TestModelsToChooseFrom(t *testing.T) {
	list := func(ids ...string) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			data := make([]string, len(ids))
			for i, id := range ids {
				data[i] = `{"id":"` + id + `"}`
			}
			w.Write([]byte(`{"data":[` + strings.Join(data, ",") + `]}`))
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	p := openai.Provider(openai.Offer{})
	for _, tt := range []struct {
		ids  []string
		chat []string
		def  string
	}{
		{[]string{"gpt-4o", "tts-1", "text-embedding-3", "o3"}, []string{"gpt-4o", "o3"}, "gpt-4o"},
		{[]string{"tts-1"}, []string{"tts-1"}, "tts-1"},
		{[]string{"a", "gpt-4o-mini", "gpt-5-mini-2026"}, []string{"a", "gpt-4o-mini", "gpt-5-mini-2026"}, "gpt-5-mini-2026"},
		{[]string{"b", "a"}, []string{"a", "b"}, "a"},
	} {
		models, err := p.Models(context.Background(), access{c: client.Credentials(list(tt.ids...), "")})
		if err != nil || !reflect.DeepEqual(models.Chat, tt.chat) || models.Default != tt.def || len(models.All) != len(tt.ids) {
			t.Errorf("%v: %+v, %v", tt.ids, models, err)
		}
	}
}
