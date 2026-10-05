package setup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/openai/openaitest"
)

// session is the setup of a new node, with the fake API offered and no key.
func session(t *testing.T) (*Session, string) {
	t.Helper()
	url := openaitest.NewServer(t)
	return New(home.Dir(t.TempDir()), Offer{BaseURL: url}, config.Config{}), url
}

func TestConnect(t *testing.T) {
	s, url := session(t)
	ctx := context.Background()
	if _, err := s.Connect(ctx, url, "sk-bad"); !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("a bad key: %v", err)
	}
	if err := s.Save("gpt-4o", "alpha"); err == nil {
		t.Fatal("saved without an endpoint that accepted the key")
	}
	models, err := s.Connect(ctx, url+"/ ", openaitest.Key)
	want := Models{Chat: []string{"gpt-4o", "gpt-4o-mini", "o3-mini"}, All: openaitest.Models, Default: "gpt-4o-mini"}
	if err != nil || !reflect.DeepEqual(models, want) {
		t.Fatalf("got %+v, %v", models, err)
	}
	err = s.Save("o3-mini", " alpha ")
	cfg, _ := s.home.Config()
	if err != nil || cfg.BaseURL != url || cfg.APIKey != openaitest.Key || cfg.Model != "o3-mini" || cfg.Name != "alpha" || cfg.ID() == "" {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	if !reflect.DeepEqual(cfg.Tools, config.DefaultTools()) {
		t.Fatalf("saved %+v", cfg.Tools)
	}
	// What was saved is what the setup starts from next time.
	if s.Endpoint() != url || s.Name() != "alpha" || !s.HasKey(url) {
		t.Fatalf("after saving: %q %q", s.Endpoint(), s.Name())
	}
}

