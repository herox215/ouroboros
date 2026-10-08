package node

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/credentials"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/llm"
	"ouroboros/internal/llm/llmtest"
	"ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
	openaiplugin "ouroboros/internal/plugins/openai"
	"ouroboros/plugin"
)

// settings are what the setup of a node starts from.
type settings struct {
	Home    string
	BaseURL string // default endpoint: a fake API
	APIKey  string // key offered for it
	// Providers are the providers besides the one for OpenAI-compatible APIs.
	Providers []plugin.Provider
}

func testSettings(t *testing.T) settings {
	t.Helper()
	return settings{Home: t.TempDir(), BaseURL: openaitest.NewServer(t)}
}

// nodeConfig is a complete configuration for a node using the fake API of s,
// as a node from before there were accesses has it.
func nodeConfig(s settings) config.Config {
	return config.Config{BaseURL: s.BaseURL, APIKey: openaitest.Key, Model: "gpt-4o", Name: "alpha"}
}

// existing saves cfg as the configuration of the node of s and returns it as
// the node reads it: with an access in place of the endpoint and the key.
func existing(t *testing.T, s settings, cfg config.Config) config.Config {
	t.Helper()
	dir := home.Dir(s.Home)
	if err := dir.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	read, err := dir.Config()
	if err != nil {
		t.Fatal(err)
	}
	return read
}

