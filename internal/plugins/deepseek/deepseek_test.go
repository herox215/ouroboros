package deepseek_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/llm/llmtest"
	"ouroboros/internal/openai"
	"ouroboros/internal/plugins/deepseek"
	"ouroboros/plugin"
)

const key = "sk-deepseek-test"

type access struct{ c plugin.Credentials }

func (a access) Credentials() (plugin.Credentials, error)    { return a.c, nil }
func (a access) Renew(func(*plugin.Credentials) error) error { return nil }

func api(t *testing.T, status int, models ...string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		if status != 0 {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":{"message":"unavailable"}}`))
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /models":
			data := []map[string]string{}
			for _, id := range models {
				data = append(data, map[string]string{"id": id})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "POST /chat/completions":
			var in struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Errorf("request: %v", err)
			}
			json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "from " + in.Model}}}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func dialog(keys ...string) *llmtest.Dialog {
	return &llmtest.Dialog{Answers: map[string][]string{"DeepSeek API key": keys}}
}

// DeepSeek is offered by the standard build, independently of custom providers.
func TestStandardProvider(t *testing.T) {
	var ctx *plugin.Context
	spy := plugin.New("spy", func(c *plugin.Context, _ struct{}) error { ctx = c; return nil })
	plugins := append(cli.Plugins(clitest.Settings(t), cli.Terminal{}), spy)
	if _, err := cli.NewWith(plugins...); err != nil {
		t.Fatal(err)
	}
	for _, p := range plugin.All[plugin.Provider](ctx) {
		if p.Name == deepseek.Name && p.Title == "DeepSeek" {
			if deepseek.BaseURL != "https://api.deepseek.com" || deepseek.Plugin(deepseek.Options{}).Name != p.Name {
				t.Fatal("incorrect DeepSeek registration")
			}
			return
		}
	}
	t.Fatal("DeepSeek is missing")
}

func TestLoginAndAnswer(t *testing.T) {
	url := api(t, 0, "deepseek-v4-pro", "deepseek-flash")
	p := deepseek.Provider(deepseek.Options{BaseURL: url + "/"})
	d := dialog(" ", "bad", " "+key+" ")
	c, err := p.Login(context.Background(), d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "DeepSeek" || c.About != url || !reflect.DeepEqual(c.Secrets(), []string{key}) || strings.Contains(string(c.Data), key) {
		t.Fatalf("invalid credentials metadata or secret storage")
	}
	if !reflect.DeepEqual(d.Rejected(), []string{"the API key is needed", "API key rejected, try again"}) {
		t.Fatalf("rejections: %v", d.Rejected())
	}
	for _, q := range d.Asked() {
		if q.Title != "DeepSeek API key" || !q.Secret {
			t.Fatalf("unexpected prompt: %s", q.Title)
		}
	}
	a := access{c}
	models, err := p.Models(context.Background(), a)
	if err != nil || models.Default != "deepseek-flash" || len(models.Chat) != 2 {
		t.Fatalf("models: %+v, %v", models, err)
	}
	reply, err := p.Model(a).Complete(context.Background(), models.Default, []plugin.ChatMessage{{Role: "user", Content: "hello"}}, nil)
	if err != nil || reply.Content != "from deepseek-flash" {
		t.Fatalf("reply: %+v, %v", reply, err)
	}
	if p.Catalog(a) != "deepseek" {
		t.Fatal("wrong model catalog")
	}
}

func TestKnownKey(t *testing.T) {
	url := api(t, 0, "deepseek-v4-pro")
	for _, tc := range []struct {
		name  string
		offer string
		have  *plugin.Credentials
		known bool
	}{
		{"environment", key, nil, true},
		{"saved", "wrong", ptr(openai.Credentials(url, key)), true},
		{"other endpoint", "", ptr(openai.Credentials("https://other.example", "other-secret")), false},
		{"unreadable", "", &plugin.Credentials{Data: []byte(`{`)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := deepseek.Provider(deepseek.Options{BaseURL: url, APIKey: tc.offer})
			d := dialog(key)
			if tc.known {
				d = dialog("")
			}
			c, err := p.Login(context.Background(), d, tc.have)
			if err != nil {
				t.Fatal(err)
			}
			q := d.Asked()[0]
			if q.Known != tc.known || (tc.known && q.Default != key) || (!tc.known && q.Default != "") {
				t.Fatal("wrong key offered")
			}
			models, err := p.Models(context.Background(), access{c})
			if err != nil || models.Default != "deepseek-v4-pro" {
				t.Fatalf("fallback: %+v, %v", models, err)
			}
		})
	}
	// A rejected offered key is not offered again.
	p := deepseek.Provider(deepseek.Options{BaseURL: url, APIKey: "wrong"})
	d := dialog("", key)
	if _, err := p.Login(context.Background(), d, nil); err != nil {
		t.Fatal(err)
	}
	if q := d.Asked()[1]; q.Known || q.Default != "" {
		t.Fatal("rejected key offered again")
	}
}

func ptr(c plugin.Credentials) *plugin.Credentials { return &c }

func TestLoginFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		models []string
	}{
		{"empty models", 0, nil}, {"server unavailable", http.StatusServiceUnavailable, []string{"deepseek-flash"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := deepseek.Provider(deepseek.Options{BaseURL: api(t, tc.status, tc.models...)})
			if _, err := p.Login(context.Background(), dialog(key), nil); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := deepseek.Provider(deepseek.Options{}).Login(ctx, dialog(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
