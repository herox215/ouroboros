// Package cli is the ouro command line: a kernel and the list of plugins that
// make up the standard ouro. It has no logic of its own. What a node does
// lives in the packages the plugins use; a plugin only wires them up and
// presents them, each in a package of its own below internal/plugins:
//
//	base       the console and the dispatcher that runs commands
//	discovery  finding nodes
//	tailscale  finding the nodes on the tailnet
//	peers      the network of nodes and the tools to reach it
//	openai     the LLM
//	assistant  the agents
//	node       start, stop, status, serve and the setup dialog
//	talk       list, ask, chat
//	tui        the terminal UI
//	update     update
//
// To add a plugin: write a package with a func that returns a plugin.Plugin,
// against the package ouroboros/plugin and nothing else, and add it to
// Plugins. Package plugin says what a plugin can need, provide and
// contribute.
//
// What a plugin cannot get is the frame of ouro: the home of the node with
// its key and its contacts, and the network that holds them. The frame is
// built in Plugins and handed to the plugins that are part of it as plain
// values. It never becomes a service: the kernel refuses a plugin that
// declares one of its types, see seal.
package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"ouroboros/internal/a2a"
	"ouroboros/internal/config"
	"ouroboros/internal/contacts"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/kernel"
	"ouroboros/internal/node"
	"ouroboros/internal/openai"
	"ouroboros/internal/peers"
	"ouroboros/internal/plugins/assistant"
	"ouroboros/internal/plugins/base"
	"ouroboros/internal/plugins/discovery"
	nodeplugin "ouroboros/internal/plugins/node"
	openaiplugin "ouroboros/internal/plugins/openai"
	peersplugin "ouroboros/internal/plugins/peers"
	"ouroboros/internal/plugins/tailscale"
	"ouroboros/internal/plugins/talk"
	"ouroboros/internal/plugins/tui"
	"ouroboros/internal/plugins/update"
	"ouroboros/plugin"
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
		dir, err := userConfigDir()
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

// userConfigDir finds where configurations are kept; tests replace it to get
// in the way.
var userConfigDir = os.UserConfigDir

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
type Terminal = plugin.Terminal

// Plugins is the standard ouro for the node described by s, talking to its
// user through t. The order is only the order of the commands in the usage:
// the kernel starts each plugin after those it depends on.
func Plugins(s Settings, t Terminal) []plugin.Plugin {
	// The frame, see the package comment.
	dir := home.Dir(s.Home)
	network := &peers.Network{Contacts: dir.Contacts(), Client: node.Local{Home: dir}.Client}
	return []plugin.Plugin{
		base.Plugin(dir, t),
		discovery.Plugin(s.DiscoPort),
		tailscale.Plugin(),
		peersplugin.Plugin(dir, network),
		openaiplugin.Plugin(),
		assistant.Plugin(dir),
		nodeplugin.Plugin(dir, nodeplugin.Options{
			HTTPPort: s.HTTPPort, DiscoPort: s.DiscoPort, Version: Version, Log: t.Out,
			BaseURL: s.BaseURL, APIKey: s.APIKey,
		}),
		talk.Plugin(),
		tui.Plugin(dir, network),
		update.Plugin(dir),
	}
}

// seal keeps the frame of ouro out of the kernel: who the node is, how it
// signs, whom it trusts, and what holds any of it. A plugin that needs or
// provides one of these types, or a type through which one can be had, stops
// ouro from starting.
func seal(k *kernel.Kernel) {
	kernel.Seal[identity.Identity](k) // the key of the node, which signs
	kernel.Seal[config.Config](k)     // holds the key
	kernel.Seal[home.Dir](k)          // opens the configuration, the identity and the contacts
	kernel.Seal[contacts.Store](k)    // whom the node trusts
	kernel.Seal[contacts.Data](k)
	kernel.Seal[a2a.Trust](k)
	kernel.Seal[a2a.Client](k) // signs as the node
	kernel.Seal[a2a.Server](k) // checks signatures and trust, runs the built-in methods
	kernel.Seal[node.Local](k)
	kernel.Seal[node.Server](k)
	kernel.Seal[peers.Network](k) // holds the contacts and the client
}

// App runs ouro commands. It is a kernel with plugins and has no logic of its
// own: a command belongs to the plugin that contributes it.
type App struct {
	kernel *kernel.Kernel
}

// New returns the standard ouro for the node described by s, talking to its
// user through t.
func New(s Settings, t Terminal) (*App, error) {
	return NewWith(Plugins(s, t)...)
}

// NewWith returns an ouro made of plugins. It fails, with nothing started, if
// they cannot all be started: see kernel.Kernel.Start.
func NewWith(plugins ...plugin.Plugin) (*App, error) {
	k := kernel.New()
	seal(k)
	if err := k.Start(plugins...); err != nil {
		return nil, err
	}
	return &App{kernel: k}, nil
}

// Run executes the command given by args (without the program name).
func (a *App) Run(ctx context.Context, args []string) error {
	d, ok := Service[*base.Dispatcher](a)
	if !ok {
		return errors.New("no plugin runs commands: the base plugin is missing")
	}
	return d.Run(ctx, args)
}

// Service returns the service of type T of the plugins of a, for whoever
// runs the app.
func Service[T any](a *App) (T, bool) { return kernel.Service[T](a.kernel) }
