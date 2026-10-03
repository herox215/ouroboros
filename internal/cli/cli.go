// Package cli implements the ouro command line interface.
package cli

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/contacts"
	"ouroboros/internal/daemon"
	"ouroboros/internal/discovery"
	"ouroboros/internal/identity"
	"ouroboros/internal/openai"
	"ouroboros/internal/update"
)

// Version is the ouro release version.
const Version = "0.1.0"

const usage = `ouro – tiny A2A agent node for any OpenAI-compatible LLM API

  ouro                    chat with your agent in the terminal UI (default)
  ouro start [--setup]    start the node (asks for endpoint, key + model on first run)
  ouro stop               stop the node
  ouro status             show the local node
  ouro list               find ouroboros nodes on the network
  ouro peers [add|rm HOST] hosts to look for nodes on beyond the local network
  ouro chat ["msg"]       ask your agent, which can ask the other nodes (terminal UI without msg)
  ouro ask ID ["msg"]     write to a node directly (interactive without msg)
  ouro tui                control the node and chat in a terminal UI
  ouro serve              run the node in the foreground
  ouro update             build and install the newest version
`

// Settings come from the environment and are not persisted.
type Settings struct {
	Home      string // config, PID and log directory
	Data      string // what the agents of this node know, see soulPath
	HTTPPort  int    // A2A port; falls back to a free port if taken
	DiscoPort int    // UDP discovery port
	BaseURL   string // default LLM endpoint offered during setup
	APIKey    string // key for BaseURL offered during setup
}

// SettingsFromEnv reads OURO_HOME, OURO_DATA, OURO_PORT, OURO_DISCO_PORT, OPENAI_BASE_URL and OPENAI_API_KEY.
func SettingsFromEnv() (Settings, error) {
	home := os.Getenv("OURO_HOME")
	if home == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return Settings{}, err
		}
		home = filepath.Join(dir, "ouroboros")
	}
	data := os.Getenv("OURO_DATA")
	if data == "" {
		dir, err := os.UserHomeDir()
		if err != nil {
			return Settings{}, err
		}
		data = filepath.Join(dir, ".ouro")
	}
	httpPort, err := envInt("OURO_PORT", 41337)
	if err != nil {
		return Settings{}, err
	}
	discoPort, err := envInt("OURO_DISCO_PORT", 41338)
	if err != nil {
		return Settings{}, err
	}
	return Settings{
		Home:      home,
		Data:      data,
		HTTPPort:  httpPort,
		DiscoPort: discoPort,
		BaseURL:   cmp.Or(os.Getenv("OPENAI_BASE_URL"), openai.DefaultBaseURL),
		APIKey:    os.Getenv("OPENAI_API_KEY"),
	}, nil
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a number", key, v)
	}
	return n, nil
}

// App runs ouro commands.
type App struct {
	settings Settings
	in       *bufio.Reader
	out      io.Writer

	// ReadSecret reads a secret without echoing it. If nil, a plain line is read.
	ReadSecret func(prompt string) (string, error)

	// Interactive reports whether stdin is a terminal. Without it, ouro with no
	// command prints the usage instead of opening the terminal UI.
	Interactive bool

	discoverWait time.Duration
	updater      update.Updater
	// vpnPeers finds hosts that discovery cannot reach by multicast or broadcast.
	vpnPeers func(context.Context) []string

	botMu  sync.Mutex
	bot    *agent.Agent // see assistant
	botKey [4]string    // the configuration bot was built for
}

// New returns an App reading user input from in and writing to out.
func New(s Settings, in io.Reader, out io.Writer) *App {
	return &App{
		settings:     s,
		in:           bufio.NewReader(in),
		out:          out,
		discoverWait: 1500 * time.Millisecond,
		vpnPeers:     discovery.TailscalePeers,
		updater:      update.Updater{Repo: update.Repo, Dir: filepath.Join(s.Home, "update"), Revision: update.Revision()},
	}
}

// Run executes the command given by args (without the program name).
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		if a.Interactive {
			return a.tui(ctx)
		}
		fmt.Fprint(a.out, usage)
		return nil
	}
	if err := os.MkdirAll(a.settings.Home, 0o700); err != nil {
		return err
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "start":
		return a.start(ctx, rest)
	case "stop":
		return a.stop()
	case "status":
		return a.status()
	case "list":
		return a.list()
	case "peers":
		return a.peers(rest)
	case "chat":
		return a.chat(ctx, rest)
	case "ask":
		return a.ask(ctx, rest)
	case "serve":
		return a.serve(ctx)
	case "update":
		return a.update(ctx)
	case "tui":
		if !a.Interactive {
			return errors.New("tui needs a terminal")
		}
		return a.tui(ctx)
	case "help", "-h", "--help":
		fmt.Fprint(a.out, usage)
		return nil
	}
	return fmt.Errorf("unknown command %q (see: ouro help)", cmd)
}

func (a *App) pidPath() string      { return filepath.Join(a.settings.Home, "ouro.pid") }
func (a *App) logPath() string      { return filepath.Join(a.settings.Home, "ouro.log") }
func (a *App) contactsPath() string { return filepath.Join(a.settings.Home, "contacts.json") }

