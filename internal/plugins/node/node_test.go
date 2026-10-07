package node_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/home"
	"ouroboros/internal/openai/openaitest"
	"ouroboros/plugin"
)

func TestMain(m *testing.M) { clitest.Main(m) }

func TestServeRequiresConfig(t *testing.T) {
	if _, err := clitest.Run(t, clitest.Settings(t), "", "serve"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("got %v", err)
	}
}

func TestServeFallsBackToFreePort(t *testing.T) {
	busy, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	s := clitest.Settings(t)
	s.HTTPPort = busy.Addr().(*net.TCPAddr).Port
	if port := clitest.StartNode(t, s, clitest.NodeConfig(s)); port == s.HTTPPort {
		t.Fatal("expected a different port")
	}
}

func TestNodeRemovesPIDFileOnShutdown(t *testing.T) {
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	ctx, cancel := context.WithCancel(context.Background())
	app, _ := clitest.NewApp(t, s, "")
	done := make(chan error)
	go func() { done <- app.Run(ctx, []string{"serve"}) }()
	pidPath := filepath.Join(s.Home, "ouro.pid")
	if _, ok := daemon.WaitRunning(pidPath, clitest.Patience); !ok {
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

// A PID file left behind is not a node: the process that got its PID since
// is neither shown as the node nor stopped, and a node starts over the file.
func TestStalePIDFileIsNotANode(t *testing.T) {
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	alive := clitest.Bystander(t, s)

	if out, err := clitest.Run(t, s, "", "status"); err != nil || !strings.Contains(out, "not running") {
		t.Fatalf("status: %q, %v", out, err)
	}
	if out, err := clitest.Run(t, s, "", "stop"); err != nil || !strings.Contains(out, "not running") {
		t.Fatalf("stop: %q, %v", out, err)
	}
	if !alive() {
		t.Fatal("stop signalled an unrelated process")
	}

	port := clitest.StartNode(t, s, clitest.NodeConfig(s))
	if out, _ := clitest.Run(t, s, "", "status"); !strings.Contains(out, fmt.Sprintf("port=%d", port)) {
		t.Fatalf("status of the node that took the file over: %q", out)
	}
}

func TestNotRunning(t *testing.T) {
	s := clitest.Settings(t)
	for _, cmd := range []string{"status", "stop"} {
		if out, err := clitest.Run(t, s, "", cmd); err != nil || !strings.Contains(out, "not running") {
			t.Fatalf("%s: %q, %v", cmd, out, err)
		}
	}
	if out, _ := clitest.Run(t, s, "", "list"); !strings.Contains(out, "no ouroboros nodes") {
		t.Fatalf("list: %q", out)
	}
}

func TestStartRejectsUnknownFlag(t *testing.T) {
	if _, err := clitest.Run(t, clitest.Settings(t), "", "start", "--force"); err == nil {
		t.Fatal("want error")
	}
}

func TestCard(t *testing.T) {
	s := clitest.Settings(t)
	port := clitest.StartNode(t, s, clitest.NodeConfig(s))
	card, err := a2a.FetchCard(context.Background(), fmt.Sprintf("https://127.0.0.1:%d/", port), clitest.PublicKey(t, s))
	if err != nil || card.Description != "Ouroboros instance named alpha" || card.Version != cli.Version {
		t.Fatalf("card: %+v, %v", card, err)
	}
}

// "ouro start" sets the node up, starts it in the background and says where
// it listens; "ouro stop" stops it again.
func TestStartAndStop(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	t.Cleanup(func() { clitest.Run(t, s, "", "stop") })

	out, err := clitest.Run(t, s, "\n"+openaitest.Key+"\n2\nmynode\n", "start")
	id := clitest.NodeID(t, s)
	if err != nil || !strings.Contains(out, "API endpoint") || !strings.Contains(out, "● ouroboros "+id+" (mynode) online on :") || !strings.Contains(out, "→ gpt-4o-mini") {
		t.Fatalf("start: %q, %v", out, err)
	}
	if out, err := clitest.Run(t, s, "", "status"); err != nil || !strings.Contains(out, "● running  id="+id+"  name=mynode") {
		t.Fatalf("status: %q, %v", out, err)
	}
	if out, _ := clitest.Run(t, s, "", "start"); !strings.Contains(out, "already running") {
		t.Fatalf("second start: %q", out)
	}
	if out, err := clitest.Run(t, s, "", "stop"); err != nil || !strings.Contains(out, "○ stopped") {
		t.Fatalf("stop: %q, %v", out, err)
	}

	// A node that is set up is asked again only with --setup.
	out, err = clitest.Run(t, s, "\n\nrenamed\n", "start", "--setup")
	if err != nil || !strings.Contains(out, "(renamed) online") {
		t.Fatalf("start --setup: %q, %v", out, err)
	}
}

func TestStartThatFails(t *testing.T) {
	// The setup ends with the input.
	s := clitest.Settings(t)
	if out, err := clitest.Run(t, s, "", "start"); err == nil || strings.Contains(out, "online") {
		t.Fatalf("setup without answers: %q, %v", out, err)
	}
	// A key that is none: the node has no identity to start with.
	cfg := clitest.NodeConfig(s)
	cfg.PrivateKey = "nonsense"
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	if out, err := clitest.Run(t, s, "", "start"); err == nil || strings.Contains(out, "online") {
		t.Fatalf("a node without a key: %q, %v", out, err)
	}
}

// A node that cannot be stopped is reported, not taken for stopped.
func TestStopThatFails(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Ghost(t, s)
	if out, err := clitest.Run(t, s, "", "stop"); err == nil || strings.Contains(out, "stopped") {
		t.Fatalf("got %q, %v", out, err)
	}
}

// The node serves the methods the plugins added, and answers peers with the
// agent the owner configured: a tool for peers that does not exist stops it.
func TestServe(t *testing.T) {
	s := clitest.Settings(t)
	port := clitest.StartNode(t, s, clitest.NodeConfig(s))
	id, err := home.Dir(s.Home).Identity()
	if err != nil {
		t.Fatal(err)
	}
	client := &a2a.Client{Identity: id}
	url := fmt.Sprintf("https://127.0.0.1:%d/", port)
	var failed *a2a.Error
	if _, err := client.Call(context.Background(), url, id.Public(), "nobody/added", nil); !errors.As(err, &failed) || failed.Code != a2a.CodeMethodNotFound {
		t.Fatalf("a method no plugin added: %v", err)
	}
	if _, err := client.Open(context.Background(), url, id.Public(), "nobody/added"); !errors.As(err, &failed) || failed.Code != a2a.CodeMethodNotFound {
		t.Fatalf("a channel no plugin added: %v", err)
	}

	typo := clitest.Settings(t)
	cfg := clitest.NodeConfig(typo)
	cfg.Tools = &config.Tools{A2A: config.Named("ask_peers")}
	config.Save(typo.Home, cfg)
	if _, err := clitest.Run(t, typo, "", "serve"); err == nil || !strings.Contains(err.Error(), "tools.a2a") {
		t.Fatalf("a node with a typo should not start: %v", err)
	}
}

// A conversation belongs to the peer whose key signed its messages: a peer
// that uses the context ID of another one starts a conversation of its own,
// and reads nothing of what the other said.
func TestPeersDoNotShareAConversation(t *testing.T) {
	beta := clitest.Settings(t)
	cfg := clitest.NodeConfig(beta)
	cfg.Name = "beta"
	clitest.StartNode(t, beta, cfg)

	ctx := context.Background()
	ask := map[string]func(text string) string{}
	for _, name := range []string{"alpha", "gamma"} {
		s := clitest.Settings(t)
		cfg := clitest.NodeConfig(s)
		cfg.Name = name
		if err := config.Save(s.Home, cfg); err != nil {
			t.Fatal(err)
		}
		clitest.Finds(s, beta)
		clitest.Trust(t, beta, s, name)
		clitest.Trust(t, s, beta, "beta")
		app, _ := clitest.NewApp(t, s, "")
		network := clitest.Network(t, s, app)
		ask[name] = func(text string) string {
			t.Helper()
			node, err := network.Resolve(ctx, "beta")
			if err != nil {
				t.Fatal(err)
			}
			reply, err := network.Send(ctx, node, "the same context", text)
			if err != nil {
				t.Fatal(err)
			}
			return reply
		}
	}

	if got := ask["alpha"]("a secret"); !strings.Contains(got, "turns=1 last=a secret") {
		t.Fatalf("alpha: %q", got)
	}
	if got := ask["gamma"]("what was said?"); !strings.Contains(got, "turns=1 last=what was said?") {
		t.Fatalf("gamma continued the conversation of alpha: %q", got)
	}
	if got := ask["alpha"]("again"); !strings.Contains(got, "turns=3 last=again") {
		t.Fatalf("alpha lost its conversation: %q", got)
	}
}

// again is a plugin with a tool that asks for ouro to start anew from exe.
func again(exe string) plugin.Plugin {
	return plugin.New("again", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.NewTool("again", "starts ouro anew", nil, func(ctx context.Context, _ struct{}) (string, error) {
			if !plugin.Restart(ctx, exe) {
				return "", errors.New("nobody starts ouro anew")
			}
			return "starting anew", nil
		}))
		return nil
	})
}

