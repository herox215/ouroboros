package cli

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/discovery"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/kernel"
	"ouroboros/internal/openai/openaitest"
	"ouroboros/internal/peers"
	"ouroboros/internal/tui"
	"ouroboros/internal/update"
	"ouroboros/internal/update/updatetest"
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
	return config.Config{BaseURL: s.BaseURL, APIKey: openaitest.Key, Model: "gpt-4o", Name: "alpha"}
}

// nodeID is the ID of the node of s, which has it once it was started.
func nodeID(t *testing.T, s Settings) string {
	t.Helper()
	return mustLoad(t, s).ID()
}

// testPeers are the hosts the node with the given home probes directly.
var testPeers = map[string][]string{}

func newApp(s Settings, input string) (*App, *bytes.Buffer) {
	return newAppOn(s, Terminal{In: strings.NewReader(input)})
}

// newAppOn returns the standard ouro with one plugin swapped: instead of the
// tailnet, the hosts in testPeers are asked, so that tests stay on this host.
func newAppOn(s Settings, term Terminal) (*App, *bytes.Buffer) {
	var out bytes.Buffer
	term.Out = &out
	if term.In == nil {
		term.In = strings.NewReader("")
	}
	list := slices.DeleteFunc(plugins(s, term), func(p kernel.Plugin) bool { return p.Name == "tailscale" })
	list = append(list, kernel.Plugin{Name: "testpeers", Start: func(ctx *kernel.Context) error {
		kernel.Add(ctx, discovery.PeerSource(func(context.Context) []string { return testPeers[s.Home] }))
		return nil
	}})
	app, err := newAppWith(list...)
	if err != nil {
		panic(err)
	}
	service[*discovery.Scanner](app).Wait = 300 * time.Millisecond
	return app, &out
}

// service returns the service of type T of app.
func service[T any](app *App) T {
	v, err := kernel.Use[T](app.ctx)
	if err != nil {
		panic(err)
	}
	return v
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
	for _, args := range [][]string{{"help"}, {"--help"}} {
		out, err := run(t, testSettings(t), "", args...)
		if err != nil || !strings.Contains(out, "ouro start") {
			t.Fatalf("%v: got %q, %v", args, out, err)
		}
	}
}

// Without a terminal, ouro with no command prints the usage instead of TUI.
func TestNoArgsWithoutTerminalShowsUsage(t *testing.T) {
	out, err := run(t, testSettings(t), "")
	if err != nil || !strings.Contains(out, "ouro start") {
		t.Fatalf("got %q, %v", out, err)
	}
}

