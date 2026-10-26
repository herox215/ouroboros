// Package clitest runs ouro for the tests of its plugins: whole nodes on this
// host, with a fake LLM, that find each other without a network.
package clitest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/cli"
	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/discovery"
	"ouroboros/internal/home"
	"ouroboros/internal/modelsdev"
	"ouroboros/internal/node"
	"ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
	"ouroboros/internal/peers"
	sessionplugin "ouroboros/internal/plugins/session"
	"ouroboros/internal/sandbox"
	"ouroboros/internal/session"
	"ouroboros/plugin"
)

// Patience is how long a test waits for a node to come up. On a busy machine
// that takes many times as long as usual; the wait ends when the node is up.
const Patience = time.Minute

// Main runs the tests of a package whose test binary is also ouro: called
// with a command, as node.Local starts the real binary, it runs that command
// for the node the environment describes. Call it from TestMain.
func Main(m *testing.M) {
	os.Setenv(modelsdev.Env, "off") // no test reaches models.dev
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		os.Exit(ouro(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// ouro is the main function of the real binary.
func ouro(args []string) int {
	s, err := cli.SettingsFromEnv()
	if err == nil {
		var app *cli.App
		if app, err = cli.New(s, cli.Terminal{In: os.Stdin, Out: os.Stdout}); err == nil {
			err = app.Run(context.Background(), args)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "✗", err)
		return 1
	}
	return 0
}

// Env makes the environment describe the node of s, for a test binary that
// is started as ouro (see Main).
func Env(t *testing.T, s cli.Settings) {
	t.Helper()
	t.Setenv("OURO_HOME", s.Home)
	t.Setenv("OURO_PORT", "0")
	t.Setenv("OURO_DISCO_PORT", fmt.Sprint(s.DiscoPort))
	t.Setenv("OPENAI_BASE_URL", s.BaseURL)
	t.Setenv("OPENAI_API_KEY", s.APIKey)
	t.Setenv("DEEPSEEK_API_KEY", s.DeepSeekKey)
}

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

// Settings are the settings of a node of its own: with an empty home, free
// ports and a fake LLM.
func Settings(t *testing.T) cli.Settings {
	t.Helper()
	t.Setenv(modelsdev.Env, "off") // no test reaches models.dev
	return cli.Settings{
		Home:      t.TempDir(),
		HTTPPort:  0,
		DiscoPort: freeUDPPort(t),
		BaseURL:   openaitest.NewServer(t),
	}
}

// NodeConfig is a complete configuration for a node using the fake API of s,
// as a node from before there were accesses has it: the node makes an access
// of the endpoint and the key when it reads it.
func NodeConfig(s cli.Settings) config.Config {
	return config.Config{BaseURL: s.BaseURL, APIKey: openaitest.Key, Model: "gpt-4o", Name: "alpha"}
}

// Reached returns the endpoint and the key of the access the node of s
// answers through, as its credentials keep them.
func Reached(t *testing.T, s cli.Settings) (endpoint, key string) {
	t.Helper()
	dir := home.Dir(s.Home)
	cfg, err := dir.Config()
	if err != nil {
		t.Fatal(err)
	}
	access, err := dir.Credentials().Get(cfg.Access)
	if err != nil {
		t.Fatalf("the access %q of the node: %v", cfg.Access, err)
	}
	endpoint, key, err = openai.Saved(access.Credentials)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint, key
}

// MustLoad returns the configuration of the node of s.
func MustLoad(t *testing.T, s cli.Settings) config.Config {
	t.Helper()
	cfg, err := config.Load(s.Home)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// NodeID is the ID of the node of s, which has it once it was started.
func NodeID(t *testing.T, s cli.Settings) string {
	t.Helper()
	return MustLoad(t, s).ID()
}

// PublicKey is the key the node of s signs with.
func PublicKey(t *testing.T, s cli.Settings) string {
	t.Helper()
	id, err := home.Dir(s.Home).Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id.Public()
}

// Port is the A2A port of the node of s, which runs.
func Port(t *testing.T, s cli.Settings) int {
	t.Helper()
	p, ok := home.Dir(s.Home).Service().Running()
	if !ok {
		t.Fatal("the node does not run")
	}
	return p.Port
}

// Network is the network of the node of s as the frame of ouro has it: with
// the contacts and the decisions on connection requests, which no plugin
// gets. It finds nodes the way app does.
func Network(t *testing.T, s cli.Settings, app *cli.App) *peers.Network {
	t.Helper()
	scanner, ok := cli.Service[*discovery.Scanner](app)
	if !ok {
		t.Fatal("no scanner")
	}
	dir := home.Dir(s.Home)
	return &peers.Network{Finder: scanner, Contacts: dir.Contacts(), Client: node.Local{Home: dir}.Client}
}

// Trust makes the node of s trust the node of peer.
func Trust(t *testing.T, s, peer cli.Settings, name string) {
	t.Helper()
	if err := home.Dir(s.Home).Contacts().AddTrusted(PublicKey(t, peer), name, ""); err != nil {
		t.Fatal(err)
	}
}

// probed are the hosts the node with the given home probes directly.
var probed = struct {
	sync.Mutex
	hosts map[string][]string
}{hosts: map[string][]string{}}

// Finds makes the node of s find the nodes of others, as VPN peers.
func Finds(s cli.Settings, others ...cli.Settings) {
	hosts := make([]string, len(others))
	for i, o := range others {
		hosts[i] = fmt.Sprintf("127.0.0.1:%d", o.DiscoPort)
	}
	probed.Lock()
	defer probed.Unlock()
	probed.hosts[s.Home] = hosts
}

// Plugins is the standard ouro with one plugin swapped: instead of the
// tailnet, the nodes given to Finds are asked, so that tests stay on this
// host. extra are plugins added to it.
func Plugins(s cli.Settings, term cli.Terminal, extra ...plugin.Plugin) []plugin.Plugin {
	list := slices.DeleteFunc(cli.Plugins(s, term), func(p plugin.Plugin) bool { return p.Name == "tailscale" })
	list = append(list, plugin.New("testpeers", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.PeerSource(func(context.Context) []string {
			probed.Lock()
			defer probed.Unlock()
			return probed.hosts[s.Home]
		}))
		return nil
	}))
	return append(list, extra...)
}

// NewApp returns ouro for the node of s, see Plugins, with a user who types
// input, and what it prints.
func NewApp(t *testing.T, s cli.Settings, input string, extra ...plugin.Plugin) (*cli.App, *bytes.Buffer) {
	t.Helper()
	return NewAppOn(t, s, cli.Terminal{In: strings.NewReader(input)}, extra...)
}

// NewAppOn is NewApp for a user at term.
func NewAppOn(t *testing.T, s cli.Settings, term cli.Terminal, extra ...plugin.Plugin) (*cli.App, *bytes.Buffer) {
	t.Helper()
	return newApp(t, s, term, func(list []plugin.Plugin) []plugin.Plugin { return list }, extra...)
}

// newApp is NewAppOn with the plugins as change makes them.
func newApp(t *testing.T, s cli.Settings, term cli.Terminal, change func([]plugin.Plugin) []plugin.Plugin, extra ...plugin.Plugin) (*cli.App, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	term.Out = &out
	if term.In == nil {
		term.In = strings.NewReader("")
	}
	app, err := cli.NewWith(change(Plugins(s, term, extra...))...)
	if err != nil {
		t.Fatal(err)
	}
	scanner, _ := cli.Service[*discovery.Scanner](app)
	scanner.Wait = 300 * time.Millisecond
	return app, &out
}

// Run runs the command args of ouro for the node of s and returns what it
// printed. Its sessions are in its own process, see InProcess: "ouro chat"
// needs no node.
func Run(t *testing.T, s cli.Settings, input string, args ...string) (string, error) {
	t.Helper()
	app, out := newApp(t, s, cli.Terminal{In: strings.NewReader(input)}, InProcess(s))
	err := app.Run(context.Background(), args)
	return out.String(), err
}

// InProcess changes a list of plugins for the node of s to have the sessions
// of the owner in the process of its front ends, as the node has them and
// kept where it keeps them, instead of those of the node on its socket: the
// same Service, without the socket. The front ends answer without a node
// then.
func InProcess(s cli.Settings) func(list []plugin.Plugin) []plugin.Plugin {
	return func(list []plugin.Plugin) []plugin.Plugin { return inProcess(home.Dir(s.Home), list) }
}

func inProcess(dir home.Dir, list []plugin.Plugin) []plugin.Plugin {
	type needs struct{ Assistant *assistant.Assistant }
	type services struct {
		Service  *session.Service
		Sessions plugin.Sessions
	}
	sessions := plugin.NewService("sessions", func(_ *plugin.Context, n needs) (services, error) {
		s := sessionplugin.In(dir, n.Assistant)
		return services{s, s}, nil
	})
	return slices.Collect(func(yield func(plugin.Plugin) bool) {
		for _, p := range list {
			if p.Name == "sessions" {
				p = sessions
			}
			if !yield(p) {
				return
			}
		}
	})
}

// StartNode runs "ouro serve" for the node of s, configured by cfg, in the
// background and returns its port. The node is stopped when the test ends.
func StartNode(t *testing.T, s cli.Settings, cfg config.Config, extra ...plugin.Plugin) int {
	t.Helper()
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	n := Serve(t, s, extra...)
	t.Cleanup(func() {
		if err := n.Stop(); err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return Port(t, s)
}

// Node is "ouro serve" that runs in the process of the test.
type Node struct {
	App  *cli.App // that serves
	stop context.CancelFunc
	done chan struct{}
	err  error
}

// Wait returns once the node stopped, with what serve ended with.
func (n *Node) Wait() error {
	<-n.done
	return n.err
}

// Stop stops the node, and returns once it stopped, with what serve ended
// with.
func (n *Node) Stop() error {
	n.stop()
	return n.Wait()
}

// Serve runs "ouro serve" for the node of s as it is configured, in the
// process of the test, and returns once the node runs. It is stopped when the
// test ends at the latest.
func Serve(t *testing.T, s cli.Settings, extra ...plugin.Plugin) *Node {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	app, _ := NewApp(t, s, "", extra...)
	n := &Node{App: app, stop: cancel, done: make(chan struct{})}
	go func() {
		defer close(n.done)
		n.err = app.Run(ctx, []string{"serve"})
	}()
	t.Cleanup(func() { n.Stop() })
	running := make(chan bool, 1)
	go func() {
		_, ok := daemon.WaitRunning(filepath.Join(s.Home, "ouro.pid"), Patience)
		running <- ok
	}()
	select {
	case ok := <-running:
		if !ok {
			t.Fatal("node did not start")
		}
	case <-n.done:
		// The sandbox of the tool bash allows no socket of the file system,
		// and a node does not run without its own.
		if sandbox.Nested() && errors.Is(n.err, syscall.EPERM) {
			t.Skipf("a node cannot run in the sandbox of bash, which allows no socket of the file system: %v", n.err)
		}
		t.Fatalf("node did not start: %v", n.err)
	}
	return n
}

// TwoNodes starts the node "beta" and returns the settings of "alpha", which
// is configured and finds beta as a VPN peer.
func TwoNodes(t *testing.T) (alpha, beta cli.Settings) {
	t.Helper()
	beta = Settings(t)
	cfg := NodeConfig(beta)
	cfg.Name, cfg.Model = "beta", "gpt-4o-mini"
	StartNode(t, beta, cfg)

	alpha = Settings(t)
	Finds(alpha, beta)
	if err := config.Save(alpha.Home, NodeConfig(alpha)); err != nil {
		t.Fatal(err)
	}
	return alpha, beta
}

// bystander starts a process that is no node and returns its PID and what
// reports whether it is still running.
func bystander(t *testing.T) (pid int, alive func() bool) {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-exited
	})
	return cmd.Process.Pid, func() bool {
		select {
		case <-exited:
			return false
		case <-time.After(200 * time.Millisecond): // more than a signal needs
			return true
		}
	}
}

// Bystander starts a process that is no node, records it in the PID file of
// s as a node that crashed would have left it, and returns a function that
// reports whether the process is still running.
func Bystander(t *testing.T, s cli.Settings) (alive func() bool) {
	t.Helper()
	pid, alive := bystander(t)
	stale := fmt.Appendf(nil, "%d 41337\n", pid)
	if err := os.WriteFile(filepath.Join(s.Home, "ouro.pid"), stale, 0o600); err != nil {
		t.Fatal(err)
	}
	return alive
}

// Ghost makes the node of s look as if it ran, on a port nobody listens on,
// without a process that could be stopped: stopping it fails.
func Ghost(t *testing.T, s cli.Settings) {
	t.Helper()
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(s.Home, "ouro.pid"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	// The lock is what says that a node runs; the PID is of a process that
	// is gone.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(f, "%d 1\n", gone.Process.Pid); err != nil {
		t.Fatal(err)
	}
}

// Unreadable returns settings whose home cannot be read or made: it is below
// a file.
func Unreadable(t *testing.T) cli.Settings {
	t.Helper()
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := Settings(t)
	s.Home = filepath.Join(file, "home")
	return s
}
