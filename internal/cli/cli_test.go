package cli

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/discovery"
	"ouroboros/internal/openai/openaitest"
)

// freeUDPPort returns a UDP port that is currently unused.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func testSettings(t *testing.T) Settings {
	t.Helper()
	return Settings{
		Home:      t.TempDir(),
		HTTPPort:  0,
		DiscoPort: freeUDPPort(t),
		BaseURL:   openaitest.NewServer(t),
	}
}

// nodeConfig is a complete configuration for a node using the fake API of s.
func nodeConfig(s Settings) config.Config {
	return config.Config{BaseURL: s.BaseURL, APIKey: openaitest.Key, Model: "gpt-4o", Name: "alpha", ID: "abc123"}
}

func newApp(s Settings, input string) (*App, *bytes.Buffer) {
	var out bytes.Buffer
	app := New(s, strings.NewReader(input), &out)
	app.discoverWait = 300 * time.Millisecond
	return app, &out
}

func run(t *testing.T, s Settings, input string, args ...string) (string, error) {
	t.Helper()
	app, out := newApp(s, input)
	err := app.Run(context.Background(), args)
	return out.String(), err
}

// startNode runs "ouro serve" in the background and returns its port.
func startNode(t *testing.T, s Settings, cfg config.Config) int {
	t.Helper()
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	app, _ := newApp(s, "")
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx, []string{"serve"}) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	p, ok := daemon.WaitRunning(filepath.Join(s.Home, "ouro.pid"), 2*time.Second)
	if !ok {
		t.Fatal("node did not start")
	}
	return p.Port
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}} {
		out, err := run(t, testSettings(t), "", args...)
		if err != nil || !strings.Contains(out, "ouro start") {
			t.Fatalf("%v: got %q, %v", args, out, err)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	if _, err := run(t, testSettings(t), "", "fly"); err == nil {
		t.Fatal("want error")
	}
}

func TestSettingsFromEnv(t *testing.T) {
	t.Setenv("OURO_HOME", "/tmp/ouro")
	t.Setenv("OURO_PORT", "1234")
	t.Setenv("OURO_DISCO_PORT", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "sk-env")
	s, err := SettingsFromEnv()
	want := Settings{Home: "/tmp/ouro", HTTPPort: 1234, DiscoPort: 41338, BaseURL: "https://api.openai.com/v1", APIKey: "sk-env"}
	if err != nil || s != want {
		t.Fatalf("got %+v, %v", s, err)
	}
	t.Setenv("OURO_PORT", "abc")
	if _, err := SettingsFromEnv(); err == nil {
		t.Fatal("want error for invalid port")
	}
}

func TestSetupRetriesKeyAndModel(t *testing.T) {
	s := testSettings(t)
	app, out := newApp(s, "\nsk-bad\n"+openaitest.Key+"\n99\nnope\n2\nmynode\n")
	cfg, err := app.setup(context.Background(), config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != s.BaseURL || cfg.APIKey != openaitest.Key || cfg.Model != "gpt-4o-mini" || cfg.Name != "mynode" || cfg.ID == "" {
		t.Fatalf("got %+v", cfg)
	}
	if strings.Count(out.String(), "rejected") != 1 || strings.Count(out.String(), "unknown model") != 2 {
		t.Fatalf("output %q", out.String())
	}
	if saved, _ := config.Load(s.Home); saved != cfg {
		t.Fatalf("saved %+v", saved)
	}
}

func TestSetupDefaults(t *testing.T) {
	s := testSettings(t)
	s.APIKey = openaitest.Key // from OPENAI_API_KEY, no prompt
	app, _ := newApp(s, "\n\n\n")
	cfg, err := app.setup(context.Background(), config.Config{})
	if err != nil || cfg.BaseURL != s.BaseURL || cfg.Model != "gpt-4o-mini" || cfg.Name != hostname() {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestSetupKeepsExistingValues(t *testing.T) {
	s := testSettings(t)
	s.BaseURL = "http://other.invalid" // the saved endpoint wins over the default
	want := nodeConfig(testSettings(t))
	app, _ := newApp(s, "\n\n\n")
	cfg, err := app.setup(context.Background(), want)
	if err != nil || cfg != want {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestSetupWithoutAPIKey(t *testing.T) {
	s := testSettings(t)
	url := openaitest.NewServerWithKey(t, "") // e.g. a local Ollama
	app, _ := newApp(s, url+"/\n\n1\nlocal\n")
	cfg, err := app.setup(context.Background(), config.Config{})
	if err != nil || cfg.BaseURL != url || cfg.APIKey != "" || cfg.Model != "gpt-4o" || !cfg.Complete() {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestSetupNeverSendsKeyToAnotherEndpoint(t *testing.T) {
	s := testSettings(t)
	s.APIKey = openaitest.Key
	other := openaitest.NewServerWithKey(t, "sk-other")
	app, out := newApp(s, other+"\nsk-other\n1\nn\n")
	old := nodeConfig(s) // saved key belongs to s.BaseURL
	cfg, err := app.setup(context.Background(), old)
	if err != nil || cfg.BaseURL != other || cfg.APIKey != "sk-other" {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	if strings.Contains(out.String(), "rejected") {
		t.Fatal("old key must not be tried against the new endpoint")
	}
}

func TestSetupUsesReadSecret(t *testing.T) {
	app, _ := newApp(testSettings(t), "\n1\nx\n")
	var prompted string
	app.ReadSecret = func(prompt string) (string, error) {
		prompted = prompt
		return openaitest.Key, nil
	}
	if _, err := app.setup(context.Background(), config.Config{}); err != nil || prompted == "" {
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
		app, _ := newApp(testSettings(t), input)
		if _, err := app.setup(context.Background(), config.Config{}); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestSetupNetworkError(t *testing.T) {
	s := testSettings(t)
	app, _ := newApp(s, "http://127.0.0.1:1\n\n")
	if _, err := app.setup(context.Background(), config.Config{}); err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("got %v", err)
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

func TestResolveModel(t *testing.T) {
	listed, all := []string{"a", "b"}, []string{"a", "b", "tts"}
	tests := []struct {
		pick string
		want string
		ok   bool
	}{
		{"2", "b", true}, {"0", "", false}, {"3", "", false}, {"tts", "tts", true}, {"x", "x", false},
	}
	for _, tt := range tests {
		if got, ok := resolveModel(listed, all, tt.pick); got != tt.want || ok != tt.ok {
			t.Errorf("resolveModel(%q) = %q, %v", tt.pick, got, ok)
		}
	}
}

func TestPrintColumns(t *testing.T) {
	var b bytes.Buffer
	printColumns(&b, []string{"a", "bb", "ccc"})
	if got := b.String(); got != "    1) a      2) bb     3) ccc\n" {
		t.Fatalf("got %q", got)
	}
}

func TestMatch(t *testing.T) {
	nodes := []discovery.Node{{ID: "abc123", Name: "alpha"}, {ID: "abd456", Name: "beta"}, {ID: "ab", Name: "gamma"}}
	tests := []struct {
		target, want string
		ok           bool
	}{
		{"abc123", "abc123", true}, // full ID
		{"abc", "abc123", true},    // unique prefix
		{"beta", "abd456", true},   // name
		{"ab", "ab", true},         // exact ID wins over prefix matches
		{"a", "", false},           // ambiguous
		{"zzz", "", false},         // unknown
	}
	for _, tt := range tests {
		n, err := match(nodes, tt.target)
		if (err == nil) != tt.ok || n.ID != tt.want {
			t.Errorf("match(%q) = %+v, %v", tt.target, n, err)
		}
	}
}

func TestServeRequiresConfig(t *testing.T) {
	if _, err := run(t, testSettings(t), "", "serve"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("got %v", err)
	}
}

func TestServeFallsBackToFreePort(t *testing.T) {
	busy, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	s := testSettings(t)
	s.HTTPPort = busy.Addr().(*net.TCPAddr).Port
	if port := startNode(t, s, nodeConfig(s)); port == s.HTTPPort {
		t.Fatal("expected a different port")
	}
}

func TestNodeLifecycle(t *testing.T) {
	s := testSettings(t)
	port := startNode(t, s, nodeConfig(s))

	out, _ := run(t, s, "", "status")
	if !strings.Contains(out, "● running") || !strings.Contains(out, fmt.Sprintf("port=%d", port)) {
		t.Fatalf("status: %q", out)
	}

	out, err := run(t, s, "", "list")
	if err != nil || !strings.Contains(out, "abc123") || !strings.Contains(out, "alpha") || !strings.Contains(out, "gpt-4o") {
		t.Fatalf("list: %q, %v", out, err)
	}

	out, err = run(t, s, "", "chat", "abc", "hello", "world")
	if err != nil || out != "[gpt-4o] turns=1 last=hello world\n" {
		t.Fatalf("chat by ID prefix: %q, %v", out, err)
	}

	out, err = run(t, s, "one\n\ntwo\n", "chat", "alpha")
	if err != nil || !strings.Contains(out, "turns=1 last=one") || !strings.Contains(out, "turns=3 last=two") {
		t.Fatalf("interactive chat should keep history: %q, %v", out, err)
	}

	out, err = run(t, s, "", "chat", fmt.Sprintf("127.0.0.1:%d", port), "direct")
	if err != nil || !strings.Contains(out, "last=direct") {
		t.Fatalf("chat by address: %q, %v", out, err)
	}

	out, _ = run(t, s, "", "start")
	if !strings.Contains(out, "already running") {
		t.Fatalf("second start: %q", out)
	}
}

func TestNodeRemovesPIDFileOnShutdown(t *testing.T) {
	s := testSettings(t)
	config.Save(s.Home, nodeConfig(s))
	ctx, cancel := context.WithCancel(context.Background())
	app, _ := newApp(s, "")
	done := make(chan error)
	go func() { done <- app.Run(ctx, []string{"serve"}) }()
	pidPath := filepath.Join(s.Home, "ouro.pid")
	if _, ok := daemon.WaitRunning(pidPath, 2*time.Second); !ok {
		t.Fatal("node did not start")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatal("PID file should be removed")
	}
}

func TestInteractiveChatReportsErrorsAndContinues(t *testing.T) {
	s := testSettings(t)
	cfg := nodeConfig(s)
	cfg.APIKey = "sk-revoked" // node starts, but every LLM call fails
	startNode(t, s, cfg)
	out, err := run(t, s, "hi\n", "chat", "alpha")
	if err != nil || !strings.Contains(out, "✗") || !strings.Contains(out, "401") {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestOneShotChatReturnsError(t *testing.T) {
	s := testSettings(t)
	cfg := nodeConfig(s)
	cfg.APIKey = "sk-revoked"
	startNode(t, s, cfg)
	if _, err := run(t, s, "", "chat", "alpha", "hi"); err == nil {
		t.Fatal("want error")
	}
}

func TestChatErrors(t *testing.T) {
	s := testSettings(t)
	if _, err := run(t, s, "", "chat"); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("no args: %v", err)
	}
	if _, err := run(t, s, "", "chat", "nobody", "hi"); err == nil || !strings.Contains(err.Error(), "no node") {
		t.Fatalf("unknown node: %v", err)
	}
}

func TestNotRunning(t *testing.T) {
	s := testSettings(t)
	for _, cmd := range []string{"status", "stop"} {
		if out, err := run(t, s, "", cmd); err != nil || !strings.Contains(out, "not running") {
			t.Fatalf("%s: %q, %v", cmd, out, err)
		}
	}
	if out, _ := run(t, s, "", "list"); !strings.Contains(out, "no ouroboros nodes") {
		t.Fatalf("list: %q", out)
	}
}

func TestStartRejectsUnknownFlag(t *testing.T) {
	if _, err := run(t, testSettings(t), "", "start", "--force"); err == nil {
		t.Fatal("want error")
	}
}