// contacts opens this node's contact list.
func (a *App) contacts() *contacts.Store { return contacts.Open(a.contactsPath()) }

// identity returns the node's signing identity, generating and persisting one
// on first use.
func (a *App) identity(cfg *config.Config) (*identity.Identity, error) {
	if cfg.PrivateKey == "" {
		id := identity.Generate()
		cfg.PrivateKey = id.Private()
		if err := config.Save(a.settings.Home, *cfg); err != nil {
			return nil, err
		}
		return id, nil
	}
	return identity.FromPrivate(cfg.PrivateKey)
}

// localPort is the running node's A2A port, or 0 when it is not running.
func (a *App) localPort() int {
	if p, ok := daemon.Running(a.pidPath()); ok {
		return p.Port
	}
	return 0
}

// client returns a signed client for the local node.
func (a *App) client(cfg config.Config) (*a2a.Client, error) {
	id, err := a.identity(&cfg)
	if err != nil {
		return nil, err
	}
	return &a2a.Client{Identity: id, Name: cfg.Name, Model: cfg.Model, Port: a.localPort()}, nil
}

func (a *App) start(ctx context.Context, args []string) error {
	setup := false
	for _, arg := range args {
		if arg != "--setup" {
			return fmt.Errorf("unknown flag %q", arg)
		}
		setup = true
	}
	if p, ok := daemon.Running(a.pidPath()); ok {
		fmt.Fprintf(a.out, "● already running (pid %d, port %d)\n", p.PID, p.Port)
		return nil
	}
	cfg, err := config.Load(a.settings.Home)
	if err != nil {
		return err
	}
	if setup || !cfg.Complete() {
		if cfg, err = a.setup(ctx, cfg); err != nil {
			return err
		}
	}
	p, err := a.launch()
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "● ouroboros %s (%s) online on :%d → %s\n", cfg.ID, cfg.Name, p.Port, cfg.Model)
	return nil
}

// launch runs the configured node in the background and waits until it is up.
func (a *App) launch() (daemon.Process, error) {
	exe, err := executable()
	if err != nil {
		return daemon.Process{}, err
	}
	return a.launchExe(exe)
}

func (a *App) launchExe(exe string) (daemon.Process, error) {
	os.Remove(a.pidPath()) // drop a stale PID file
	if err := daemon.Spawn(exe, a.logPath(), "serve"); err != nil {
		return daemon.Process{}, err
	}
	p, ok := daemon.WaitRunning(a.pidPath(), 5*time.Second)
	if !ok {
		return daemon.Process{}, fmt.Errorf("node did not start, see %s", a.logPath())
	}
	return p, nil
}

// executable is the path of the running binary with symlinks resolved.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func (a *App) update(ctx context.Context) error {
	rev, updated, err := a.selfUpdate(ctx, a.out, false)
	if err != nil {
		return err
	}
	if updated {
		fmt.Fprintf(a.out, "● updated to %s\n", update.Short(rev))
	} else {
		fmt.Fprintf(a.out, "● already up to date (%s)\n", update.Short(rev))
	}
	return nil
}

// selfUpdate installs the newest version over the running binary and restarts
// the node if it is running. Build output goes to out.
func (a *App) selfUpdate(ctx context.Context, out io.Writer, batch bool) (rev string, updated bool, err error) {
	u := a.updater
	u.Out, u.Batch = out, batch
	if u.Exe == "" {
		// Resolved before the update: afterwards the path of a replaced binary is gone.
		if u.Exe, err = executable(); err != nil {
			return "", false, err
		}
	}
	if rev, updated, err = u.Run(ctx); err != nil || !updated {
		return rev, updated, err
	}
	if running, err := a.stopNode(); err != nil || !running {
		return rev, true, err
	}
	_, err = a.launchExe(u.Exe)
	return rev, true, err
}

func (a *App) stop() error {
	stopped, err := a.stopNode()
	if err != nil {
		return err
	}
	if stopped {
		fmt.Fprintln(a.out, "○ stopped")
	} else {
		fmt.Fprintln(a.out, "○ not running")
	}
	return nil
}

// stopNode stops the background node and reports whether one was running.
func (a *App) stopNode() (bool, error) {
	p, ok := daemon.Running(a.pidPath())
	if !ok {
		return false, nil
	}
	return true, daemon.Stop(p.PID, 5*time.Second)
}

func (a *App) status() error {
	p, ok := daemon.Running(a.pidPath())
	if !ok {
		fmt.Fprintln(a.out, "○ not running")
		return nil
	}
	cfg, err := config.Load(a.settings.Home)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "● running  id=%s  name=%s  port=%d  model=%s\n", cfg.ID, cfg.Name, p.Port, cfg.Model)
	return nil
}

