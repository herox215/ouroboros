// Package cli implements the ouro command line interface.
package cli

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/discovery"
	"ouroboros/internal/openai"
)

// Version is the ouro release version.
const Version = "0.1.0"

const usage = `ouro – tiny A2A agent node for any OpenAI-compatible LLM API

  ouro start [--setup]    start the node (asks for endpoint, key + model on first run)
  ouro stop               stop the node
  ouro status             show the local node
  ouro list               find ouroboros nodes on the network
  ouro chat ID ["msg"]    chat with a node (interactive without msg)
  ouro serve              run the node in the foreground
`

// Settings come from the environment and are not persisted.
type Settings struct {
	Home      string // config, PID and log directory
	HTTPPort  int    // A2A port; falls back to a free port if taken
	DiscoPort int    // UDP discovery port
	BaseURL   string // default LLM endpoint offered during setup
	APIKey    string // key for BaseURL offered during setup
}

// SettingsFromEnv reads OURO_HOME, OURO_PORT, OURO_DISCO_PORT, OPENAI_BASE_URL and OPENAI_API_KEY.
func SettingsFromEnv() (Settings, error) {
	home := os.Getenv("OURO_HOME")
	if home == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return Settings{}, err
		}
		home = filepath.Join(dir, "ouroboros")
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

	discoverWait time.Duration
}

// New returns an App reading user input from in and writing to out.
func New(s Settings, in io.Reader, out io.Writer) *App {
	return &App{settings: s, in: bufio.NewReader(in), out: out, discoverWait: 1500 * time.Millisecond}
}

// Run executes the command given by args (without the program name).
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
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
	case "chat":
		return a.chat(ctx, rest)
	case "serve":
		return a.serve(ctx)
	case "help", "-h", "--help":
		fmt.Fprint(a.out, usage)
		return nil
	}
	return fmt.Errorf("unknown command %q (see: ouro help)", cmd)
}

func (a *App) pidPath() string { return filepath.Join(a.settings.Home, "ouro.pid") }
func (a *App) logPath() string { return filepath.Join(a.settings.Home, "ouro.log") }

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
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	os.Remove(a.pidPath()) // drop a stale PID file
	if err := daemon.Spawn(exe, a.logPath(), "serve"); err != nil {
		return err
	}
	p, ok := daemon.WaitRunning(a.pidPath(), 5*time.Second)
	if !ok {
		return fmt.Errorf("node did not start, see %s", a.logPath())
	}
	fmt.Fprintf(a.out, "● ouroboros %s (%s) online on :%d → %s\n", cfg.ID, cfg.Name, p.Port, cfg.Model)
	return nil
}

func (a *App) stop() error {
	p, ok := daemon.Running(a.pidPath())
	if !ok {
		fmt.Fprintln(a.out, "○ not running")
		return nil
	}
	if err := daemon.Stop(p.PID, 5*time.Second); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "○ stopped")
	return nil
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

func (a *App) list() error {
	nodes, err := discovery.Discover(a.settings.DiscoPort, a.discoverWait)
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

func (a *App) chat(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New(`usage: ouro chat ID ["message"]`)
	}
	addr, err := a.resolve(args[0])
	if err != nil {
		return err
	}
	url, contextID := "http://"+addr+"/", a2a.NewID()

	if len(args) > 1 {
		reply, err := a2a.Send(ctx, url, contextID, strings.Join(args[1:], " "))
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
		reply, err := a2a.Send(ctx, url, contextID, line)
		if err != nil {
			reply = "✗ " + err.Error()
		}
		fmt.Fprintf(a.out, "%s\n\n", reply)
	}
}

// resolve turns a node ID, ID prefix, name or host:port into an address.
func (a *App) resolve(target string) (string, error) {
	if strings.Contains(target, ":") {
		return target, nil
	}
	nodes, err := discovery.Discover(a.settings.DiscoPort, a.discoverWait)
	if err != nil {
		return "", err
	}
	n, err := match(nodes, target)
	return n.Addr(), err
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

// ask prints prompt and reads an answer, returning def for an empty one.
func (a *App) ask(prompt, def string) (string, error) {
	fmt.Fprint(a.out, prompt)
	line, err := a.readLine()
	return cmp.Or(line, def), err
}
