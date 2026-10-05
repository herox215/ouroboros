package node

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/openai/openaitest"
	"ouroboros/internal/setup"
	"ouroboros/plugin"
)

// settings are what the setup of a node starts from.
type settings struct {
	Home    string
	BaseURL string // default endpoint: a fake API
	APIKey  string // key offered for it
}

func testSettings(t *testing.T) settings {
	t.Helper()
	return settings{Home: t.TempDir(), BaseURL: openaitest.NewServer(t)}
}

// nodeConfig is a complete configuration for a node using the fake API of s.
func nodeConfig(s settings) config.Config {
	return config.Config{BaseURL: s.BaseURL, APIKey: openaitest.Key, Model: "gpt-4o", Name: "alpha"}
}

// newWizard returns the setup of the node of s with a user who types input,
// and what it prints.
func newWizard(s settings, input string) (*wizard, *bytes.Buffer) {
	return newWizardOn(s, plugin.Terminal{In: strings.NewReader(input)})
}

func newWizardOn(s settings, term plugin.Terminal) (*wizard, *bytes.Buffer) {
	var out bytes.Buffer
	term.Out = &out
	return &wizard{term: plugin.NewConsole(term), home: home.Dir(s.Home), offer: setup.Offer{BaseURL: s.BaseURL, APIKey: s.APIKey}}, &out
}