// reached returns the endpoint and the key of the access the node of s
// answers through.
func reached(t *testing.T, s settings, cfg config.Config) (endpoint, key string) {
	t.Helper()
	access, err := home.Dir(s.Home).Credentials().Get(cfg.Access)
	if err != nil {
		t.Fatalf("the access %q of the node: %v", cfg.Access, err)
	}
	endpoint, key, err = openai.Saved(access.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint, key
}

// newWizard returns the setup of the node of s with a user who types input,
// and what it prints.
func newWizard(s settings, input string) (*wizard, *bytes.Buffer) {
	return newWizardOn(s, plugin.Terminal{In: strings.NewReader(input)})
}

func newWizardOn(s settings, term plugin.Terminal) (*wizard, *bytes.Buffer) {
	var out bytes.Buffer
	term.Out = &out
	providers := append([]plugin.Provider{openaiplugin.Provider(openaiplugin.Offer{BaseURL: s.BaseURL, APIKey: s.APIKey})}, s.Providers...)
	return &wizard{term: plugin.NewConsole(term), home: home.Dir(s.Home), providers: func() []plugin.Provider { return providers }}, &out
}

func TestSetupRetriesKeyAndModel(t *testing.T) {
	s := testSettings(t)
	w, out := newWizard(s, "\nsk-bad\n"+openaitest.Key+"\n99\nnope\n2\nmynode\n")
	cfg, err := w.run(context.Background(), config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "gpt-4o-mini" || cfg.Name != "mynode" || cfg.ID() == "" || cfg.BaseURL != "" || cfg.APIKey != "" {
		t.Fatalf("got %+v", cfg)
	}
	if endpoint, key := reached(t, s, cfg); endpoint != s.BaseURL || key != openaitest.Key {
		t.Fatalf("the node reaches %q with %q", endpoint, key)
	}
	if strings.Count(out.String(), "rejected") != 1 || strings.Count(out.String(), "unknown model") != 2 {
		t.Fatalf("output %q", out.String())
	}
	// With one provider there is nothing to pick, and a new node has no
	// access to choose.
	if strings.Contains(out.String(), "Providers:") || strings.Contains(out.String(), "Accesses:") {
		t.Fatalf("asked for a provider or an access: %q", out.String())
	}
	for _, asked := range []string{"Any OpenAI-compatible API:", "LLM endpoint [" + s.BaseURL + "]: ", "for " + s.BaseURL, "API key (Enter for none): "} {
		if !strings.Contains(out.String(), asked) {
			t.Errorf("no %q in %q", asked, out.String())
		}
	}
	if saved, _ := config.Load(s.Home); !reflect.DeepEqual(saved, cfg) {
		t.Fatalf("saved %+v", saved)
	}
	// The key is in the credentials and not in the configuration.
	if written, _ := os.ReadFile(filepath.Join(s.Home, "config.json")); strings.Contains(string(written), openaitest.Key) {
		t.Fatalf("the configuration holds the key: %s", written)
	}
}

func TestSetupDefaults(t *testing.T) {
	s := testSettings(t)
	s.APIKey = openaitest.Key // from OPENAI_API_KEY, no prompt
	w, out := newWizard(s, "\n\n\n")
	cfg, err := w.run(context.Background(), config.Config{})
	if err != nil || cfg.Model != "gpt-4o-mini" || cfg.Name != host(t) {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	if endpoint, key := reached(t, s, cfg); endpoint != s.BaseURL || key != openaitest.Key {
		t.Fatalf("the node reaches %q with %q", endpoint, key)
	}
	if strings.Contains(out.String(), "API key") {
		t.Fatalf("asked for the key that was offered: %q", out.String())
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
	s = testSettings(t)
	old := nodeConfig(s)
	old.Tools = &config.Tools{A2A: config.Named("list_peers")}
	w, _ = newWizard(s, "\n\n\n")
	cfg, err := w.run(context.Background(), existing(t, s, old))
	want := &config.Tools{A2A: config.Named("list_peers"), Protected: config.DefaultTools().Protected}
	if err != nil || !reflect.DeepEqual(cfg.Tools, want) {
		t.Fatalf("a node from before: %+v, %v", cfg.Tools, err)
	}
}

// A node that is set up keeps what it has with Enter alone: its access, as
// it is, its model and its name.
func TestSetupKeepsExistingValues(t *testing.T) {
	s := testSettings(t)
	old := nodeConfig(s)
	old.PrivateKey = identity.Generate().Private()                         // and with it the ID of the node
	old.Tools = &config.Tools{H2A: config.Named(), Protected: &[]string{}} // the owner's decision to protect nothing
	want := existing(t, s, old)
	before, _ := home.Dir(s.Home).Credentials().Get(want.Access)
	s.BaseURL = "http://other.invalid" // the saved endpoint wins over the default
	w, out := newWizard(s, "\n\n\n")
	cfg, err := w.run(context.Background(), want)
	if err != nil || !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	if after, _ := home.Dir(s.Home).Credentials().Get(cfg.Access); !reflect.DeepEqual(after, before) {
		t.Fatalf("the access was written anew: %+v", after)
	}
	if !strings.Contains(out.String(), "    1) localhost · "+before.About+"  (in use)\n") || !strings.Contains(out.String(), "Access [number, Enter = 1]: ") {
		t.Fatalf("output %q", out.String())
	}
}

func TestSetupWithoutAPIKey(t *testing.T) {
	s := testSettings(t)
	url := openaitest.NewServerWithKey(t, "") // e.g. a local Ollama
	w, _ := newWizard(s, url+"/\n\n1\nlocal\n")
	cfg, err := w.run(context.Background(), config.Config{})
	if err != nil || cfg.Model != "gpt-4o" || !cfg.Complete() {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	if endpoint, key := reached(t, s, cfg); endpoint != url || key != "" {
		t.Fatalf("the node reaches %q with %q", endpoint, key)
	}
}

// Signing in to an access again at another endpoint asks for the key of that
// one: the key that is saved stays with its endpoint.
func TestSetupNeverSendsKeyToAnotherEndpoint(t *testing.T) {
	s := testSettings(t)
	other := openaitest.NewServerWithKey(t, "sk-other")
	old := existing(t, s, nodeConfig(s)) // saved key belongs to s.BaseURL
	w, out := newWizard(s, "l 1\n"+other+"\nsk-other\n1\nn\n")
	cfg, err := w.run(context.Background(), old)
	if err != nil || cfg.Access != old.Access {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	if endpoint, key := reached(t, s, cfg); endpoint != other || key != "sk-other" {
		t.Fatalf("the node reaches %q with %q", endpoint, key)
	}
	if strings.Contains(out.String(), "rejected") {
		t.Fatal("old key must not be tried against the new endpoint")
	}
}

// A saved key the endpoint no longer takes is asked for again.
func TestSetupAsksAgainForARejectedKey(t *testing.T) {
	s := testSettings(t)
	revoked := nodeConfig(s)
	revoked.APIKey = "sk-revoked"
	old := existing(t, s, revoked)
	w, out := newWizard(s, "\n\n"+openaitest.Key+"\n\n\n")
	cfg, err := w.run(context.Background(), old)
	if err != nil || cfg.Access != old.Access {
		t.Fatalf("got %+v, %v, output %q", cfg, err, out.String())
	}
	if _, key := reached(t, s, cfg); key != openaitest.Key {
		t.Fatalf("the node kept the key %q", key)
	}
	if !strings.Contains(out.String(), "✗ API key rejected: sign in again") || strings.Count(out.String(), "✗ API key rejected, try again") != 1 {
		t.Fatalf("output %q", out.String())
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
	// The access can be kept, the configuration cannot be written.
	s = testSettings(t)
	s.APIKey = openaitest.Key
	os.Mkdir(filepath.Join(s.Home, "config.json.lock"), 0o700)
	w, _ = newWizard(s, "\n\n\n")
	if _, err := w.run(context.Background(), config.Config{}); err == nil {
		t.Fatal("want error")
	}
}

func TestSetupUsesReadSecret(t *testing.T) {
	var prompted string
	w, out := newWizardOn(testSettings(t), plugin.Terminal{In: strings.NewReader("\n1\nx\n"), ReadSecret: func(prompt string) (string, error) {
		prompted = prompt
		return openaitest.Key, nil
	}})
	// The setup prints the question itself, and has the answer read without
	// showing it.
	prompted = "not asked"
	_, err := w.run(context.Background(), config.Config{})
	if err != nil || prompted != "" || !strings.Contains(out.String(), "API key (Enter for none): ") {
		t.Fatalf("prompt %q, err %v, output %q", prompted, err, out.String())
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
	// And at the access, and at the provider, where there are some to pick.
	s := testSettings(t)
	old := existing(t, s, nodeConfig(s))
	w, _ := newWizard(s, "")
	if _, err := w.run(context.Background(), old); err == nil {
		t.Error("access: want error")
	}
	s.Providers = []plugin.Provider{llmtest.Provider("two")}
	w, _ = newWizard(s, "a\n")
	if _, err := w.run(context.Background(), old); err == nil {
		t.Error("provider: want error")
	}
}

func TestSetupNetworkError(t *testing.T) {
	s := testSettings(t)
	w, _ := newWizard(s, "http://127.0.0.1:1\n\n")
	if _, err := w.run(context.Background(), config.Config{}); err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("got %v", err)
	}
	// An access that is saved and not reached is no reason to sign in anew.
	s.BaseURL = "http://127.0.0.1:1"
	old := existing(t, s, nodeConfig(s))
	w, out := newWizard(s, "1\n")
	if _, err := w.run(context.Background(), old); err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") || strings.Contains(out.String(), "API key rejected") {
		t.Fatalf("got %v, output %q", err, out.String())
	}
}

// With several providers the setup asks which one signs in, and each asks
// what it needs: the second one is no OpenAI-compatible API and answers a
// message all the same.
func TestSetupWithASecondProvider(t *testing.T) {
	s := testSettings(t)
	s.Providers = []plugin.Provider{llmtest.Provider("two")}
	w, out := newWizard(s, "0\n9\nx\n2\nbad\ngood\n2\nbeta\n")
	cfg, err := w.run(context.Background(), config.Config{})
	if err != nil || cfg.Model != "two-large" || cfg.Name != "beta" {
		t.Fatalf("got %+v, %v, output %q", cfg, err, out.String())
	}
	for _, want := range []string{"Providers:\n\n    1) Custom Provider\n    2) Test two\n", "Provider [number, Enter = 1]: ", "the token of two\nToken: ", "✗ wrong token"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in %q", want, out.String())
		}
	}
	if strings.Count(out.String(), "✗ unknown provider") != 3 {
		t.Fatalf("output %q", out.String())
	}
	access, err := home.Dir(s.Home).Credentials().Get(cfg.Access)
	if err != nil || access.Provider != "two" {
		t.Fatalf("the access: %+v, %v", access, err)
	}
	book := llm.Book{Store: home.Dir(s.Home).Credentials(), Providers: w.providers}
	model, err := book.Model(cfg.Access)
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := model.Complete(context.Background(), cfg.Model, []plugin.ChatMessage{{Role: "user", Content: "hi"}}, nil); err != nil || reply.Content != "[two two-large as good] hi" {
		t.Fatalf("the second provider answers %+v, %v", reply, err)
	}
}

// The accesses of a node are managed where it is set up: one is added,
// renamed, signed in to again and removed, and one is picked to answer.
func TestSetupManagesAccesses(t *testing.T) {
	s := testSettings(t)
	s.Providers = []plugin.Provider{llmtest.Provider("two")}
	old := existing(t, s, nodeConfig(s))
	store := home.Dir(s.Home).Credentials()

	// Add an access of the second provider, which is in use from then on.
	w, _ := newWizard(s, "a\n2\ngood-work\n\n\n")
	cfg, err := w.run(context.Background(), old)
	if err != nil || cfg.Access == old.Access || cfg.Model != "two-small" {
		t.Fatalf("added: %+v, %v", cfg, err)
	}
	second := cfg.Access

	// Rename both, look at the list, and go back to the first.
	w, out := newWizard(s, "r 2 Work account\nr 1 Local\nx\n7\nr 9 no\nd\n1\n\n\n")
	cfg, err = w.run(context.Background(), cfg)
	if err != nil || cfg.Access != old.Access || cfg.Model != "gpt-4o-mini" {
		t.Fatalf("picked: %+v, %v, output %q", cfg, err, out.String())
	}
	if !strings.Contains(out.String(), "    1) Local · "+s.BaseURL+"\n    2) Work account · account good-work  (in use)\n") {
		t.Fatalf("the list: %q", out.String())
	}
	if strings.Count(out.String(), "✗ unknown choice") != 4 || !strings.Contains(out.String(), "Access [number, Enter = 2]: ") {
		t.Fatalf("output %q", out.String())
	}
	if a, _ := store.Get(second); a.Name != "Work account" || a.ID != second {
		t.Fatalf("renamed: %+v", a)
	}

	// Remove the one in use: the other takes over, and the setup goes on
	// with it. The last one cannot be removed.
	w, out = newWizard(s, "d 1\nd 1\n\n\n\n")
	cfg, err = w.run(context.Background(), cfg)
	if err != nil || cfg.Access != second || cfg.Model != "two-small" {
		t.Fatalf("after removing the access in use: %+v, %v, output %q", cfg, err, out.String())
	}
	if !strings.Contains(out.String(), "the agents answer with two-small of Work account now.") || !strings.Contains(out.String(), "✗ the last access cannot be removed: add another one first") {
		t.Fatalf("output %q", out.String())
	}
	if list, _ := store.List(); len(list) != 1 || list[0].ID != second {
		t.Fatalf("accesses %+v", list)
	}
	// An access whose provider is not part of this ouro says so.
	store.Put(credentials.Access{Provider: "gone", Name: "Old"})
	w, out = newWizard(s, "1\n\n\n")
	if _, err = w.run(context.Background(), cfg); err != nil || !strings.Contains(out.String(), "2) Old  (its provider gone is not part of this ouro)") {
		t.Fatalf("%v, output %q", err, out.String())
	}
}

func TestSetupFailsWithoutWhatItNeeds(t *testing.T) {
	// No provider at all.
	var out bytes.Buffer
	w := &wizard{term: plugin.NewConsole(plugin.Terminal{In: strings.NewReader(""), Out: &out}), home: home.Dir(t.TempDir()),
		providers: func() []plugin.Provider { return nil }}
	if _, err := w.run(context.Background(), config.Config{}); err == nil || !strings.Contains(err.Error(), "no provider to sign in with") {
		t.Fatalf("without a provider: %v", err)
	}
	// Credentials that cannot be read.
	s := testSettings(t)
	os.WriteFile(filepath.Join(s.Home, credentials.FileName), []byte("{"), 0o600)
	w, _ = newWizard(s, "\n")
	if _, err := w.run(context.Background(), config.Config{}); err == nil {
		t.Fatal("want error")
	}
	// A name that cannot be saved.
	s = testSettings(t)
	old := existing(t, s, nodeConfig(s))
	w, _ = newWizard(s, "r 1 New\n")
	os.Mkdir(filepath.Join(s.Home, credentials.LockName), 0o700)
	os.Remove(filepath.Join(s.Home, credentials.LockName))
	os.Chmod(s.Home, 0o500)
	t.Cleanup(func() { os.Chmod(s.Home, 0o700) })
	if _, err := w.run(context.Background(), old); err == nil {
		t.Fatal("renaming in a home that cannot be written should fail")
	}
}

// A provider may give up a question, as when the browser brought what it
// asked to be pasted. The line the terminal is still waiting for then
// answers the next question.
func TestSetupGoesOnAfterAQuestionThatWasGivenUp(t *testing.T) {
	var opened []string
	old := llm.Browse
	t.Cleanup(func() { llm.Browse = old })
	llm.Browse = func(url string) error {
		opened = append(opened, url)
		return errors.New("no browser here")
	}
	browser := llmtest.Provider("browser")
	token := browser.Login
	browser.Login = func(ctx context.Context, d plugin.Dialog, have *plugin.Credentials) (plugin.Credentials, error) {
		d.Tell("Sign in on the page.")
		d.Open("https://example.com/sign-in")
		asking, done := context.WithCancel(ctx)
		given := make(chan error)
		go func() {
			_, err := d.Ask(asking, plugin.Question{Title: "Paste the address", Hint: "waiting for the browser"})
			given <- err
		}()
		done() // the browser was faster
		if err := <-given; !errors.Is(err, context.Canceled) {
			return plugin.Credentials{}, errors.New("the question was not given up")
		}
		return token(ctx, d, have)
	}
	s := testSettings(t)
	s.Providers = []plugin.Provider{browser}
	w, out := newWizard(s, "2\ngood\n\nnode\n")
	cfg, err := w.run(context.Background(), config.Config{})
	if err != nil || cfg.Model != "browser-small" || cfg.Name != "node" {
		t.Fatalf("got %+v, %v, output %q", cfg, err, out.String())
	}
	if !reflect.DeepEqual(opened, []string{"https://example.com/sign-in"}) {
		t.Fatalf("opened %v", opened)
	}
	for _, want := range []string{"Sign in on the page.\n", "Open this page in your browser:\n\n  https://example.com/sign-in\n", "Paste the address (waiting for the browser): "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in %q", want, out.String())
		}
	}
}

func TestPrintColumns(t *testing.T) {
	var b bytes.Buffer
	printColumns(&b, []string{"a", "bb", "ccc"})
	if got := b.String(); got != "    1) a      2) bb     3) ccc\n" {
		t.Fatalf("got %q", got)
	}
	// As many columns as fit into 96 cells, each as wide as the longest item
	// and its number; the last row ends where the items end.
	b.Reset()
	printColumns(&b, []string{"model-0001", "model-0002", "model-0003", "model-0004", "model-0005", "model-0006", "m7"})
	want := "    1) model-0001    2) model-0002    3) model-0003    4) model-0004    5) model-0005\n" +
		"    6) model-0006    7) m7        \n"
	if got := b.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// An item too wide for two columns has a row of its own.
	b.Reset()
	wide := strings.Repeat("w", 50)
	printColumns(&b, []string{wide, "b"})
	if got := b.String(); got != "    1) "+wide+"\n    2) b"+strings.Repeat(" ", 49)+"\n" {
		t.Fatalf("got %q", got)
	}
	b.Reset()
	if printColumns(&b, nil); b.Len() != 0 {
		t.Fatalf("no models print %q", b.String())
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

// A question that is given up in the moment its line comes leaves the line
// to the next question: nothing the user typed is lost.
func TestGivenUpQuestionKeepsTheLine(t *testing.T) {
	var out bytes.Buffer
	l := &lines{term: plugin.NewConsole(plugin.Terminal{In: strings.NewReader(""), Out: &out}), out: &serial{w: &out}}
	given, cancel := context.WithCancel(context.Background())
	cancel()
	// Both are there at once, the line and the giving up: whichever the
	// question sees first, it ends as given up and the line stays.
	for range 200 {
		l.waiting = make(chan answer, 1)
		l.waiting <- answer{line: "typed"}
		if got, err := l.prompt(given, "? ", "", false); !errors.Is(err, context.Canceled) || got != "" {
			t.Fatalf("a question that was given up: %q, %v", got, err)
		}
		if got, err := l.prompt(context.Background(), "? ", "", false); err != nil || got != "typed" {
			t.Fatalf("the next question: %q, %v", got, err)
		}
		if l.waiting != nil {
			t.Fatal("the line was answered: nothing is waited for")
		}
	}
}