// discover finds the nodes on the local network, on the configured peers and
// on the VPN peers of this host.
func (a *App) discover() ([]discovery.Node, error) {
	cfg, err := config.Load(a.settings.Home)
	if err != nil {
		return nil, err
	}
	peers := cfg.Peers
	if a.vpnPeers != nil {
		peers = append(peers, a.vpnPeers(context.Background())...)
	}
	return discovery.Discover(a.settings.DiscoPort, a.discoverWait, peers...)
}

// peers lists or changes the hosts that are probed directly.
func (a *App) peers(args []string) error {
	cfg, err := config.Load(a.settings.Home)
	if err != nil {
		return err
	}
	switch {
	case len(args) == 0:
		if len(cfg.Peers) == 0 {
			fmt.Fprintln(a.out, "no peers (add one with: ouro peers add HOST)")
		}
		for _, p := range cfg.Peers {
			fmt.Fprintln(a.out, p)
		}
		return nil
	case len(args) == 2 && args[0] == "add":
		if !slices.Contains(cfg.Peers, args[1]) {
			cfg.Peers = append(cfg.Peers, args[1])
		}
	case len(args) == 2 && args[0] == "rm":
		i := slices.Index(cfg.Peers, args[1])
		if i < 0 {
			return fmt.Errorf("no peer %q", args[1])
		}
		cfg.Peers = slices.Delete(cfg.Peers, i, i+1)
	default:
		return errors.New("usage: ouro peers [add|rm HOST]")
	}
	return config.Save(a.settings.Home, cfg)
}

func (a *App) list() error {
	nodes, err := a.discover()
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		fmt.Fprintln(a.out, "no ouroboros nodes found")
		return nil
	}
	tw := tabwriter.NewWriter(a.out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tMODEL\tADDRESS")
	for _, n := range nodes {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", n.ID, n.Name, n.Model, n.Addr())
	}
	return tw.Flush()
}

// ask writes to a node directly, without the assistant in between.
func (a *App) ask(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(`usage: ouro ask ID ["message"]`)
	}
	node, err := a.resolve(args[0])
	if err != nil {
		return err
	}
	addr := node.Addr()
	cfg, err := config.Load(a.settings.Home)
	if err != nil {
		return err
	}
	client, err := a.client(cfg)
	if err != nil {
		return err
	}
	url, contextID := "http://"+addr+"/", a2a.NewID()

	if len(args) > 1 {
		reply, err := client.Send(ctx, url, contextID, strings.Join(args[1:], " "))
		if a2a.IsPending(err) {
			a.requestConnection(node)
			return fmt.Errorf("connection request sent to %s, waiting for approval", cmp.Or(node.Name, addr))
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(a.out, reply)
		return nil
	}

	fmt.Fprintf(a.out, "chatting with %s (Ctrl-D to quit)\n", addr)
	for {
		fmt.Fprint(a.out, "› ")
		line, err := a.readLine()
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(a.out)
			return nil
		}
		if err != nil {
			return err
		}
		if line == "" {
			continue
		}
		reply, err := client.Send(ctx, url, contextID, line)
		if a2a.IsPending(err) {
			a.requestConnection(node)
			reply = "connection request sent — waiting for approval"
		} else if err != nil {
			reply = "✗ " + err.Error()
		}
		fmt.Fprintf(a.out, "%s\n\n", reply)
	}
}

// requestConnection remembers an outgoing connection request so that the
// peer's acceptance can be recognised later.
func (a *App) requestConnection(n discovery.Node) {
	if n.PublicKey != "" {
		a.contacts().AddOutgoing(n.PublicKey, n.Name, n.Model, n.Addr())
	}
}

// resolve turns a node ID, ID prefix, name or host:port into a node.
func (a *App) resolve(target string) (discovery.Node, error) {
	if strings.Contains(target, ":") {
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			return discovery.Node{}, err
		}
		p, err := strconv.Atoi(port)
		if err != nil {
			return discovery.Node{}, fmt.Errorf("invalid port in %q", target)
		}
		return discovery.Node{Host: host, Port: p}, nil
	}
	nodes, err := a.discover()
	if err != nil {
		return discovery.Node{}, err
	}
	return match(nodes, target)
}

func match(nodes []discovery.Node, target string) (discovery.Node, error) {
	var hits []discovery.Node
	for _, n := range nodes {
		if n.ID == target {
			return n, nil
		}
		if n.Name == target || strings.HasPrefix(n.ID, target) {
			hits = append(hits, n)
		}
	}
	switch len(hits) {
	case 0:
		return discovery.Node{}, fmt.Errorf("no node %q found (see: ouro list)", target)
	case 1:
		return hits[0], nil
	}
	ids := make([]string, len(hits))
	for i, n := range hits {
		ids[i] = n.ID
	}
	return discovery.Node{}, fmt.Errorf("%q is ambiguous: %s", target, strings.Join(ids, ", "))
}

// readLine reads one trimmed line. It returns io.EOF only when no input is left.
func (a *App) readLine() (string, error) {
	line, err := a.in.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// prompt prints prompt and reads an answer, returning def for an empty one.
func (a *App) prompt(prompt, def string) (string, error) {
	fmt.Fprint(a.out, prompt)
	line, err := a.readLine()
	return cmp.Or(line, def), err
}