func TestSetupRetriesKeyAndModel(t *testing.T) {
	s := testSettings(t)
	w, out := newWizard(s, "\nsk-bad\n"+openaitest.Key+"\n99\nnope\n2\nmynode\n")
	cfg, err := w.run(context.Background(), config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != s.BaseURL || cfg.APIKey != openaitest.Key || cfg.Model != "gpt-4o-mini" || cfg.Name != "mynode" || cfg.ID() == "" {
		t.Fatalf("got %+v", cfg)
	}
	if strings.Count(out.String(), "rejected") != 1 || strings.Count(out.String(), "unknown model") != 2 {
		t.Fatalf("output %q", out.String())
	}
	if saved, _ := config.Load(s.Home); !reflect.DeepEqual(saved, cfg) {
		t.Fatalf("saved %+v", saved)
	}
}

func TestSetupDefaults(t *testing.T) {
	s := testSettings(t)
	s.APIKey = openaitest.Key // from OPENAI_API_KEY, no prompt
	w, _ := newWizard(s, "\n\n\n")
	cfg, err := w.run(context.Background(), config.Config{})
	if err != nil || cfg.BaseURL != s.BaseURL || cfg.Model != "gpt-4o-mini" || cfg.Name != host(t) {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

// The setup writes out what the agent may do, for the owner to see and
// change, and keeps what the owner wrote.
func TestSetupWritesTheTools(t *testing.T) {
	s := testSettings(t)
	s.APIKey = openaitest.Key
	w, _ := newWizard(s, "\n\n\n")
	if _, err := w.run(context.Background(), config.Config{}); err != nil {
		t.Fatal(err)
	}
	saved, err := config.Load(s.Home)
	if err != nil || !reflect.DeepEqual(saved.Tools, config.DefaultTools()) {
		t.Fatalf("a new node: %+v, %v", saved.Tools, err)
	}

	// A node from before there were tools for files keeps its lists and
	// gets what is protected.
	old := nodeConfig(s)
	old.Tools = &config.Tools{A2A: config.Named("list_peers")}
	w, _ = newWizard(s, "\n\n\n")
	cfg, err := w.run(context.Background(), old)
	want := &config.Tools{A2A: config.Named("list_peers"), Protected: config.DefaultTools().Protected}
	if err != nil || !reflect.DeepEqual(cfg.Tools, want) {
		t.Fatalf("a node from before: %+v, %v", cfg.Tools, err)
	}
}

func TestSetupKeepsExistingValues(t *testing.T) {
	s := testSettings(t)
	s.BaseURL = "http://other.invalid" // the saved endpoint wins over the default
	want := nodeConfig(testSettings(t))
	want.PrivateKey = identity.Generate().Private()                         // and with it the ID of the node
	want.Tools = &config.Tools{H2A: config.Named(), Protected: &[]string{}} // the owner's decision to protect nothing
	w, _ := newWizard(s, "\n\n\n")
	cfg, err := w.run(context.Background(), want)
	if err != nil || !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestSetupWithoutAPIKey(t *testing.T) {
	s := testSettings(t)
	url := openaitest.NewServerWithKey(t, "") // e.g. a local Ollama
	w, _ := newWizard(s, url+"/\n\n1\nlocal\n")
	cfg, err := w.run(context.Background(), config.Config{})
	if err != nil || cfg.BaseURL != url || cfg.APIKey != "" || cfg.Model != "gpt-4o" || !cfg.Complete() {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestSetupNeverSendsKeyToAnotherEndpoint(t *testing.T) {
	s := testSettings(t)
	s.APIKey = openaitest.Key
	other := openaitest.NewServerWithKey(t, "sk-other")
	w, out := newWizard(s, other+"\nsk-other\n1\nn\n")
	old := nodeConfig(s) // saved key belongs to s.BaseURL
	cfg, err := w.run(context.Background(), old)
	if err != nil || cfg.BaseURL != other || cfg.APIKey != "sk-other" {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	if strings.Contains(out.String(), "rejected") {
		t.Fatal("old key must not be tried against the new endpoint")
	}
}

// A saved key the endpoint no longer takes is asked for again.
func TestSetupAsksAgainForARejectedKey(t *testing.T) {
	s := testSettings(t)
	old := nodeConfig(s)
	old.APIKey = "sk-revoked"
	w, out := newWizard(s, "\n"+openaitest.Key+"\n\n\n")
	cfg, err := w.run(context.Background(), old)
	if err != nil || cfg.APIKey != openaitest.Key || strings.Count(out.String(), "rejected") != 1 {
		t.Fatalf("got %+v, %v, output %q", cfg, err, out.String())
	}
}

func TestSetupCannotSave(t *testing.T) {
	s := testSettings(t)
	s.APIKey = openaitest.Key
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.Home = filepath.Join(file, "home")
	w, _ := newWizard(s, "\n\n\n")
	if _, err := w.run(context.Background(), config.Config{}); err == nil {
		t.Fatal("want error")
	}
}

func TestSetupUsesReadSecret(t *testing.T) {
	var prompted string
	w, _ := newWizardOn(testSettings(t), plugin.Terminal{In: strings.NewReader("\n1\nx\n"), ReadSecret: func(prompt string) (string, error) {
		prompted = prompt
		return openaitest.Key, nil
	}})
	if _, err := w.run(context.Background(), config.Config{}); err != nil || prompted == "" {
		t.Fatalf("prompt %q, err %v", prompted, err)
	}
}

func TestSetupStopsAtEndOfInput(t *testing.T) {
	inputs := map[string]string{
		"endpoint": "",
		"key":      "\n",
		"model":    "\n" + openaitest.Key + "\n",
		"name":     "\n" + openaitest.Key + "\n1\n",
	}
	for name, input := range inputs {
		w, _ := newWizard(testSettings(t), input)
		if _, err := w.run(context.Background(), config.Config{}); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestSetupNetworkError(t *testing.T) {
	s := testSettings(t)
	w, _ := newWizard(s, "http://127.0.0.1:1\n\n")
	if _, err := w.run(context.Background(), config.Config{}); err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("got %v", err)
	}
}

func TestPrintColumns(t *testing.T) {
	var b bytes.Buffer
	printColumns(&b, []string{"a", "bb", "ccc"})
	if got := b.String(); got != "    1) a      2) bb     3) ccc\n" {
		t.Fatalf("got %q", got)
	}
}

// host is the name of this host, which the setup offers as the name of the node.
func host(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	return h
}