// A node that a tool asks for a new build of ends with plugin.Restarting once
// the answer is given, and hands its sessions over in a file the new node
// reads once.
func TestServeHandsOverTheSessions(t *testing.T) {
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	n := clitest.Serve(t, s, again("/new/ouro"))
	app, _ := clitest.NewApp(t, s, "")
	sessions, _ := cli.Service[plugin.Sessions](app)
	if reply, err := plugin.Await(context.Background(), sessions, "c1", "!again"); err != nil || !strings.Contains(reply, "starting anew") {
		t.Fatalf("got %q, %v", reply, err)
	}
	err := n.Wait()
	r, ok := errors.AsType[*plugin.Restarting](err)
	if !ok || r.Exe != "/new/ouro" {
		t.Fatalf("got %v", err)
	}
	var file string
	for _, kv := range r.Env {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "OURO_") {
			t.Setenv(k, v)
		}
		if k == "OURO_SESSIONS" {
			file = v
		}
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 || file != home.Dir(s.Home).Sessions() {
		t.Fatalf("the sessions handed over: %v, %v", info, err)
	}
	next := clitest.Serve(t, s)
	if list, err := sessions.List(context.Background()); err != nil || len(list) != 1 || list[0].ID != "c1" {
		t.Fatalf("the new node has %+v, %v", list, err)
	}
	if reply, err := plugin.Await(context.Background(), sessions, "c1", "and now?"); err != nil || !strings.Contains(reply, "turns=5 last=and now?") {
		t.Fatalf("the agent of the new node should remember: %q, %v", reply, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("the file should be read once")
	}
	if _, ok := os.LookupEnv("OURO_SESSIONS"); ok {
		t.Fatal("a process the node starts should not see the file")
	}
	next.Stop()

	// Sessions that cannot be written are lost, and the node is replaced all
	// the same; those that cannot be read are lost, and the node starts.
	os.Mkdir(file, 0o700)
	os.WriteFile(filepath.Join(file, "in the way"), nil, 0o600)
	n = clitest.Serve(t, s, again("/new/ouro"))
	plugin.Await(context.Background(), sessions, "c2", "!again")
	r, ok = errors.AsType[*plugin.Restarting](n.Wait())
	if !ok || slices.ContainsFunc(r.Env, func(kv string) bool { return strings.HasPrefix(kv, "OURO_SESSIONS=") }) {
		t.Fatalf("got %+v", r)
	}
	for _, kv := range r.Env {
		if k, v, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "OURO_") {
			t.Setenv(k, v)
		}
	}
	for _, content := range []string{"", "{"} {
		os.RemoveAll(file)
		if content != "" {
			os.WriteFile(file, []byte(content), 0o600)
		}
		t.Setenv("OURO_SESSIONS", file)
		if err := clitest.Serve(t, s).Stop(); err != nil {
			t.Fatal(err)
		}
	}
	logged, _ := os.ReadFile(home.Dir(s.Home).Service().LogFile)
	for _, want := range []string{"the sessions are not handed over: ", "the sessions handed over are lost: open ", "the sessions handed over are lost: unexpected end of JSON input"} {
		if !strings.Contains(string(logged), want) {
			t.Fatalf("the log lacks %q:\n%s", want, logged)
		}
	}
}