// Without a message "ouro chat" is the terminal UI; only pipes get the line-by-line chat.
func TestChatWithTerminalOpensTUI(t *testing.T) {
	s := testSettings(t)
	config.Save(s.Home, nodeConfig(s))
	app, out := newAppOn(s, Terminal{Interactive: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the UI has no terminal here: it must give up instead of reading lines
	if err := app.Run(ctx, []string{"chat"}); err == nil || strings.Contains(out.String(), "chatting with your agent") {
		t.Fatalf("got %q, %v", out.String(), err)
	}
}

func TestTuiWithoutTerminal(t *testing.T) {
	if _, err := run(t, testSettings(t), "", "tui"); err == nil {
		t.Fatal("want error")
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
	cfg, err := service[*wizard](app).run(context.Background(), config.Config{})
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
	app, _ := newApp(s, "\n\n\n")
	cfg, err := service[*wizard](app).run(context.Background(), config.Config{})
	if err != nil || cfg.BaseURL != s.BaseURL || cfg.Model != "gpt-4o-mini" || cfg.Name != hostname() {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestSetupKeepsExistingValues(t *testing.T) {
	s := testSettings(t)
	s.BaseURL = "http://other.invalid" // the saved endpoint wins over the default
	want := nodeConfig(testSettings(t))
	want.PrivateKey = identity.Generate().Private() // and with it the ID of the node
	app, _ := newApp(s, "\n\n\n")
	cfg, err := service[*wizard](app).run(context.Background(), want)
	if err != nil || !reflect.DeepEqual(cfg, want) {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestSetupWithoutAPIKey(t *testing.T) {
	s := testSettings(t)
	url := openaitest.NewServerWithKey(t, "") // e.g. a local Ollama
	app, _ := newApp(s, url+"/\n\n1\nlocal\n")
	cfg, err := service[*wizard](app).run(context.Background(), config.Config{})
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
	cfg, err := service[*wizard](app).run(context.Background(), old)
	if err != nil || cfg.BaseURL != other || cfg.APIKey != "sk-other" {
		t.Fatalf("got %+v, %v", cfg, err)
	}
	if strings.Contains(out.String(), "rejected") {
		t.Fatal("old key must not be tried against the new endpoint")
	}
}

func TestSetupUsesReadSecret(t *testing.T) {
	var prompted string
	app, _ := newAppOn(testSettings(t), Terminal{In: strings.NewReader("\n1\nx\n"), ReadSecret: func(prompt string) (string, error) {
		prompted = prompt
		return openaitest.Key, nil
	}})
	if _, err := service[*wizard](app).run(context.Background(), config.Config{}); err != nil || prompted == "" {
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
		if _, err := service[*wizard](app).run(context.Background(), config.Config{}); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestSetupNetworkError(t *testing.T) {
	s := testSettings(t)
	app, _ := newApp(s, "http://127.0.0.1:1\n\n")
	if _, err := service[*wizard](app).run(context.Background(), config.Config{}); err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
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

	id := nodeID(t, s)
	out, err := run(t, s, "", "list")
	if err != nil || len(id) != 12 || !strings.Contains(out, id) || !strings.Contains(out, "alpha") || !strings.Contains(out, "gpt-4o") {
		t.Fatalf("list: %q, %v", out, err)
	}

	out, err = run(t, s, "", "ask", id[:4], "hello", "world")
	if err != nil || out != "[gpt-4o] turns=1 last=hello world\n" {
		t.Fatalf("ask by ID prefix: %q, %v", out, err)
	}

	out, err = run(t, s, "one\n\ntwo\n", "ask", "alpha")
	if err != nil || !strings.Contains(out, "turns=1 last=one") || !strings.Contains(out, "turns=3 last=two") {
		t.Fatalf("interactive ask should keep history: %q, %v", out, err)
	}

	out, err = run(t, s, "", "ask", fmt.Sprintf("127.0.0.1:%d", port), "direct")
	if err != nil || !strings.Contains(out, "last=direct") {
		t.Fatalf("ask by address: %q, %v", out, err)
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

func TestInteractiveAskReportsErrorsAndContinues(t *testing.T) {
	s := testSettings(t)
	cfg := nodeConfig(s)
	cfg.APIKey = "sk-revoked" // node starts, but every LLM call fails
	startNode(t, s, cfg)
	out, err := run(t, s, "hi\n", "ask", "alpha")
	if err != nil || !strings.Contains(out, "✗") || !strings.Contains(out, "401") {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestOneShotAskReturnsError(t *testing.T) {
	s := testSettings(t)
	cfg := nodeConfig(s)
	cfg.APIKey = "sk-revoked"
	startNode(t, s, cfg)
	if _, err := run(t, s, "", "ask", "alpha", "hi"); err == nil {
		t.Fatal("want error")
	}
}

func TestAskErrors(t *testing.T) {
	s := testSettings(t)
	if _, err := run(t, s, "", "ask"); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("no args: %v", err)
	}
	if _, err := run(t, s, "", "ask", "nobody", "hi"); err == nil || !strings.Contains(err.Error(), "no node") {
		t.Fatalf("unknown node: %v", err)
	}
}

// The assistant runs in the calling process: it needs a configuration but no running node.
func TestChatWithAssistant(t *testing.T) {
	s := testSettings(t)
	if _, err := run(t, s, "", "chat", "hi"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured: %v", err)
	}
	config.Save(s.Home, nodeConfig(s))

	out, err := run(t, s, "", "chat", "hello", "world")
	if err != nil || out != "[gpt-4o] turns=1 last=hello world\n" {
		t.Fatalf("one-shot: %q, %v", out, err)
	}
	out, err = run(t, s, "one\n\ntwo\n", "chat")
	if err != nil || !strings.Contains(out, "turns=1 last=one") || !strings.Contains(out, "turns=3 last=two") {
		t.Fatalf("interactive chat should keep history: %q, %v", out, err)
	}

	cfg := nodeConfig(s)
	cfg.APIKey = "sk-revoked"
	config.Save(s.Home, cfg)
	if _, err := run(t, s, "", "chat", "hi"); err == nil {
		t.Fatal("a failing LLM should fail the one-shot chat")
	}
	if out, err := run(t, s, "hi\n", "chat"); err != nil || !strings.Contains(out, "✗") || !strings.Contains(out, "401") {
		t.Fatalf("interactive chat should report the error and continue: %q, %v", out, err)
	}
}

// twoNodes starts the node "beta" and returns the settings of "alpha", which
// finds beta as a VPN peer.
func twoNodes(t *testing.T) (alpha, beta Settings) {
	t.Helper()
	beta = testSettings(t)
	cfg := nodeConfig(beta)
	cfg.Name, cfg.Model = "beta", "gpt-4o-mini"
	startNode(t, beta, cfg)

	alpha = testSettings(t)
	testPeers[alpha.Home] = []string{fmt.Sprintf("127.0.0.1:%d", beta.DiscoPort)}
	if err := config.Save(alpha.Home, nodeConfig(alpha)); err != nil {
		t.Fatal(err)
	}
	return alpha, beta
}

func TestAssistantAsksPeers(t *testing.T) {
	alpha, beta := twoNodes(t)
	startNode(t, alpha, mustLoad(t, alpha)) // beta's acceptance has to reach alpha
	b, _ := newApp(beta, "")

	// "@" makes the fake LLM list the peers.
	out, err := run(t, alpha, "", "chat", "@")
	if err != nil || !strings.Contains(out, "last=beta (id "+nodeID(t, beta)+", model gpt-4o-mini, not connected)") || strings.Contains(out, "alpha") {
		t.Fatalf("list_peers: %q, %v", out, err)
	}

	// Beta does not know alpha yet: the first message becomes a connection request.
	out, err = run(t, alpha, "@beta hello\n", "chat")
	if err != nil || !strings.Contains(out, "last=beta is not connected") {
		t.Fatalf("first contact: %q, %v", out, err)
	}
	if out, _ = run(t, alpha, "", "chat", "@"); !strings.Contains(out, "connection requested") {
		t.Fatalf("the request should be remembered: %q", out)
	}

	if strings.Contains(out, "instance named") {
		t.Fatalf("a peer that is not connected should not get a word in: %q", out)
	}
	requests, _ := home.Dir(beta.Home).Contacts().Snapshot()
	if len(requests.Incoming) != 1 || requests.Incoming[0].Name != "alpha" {
		t.Fatalf("beta's requests: %+v", requests)
	}
	if err := service[*peers.Network](b).Accept(context.Background(), requests.Incoming[0].PublicKey); err != nil {
		t.Fatal(err)
	}

	// Connected: beta's agent answers, and remembers the conversation.
	out, err = run(t, alpha, "@beta one\n@beta two\n", "chat")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[gpt-4o] turns=3 last=[gpt-4o-mini] turns=1 last=one",
		"[gpt-4o] turns=7 last=[gpt-4o-mini] turns=3 last=two", // user, call, result, answer, user, call, result
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// What the agents say to each other is not printed, only the assistant's answers.
	if strings.Count(out, "last=one") != 1 || strings.Contains(out, "beta:") {
		t.Errorf("the talk between the agents should stay out of the chat:\n%s", out)
	}
	if out, _ = run(t, alpha, "", "chat", "@"); !strings.Contains(out, "model gpt-4o-mini, connected): Ouroboros instance named beta\n") {
		t.Fatalf("beta should be listed as connected, with its card: %q", out)
	}

	// A one-shot chat prints the answer only.
	out, err = run(t, alpha, "", "chat", "@beta", "ping")
	if err != nil || out != "[gpt-4o] turns=3 last=[gpt-4o-mini] turns=1 last=ping\n" {
		t.Fatalf("one-shot: %q, %v", out, err)
	}
}

func TestAssistantCannotReachPeer(t *testing.T) {
	alpha, _ := twoNodes(t)
	startNode(t, alpha, mustLoad(t, alpha))
	for peer, want := range map[string]string{"nobody": `no node "nobody" found`, "alpha": "is this node itself"} {
		out, err := run(t, alpha, "@"+peer+" hi\n", "chat")
		if err != nil || !strings.Contains(out, "last=error: ") || !strings.Contains(out, want) {
			t.Errorf("%s: %q, %v", peer, out, err)
		}
	}
}

func TestTUIBackendAsk(t *testing.T) {
	alpha, _ := twoNodes(t)
	app, _ := newApp(alpha, "")
	var steps []tui.Step
	reply, err := service[*ui](app).Ask(context.Background(), "ctx", "@beta hello", func(s tui.Step) { steps = append(steps, s) })
	if err != nil || !strings.Contains(reply, "not connected") {
		t.Fatalf("got %q, %v", reply, err)
	}
	if len(steps) != 2 || steps[0] != (tui.Step{Peer: "beta"}) || steps[1] != (tui.Step{Peer: "beta", Done: true}) {
		t.Fatalf("steps %+v", steps)
	}
	// Looking up the peers is not worth a line in the chat.
	steps = nil
	if _, err := service[*ui](app).Ask(context.Background(), "ctx", "@", func(s tui.Step) { steps = append(steps, s) }); err != nil || len(steps) != 0 {
		t.Fatalf("steps %+v, %v", steps, err)
	}
}

func mustLoad(t *testing.T, s Settings) config.Config {
	t.Helper()
	cfg, err := config.Load(s.Home)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
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

func TestUpdateInstallsNewestVersion(t *testing.T) {
	s := testSettings(t)
	repo, rev := updatetest.NewRepo(t, "v1")
	exe := filepath.Join(t.TempDir(), "ouro")
	app, out := newApp(s, "")
	updater := service[*update.Updater](app)
	updater.Repo, updater.Exe, updater.Revision = repo, exe, "old"

	if err := app.Run(context.Background(), []string{"update"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "v1" || !strings.Contains(out.String(), "updated to "+rev[:7]) {
		t.Fatalf("binary %q, output %q", got, out.String())
	}

	out.Reset()
	updater.Revision = rev
	if err := app.Run(context.Background(), []string{"update"}); err != nil || !strings.Contains(out.String(), "already up to date") {
		t.Fatalf("second update: %v, output %q", err, out.String())
	}
}

// A node outside the local network is found through a VPN peer.
func TestListFindsNodesOnPeers(t *testing.T) {
	remote := testSettings(t)
	startNode(t, remote, nodeConfig(remote))

	s := testSettings(t) // another discovery port: the remote node does not hear its probes
	if out, _ := run(t, s, "", "list"); !strings.Contains(out, "no ouroboros nodes") {
		t.Fatalf("without peers nothing should be found: %q", out)
	}
	testPeers[s.Home] = []string{fmt.Sprintf("127.0.0.1:%d", remote.DiscoPort)}
	if out, err := run(t, s, "", "list"); err != nil || !strings.Contains(out, "alpha") {
		t.Fatalf("the VPN peer should be found: %q, %v", out, err)
	}
}

func TestCard(t *testing.T) {
	s := testSettings(t)
	port := startNode(t, s, nodeConfig(s))
	card, err := a2a.FetchCard(context.Background(), fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil || card.Description != "Ouroboros instance named alpha" || card.Version != Version {
		t.Fatalf("card: %+v, %v", card, err)
	}
}

// publicKey is the key the node of s signs with.
func publicKey(t *testing.T, s Settings) string {
	t.Helper()
	id, err := home.Dir(s.Home).Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id.Public()
}

// trust makes the node of s trust the node of peer.
func trust(t *testing.T, s, peer Settings, name string) {
	t.Helper()
	home.Dir(s.Home).Contacts().AddTrusted(publicKey(t, peer), name, "")
}

// A peer's question may make the agent use the tools its owner allows for
// peers, and no others.
func TestPeerAgentUsesAllowedTools(t *testing.T) {
	// gamma keeps the default: its agent has no tools for peers.
	gamma := testSettings(t)
	cfg := nodeConfig(gamma)
	cfg.Name, cfg.Model = "gamma", "o3-mini"
	startNode(t, gamma, cfg)

	// beta lets peers use its tools.
	beta := testSettings(t)
	cfg = nodeConfig(beta)
	cfg.Name, cfg.Model = "beta", "gpt-4o-mini"
	cfg.Tools = &config.Tools{A2A: []string{"list_peers", "ask_peer"}}
	startNode(t, beta, cfg)

	alpha := testSettings(t)
	startNode(t, alpha, nodeConfig(alpha))

	disco := func(s Settings) string { return fmt.Sprintf("127.0.0.1:%d", s.DiscoPort) }
	testPeers[alpha.Home] = []string{disco(beta)}
	testPeers[beta.Home] = []string{disco(alpha), disco(gamma)}
	testPeers[gamma.Home] = []string{disco(beta)}
	trust(t, beta, alpha, "alpha")
	trust(t, gamma, beta, "beta")
	trust(t, beta, gamma, "gamma")

	// alpha asks beta, whose agent asks gamma on the way.
	out, err := run(t, alpha, "", "ask", "beta", "@gamma hello")
	if err != nil || out != "[gpt-4o-mini] turns=3 last=[o3-mini] turns=1 last=hello\n" {
		t.Fatalf("beta should ask gamma: %q, %v", out, err)
	}

	// The peer that asks is not asked in turn, and not offered either.
	out, err = run(t, alpha, "", "ask", "beta", "@alpha hello")
	if err != nil || !strings.Contains(out, "last=error: alpha is the peer you are answering") {
		t.Fatalf("beta should not ask alpha back: %q, %v", out, err)
	}
	out, err = run(t, alpha, "", "ask", "beta", "@")
	if err != nil || !strings.Contains(out, "gamma (id "+nodeID(t, gamma)) || strings.Contains(out, "alpha") {
		t.Fatalf("beta should list gamma only: %q, %v", out, err)
	}

	// gamma allows peers no tools: the same message is just a message.
	out, err = run(t, beta, "", "ask", "gamma", "@beta hello")
	if err != nil || out != "[o3-mini] turns=1 last=@beta hello\n" {
		t.Fatalf("gamma should not use tools: %q, %v", out, err)
	}
}

// The owner can take tools away from the own agent, too.
func TestOwnerToolsCanBeRestricted(t *testing.T) {
	s := testSettings(t)
	cfg := nodeConfig(s)
	cfg.Tools = &config.Tools{H2A: []string{}}
	config.Save(s.Home, cfg)
	out, err := run(t, s, "", "chat", "@beta hello")
	if err != nil || out != "[gpt-4o] turns=1 last=@beta hello\n" {
		t.Fatalf("without tools the message is just a message: %q, %v", out, err)
	}

	cfg.Tools = &config.Tools{H2A: []string{"ask_peers"}}
	config.Save(s.Home, cfg)
	if _, err := run(t, s, "", "chat", "hi"); err == nil || !strings.Contains(err.Error(), "tools.h2a") {
		t.Fatalf("a typo should be reported: %v", err)
	}
	cfg.Tools = &config.Tools{A2A: []string{"ask_peers"}}
	config.Save(s.Home, cfg)
	if _, err := run(t, s, "", "serve"); err == nil || !strings.Contains(err.Error(), "tools.a2a") {
		t.Fatalf("a node with a typo should not start: %v", err)
	}
}

// A plugin adds to ouro without a change anywhere else: here a command and a
// tool the owner can allow.
func TestPluginExtendsOuro(t *testing.T) {
	s := testSettings(t)
	cfg := nodeConfig(s)
	cfg.Tools = &config.Tools{H2A: []string{"shout"}}
	config.Save(s.Home, cfg)

	var out bytes.Buffer
	term := Terminal{In: strings.NewReader(""), Out: &out}
	shout := kernel.Plugin{Name: "shout", Start: func(ctx *kernel.Context) error {
		term, err := kernel.Use[*console](ctx)
		if err != nil {
			return err
		}
		kernel.Add(ctx, command{"shout", "TEXT", "print TEXT in capitals", func(_ context.Context, args []string) error {
			fmt.Fprintln(term.out, strings.ToUpper(strings.Join(args, " ")))
			return nil
		}})
		kernel.Add(ctx, agent.NewTool("shout", "Returns the text in capitals.",
			[]agent.Param{{Name: "text"}},
			func(_ context.Context, args struct {
				Text string `json:"text"`
			}) (string, error) {
				return strings.ToUpper(args.Text), nil
			}))
		return nil
	}}

	// Listed first, although it needs the console of the base plugin.
	app, err := newAppWith(append([]kernel.Plugin{shout}, plugins(s, term)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), []string{"shout", "hello", "ouro"}); err != nil || out.String() != "HELLO OURO\n" {
		t.Fatalf("command: %q, %v", out.String(), err)
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"help"}); err != nil || !strings.Contains(out.String(), "ouro shout TEXT") {
		t.Fatalf("usage: %q, %v", out.String(), err)
	}
	// The owner allowed the tool "shout": the assistant is built with it.
	if err := service[*assistant.Assistant](app).Ready(); err != nil {
		t.Fatalf("the tool of the plugin should be known: %v", err)
	}

	// Without the plugin the same configuration names a tool that is not there.
	plain, _ := newApp(s, "")
	if err := service[*assistant.Assistant](plain).Ready(); err == nil || !strings.Contains(err.Error(), `unknown tool "shout"`) {
		t.Fatalf("got %v", err)
	}
}

// A plugin that needs something nobody provides stops ouro from starting.
func TestPluginWithMissingService(t *testing.T) {
	s := testSettings(t)
	lonely := kernel.Plugin{Name: "lonely", Start: func(ctx *kernel.Context) error {
		_, err := kernel.Use[*testing.T](ctx)
		return err
	}}
	_, err := newAppWith(append(plugins(s, Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}}), lonely)...)
	if err == nil || !strings.Contains(err.Error(), "plugin lonely needs *testing.T") {
		t.Fatalf("got %v", err)
	}
}
