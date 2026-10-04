// Package cli is the ouro command line: a kernel and the plugins that make up
// the standard ouro. What a node does lives in the packages the plugins use;
// a plugin only wires them up and presents them.
//
// Where things are:
//
//	cli.go       App, and the list of plugins in plugins()
//	base.go      plugin base: home directory, console, local node, dispatcher
//	discover.go  plugins discovery and tailscale: finding nodes
//	peers.go     plugin peers: the network of nodes and the tools to reach it
//	chat.go      plugins openai and assistant: the LLM and the agents
//	node.go      plugin node: start, stop, status, serve
//	talk.go      plugin talk: list, ask, chat
//	tui.go       plugin tui: the terminal UI
//	update.go    plugin update
//	setup.go     the setup dialog
//	console.go   line-based input and output
//
// To add a plugin: write a func that returns a kernel.Plugin, like the ones
// in these files, and add it to plugins(). In its Start it gets the services
// it needs with use, offers its own with kernel.Provide, and contributes with
// kernel.Add:
//
//	command              a command of ouro (this package)
//	agent.Tool           something the agents can do
//	discovery.PeerSource hosts to look for nodes on
//	a2a.Method           a JSON-RPC method for connected peers
package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"ouroboros/internal/config"
	"ouroboros/internal/kernel"
	"ouroboros/internal/openai"
)

// Version is the ouro release version.
const Version = "0.1.0"

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

// Terminal is how ouro talks to its user.
type Terminal struct {
	In  io.Reader
	Out io.Writer
	// ReadSecret reads a secret without echoing it. If nil, a plain line is read.
	ReadSecret func(prompt string) (string, error)
	// Interactive reports whether In is a terminal. Without it, ouro with no
	// command prints the usage instead of opening the terminal UI.
	Interactive bool
}

// command is one ouro command. Plugins contribute commands with kernel.Add;
// the usage text is generated from them.
type command struct {
	name string // what the user types: ouro NAME
	args string // its arguments, as shown in the usage
	help string // one line for the usage
	// run gets the arguments after the name. It prints to the console and
	// returns an error to fail with exit code 1.
	run func(ctx context.Context, args []string) error
}

// plugins is the standard ouro. The order is only the order of the commands
// in the usage: the kernel starts each plugin after those it depends on.
func plugins(s Settings, t Terminal) []kernel.Plugin {
	return []kernel.Plugin{
		basePlugin(s, t),
		discoveryPlugin(),
		tailscalePlugin(),
		peersPlugin(),
		openaiPlugin(),
		assistantPlugin(),
		nodePlugin(),
		talkPlugin(),
		tuiPlugin(),
		updatePlugin(),
	}
}

// App runs ouro commands. It is a kernel with plugins and has no logic of its
// own: a command belongs to the plugin that contributes it.
type App struct {
	kernel *kernel.Kernel
	ctx    *kernel.Context
}

// New returns the standard ouro for the node described by s, talking to its
// user through t.
func New(s Settings, t Terminal) (*App, error) {
	return newAppWith(plugins(s, t)...)
}

func newAppWith(plugins ...kernel.Plugin) (*App, error) {
	k := kernel.New()
	if err := k.Start(plugins...); err != nil {
		return nil, err
	}
	return &App{kernel: k, ctx: k.Context()}, nil
}

// Run executes the command given by args (without the program name).
func (a *App) Run(ctx context.Context, args []string) error {
	d, err := kernel.Use[*dispatcher](a.ctx)
	if err != nil {
		return err
	}
	return d.run(ctx, args)
}

// use gets the service of type T for a plugin that needs several: the first
// failure is kept in err, to be returned by Start after the last use.
func use[T any](ctx *kernel.Context, err *error) T {
	service, e := kernel.Use[T](ctx)
	if *err == nil {
		*err = e
	}
	return service
}

// withHint tells how to get past a node that was not set up yet.
func withHint(err error) error {
	if errors.Is(err, config.ErrIncomplete) {
		return fmt.Errorf("%w, run: ouro start", err)
	}
	return err
}