func TestConnectFails(t *testing.T) {
	s, _ := session(t)
	ctx := context.Background()
	if _, err := s.Connect(ctx, "http://127.0.0.1:1", ""); err == nil || !strings.Contains(err.Error(), "cannot list models at http://127.0.0.1:1") {
		t.Fatalf("no endpoint: %v", err)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[]}`))
	}))
	defer empty.Close()
	if _, err := s.Connect(ctx, empty.URL, ""); err == nil || !strings.Contains(err.Error(), "no models available") {
		t.Fatalf("no models: %v", err)
	}
	if s.connected {
		t.Fatal("a failed connect is nothing to save")
	}
}

// A key is only ever sent to the endpoint it was entered for.
func TestSavedKey(t *testing.T) {
	offered := openaitest.NewServer(t)
	saved := openaitest.NewServerWithKey(t, "sk-saved")
	other := openaitest.NewServerWithKey(t, "sk-other")
	s := New(home.Dir(t.TempDir()), Offer{BaseURL: offered + "/", APIKey: openaitest.Key},
		config.Config{BaseURL: saved, APIKey: "sk-saved", Model: "o3-mini", Name: "alpha"})
	ctx := context.Background()
	for endpoint, has := range map[string]bool{offered: true, saved + "/": true, other: false} {
		if s.HasKey(endpoint) != has {
			t.Errorf("HasKey(%s) = %v", endpoint, !has)
		}
	}
	if models, err := s.Saved(ctx, saved); err != nil || models.Default != "o3-mini" {
		t.Fatalf("saved endpoint: %+v, %v", models, err)
	}
	if _, err := s.Saved(ctx, offered); err != nil {
		t.Fatalf("offered endpoint: %v", err)
	}
	if _, err := s.Saved(ctx, other); !errors.Is(err, ErrKeyRejected) {
		t.Fatalf("another endpoint gets no key: %v", err)
	}
	// The node without a key of its own keeps to what is offered.
	s = New(home.Dir(t.TempDir()), Offer{BaseURL: offered, APIKey: openaitest.Key}, config.Config{BaseURL: offered})
	if !s.HasKey(offered) {
		t.Fatal("the offered key belongs to the offered endpoint")
	}
}

func TestSaveKeepsWhatTheNodeHas(t *testing.T) {
	url := openaitest.NewServer(t)
	key := identity.Generate().Private()
	tools := &config.Tools{H2A: config.Named(), Protected: &[]string{}} // the owner's decision to protect nothing
	s := New(home.Dir(t.TempDir()), Offer{}, config.Config{BaseURL: url, PrivateKey: key, Tools: tools})
	if _, err := s.Connect(context.Background(), url, openaitest.Key); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("gpt-4o", " "); err == nil {
		t.Fatal("saved without a name")
	}
	if err := s.Save("", "alpha"); err == nil {
		t.Fatal("saved without a model")
	}
	err := s.Save("gpt-4o", "alpha")
	cfg, _ := s.home.Config()
	if err != nil || cfg.PrivateKey != key || !reflect.DeepEqual(cfg.Tools, tools) {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestSaveFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	url := openaitest.NewServer(t)
	s := New(home.Dir(filepath.Join(file, "home")), Offer{}, config.Config{})
	if _, err := s.Connect(context.Background(), url, openaitest.Key); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("gpt-4o", "alpha"); err == nil {
		t.Fatal("want error")
	}
	if s.Name() == "alpha" {
		t.Fatal("what was not saved is not what the node has")
	}
}

func TestOpen(t *testing.T) {
	dir := home.Dir(t.TempDir())
	if err := dir.SaveConfig(config.Config{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, Offer{BaseURL: "http://offered"})
	if err != nil || s.Name() != "alpha" || s.Endpoint() != "http://offered" {
		t.Fatalf("got %+v, %v", s, err)
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(home.Dir(broken), Offer{}); err == nil {
		t.Fatal("want error")
	}
}

func TestRights(t *testing.T) {
	owner, peers, protected := New("", Offer{}, config.Config{}).Rights()
	if !slices.Equal(owner, []string{config.AllTools}) || len(peers) != 0 || !slices.Equal(protected, config.DefaultProtected()) {
		t.Fatalf("a new node: %v %v %v", owner, peers, protected)
	}
	// A node from before there were tools for files keeps its lists and
	// gets what is protected; the configuration it has is left as it is.
	old := &config.Tools{H2A: config.Named("read"), A2A: config.Named("list_peers")}
	owner, peers, protected = New("", Offer{}, config.Config{Tools: old}).Rights()
	if !slices.Equal(owner, []string{"read"}) || !slices.Equal(peers, []string{"list_peers"}) || !slices.Equal(protected, config.DefaultProtected()) {
		t.Fatalf("a node from before: %v %v %v", owner, peers, protected)
	}
	if old.Protected != nil {
		t.Fatal("the tools of the node were changed")
	}
}

func TestChatModels(t *testing.T) {
	got := chatModels([]string{"gpt-4o", "tts-1", "text-embedding-3", "o3"})
	if !slices.Equal(got, []string{"gpt-4o", "o3"}) {
		t.Fatalf("got %v", got)
	}
	if got := chatModels([]string{"tts-1"}); !slices.Equal(got, []string{"tts-1"}) {
		t.Fatalf("fallback: got %v", got)
	}
}

func TestDefaultModel(t *testing.T) {
	tests := []struct {
		models  []string
		current string
		want    string
	}{
		{[]string{"a", "gpt-4o-mini", "gpt-5-mini-2026"}, "", "gpt-5-mini-2026"},
		{[]string{"a", "gpt-4o-mini"}, "a", "a"},
		{[]string{"a", "b"}, "gone", "a"},
	}
	for _, tt := range tests {
		if got := defaultModel(tt.models, tt.current); got != tt.want {
			t.Errorf("defaultModel(%v, %q) = %q, want %q", tt.models, tt.current, got, tt.want)
		}
	}
}

func TestResolve(t *testing.T) {
	models := Models{Chat: []string{"a", "b"}, All: []string{"a", "b", "tts"}}
	tests := []struct {
		pick string
		want string
		ok   bool
	}{
		{"2", "b", true}, {"1", "a", true}, {"0", "", false}, {"3", "", false}, {"tts", "tts", true}, {"x", "x", false},
	}
	for _, tt := range tests {
		if got, ok := models.Resolve(tt.pick); got != tt.want || ok != tt.ok {
			t.Errorf("Resolve(%q) = %q, %v", tt.pick, got, ok)
		}
	}
}

// A host without a name still gives the node one.
func TestHostname(t *testing.T) {
	old := osHostname
	t.Cleanup(func() { osHostname = old })
	osHostname = func() (string, error) { return "", errors.New("no name") }
	if got := hostname(); got != "ouroboros" {
		t.Fatalf("got %q", got)
	}
	osHostname = func() (string, error) { return "box", nil }
	if got := New("", Offer{}, config.Config{}).Name(); got != "box" {
		t.Fatalf("got %q", got)
	}
}
