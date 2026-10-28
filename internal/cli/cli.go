// Package cli is the ouro command line: a kernel and the list of plugins that
// make up the standard ouro. It has no logic of its own. What a node does
// lives in the packages the plugins use; a plugin only wires them up and
// presents them, each in a package of its own below internal/plugins:
//
//	base       the console and the dispatcher that runs commands
//	discovery  finding nodes
//	tailscale  finding the nodes on the tailnet
//	peers      the network of nodes and the tools to reach it
//	files      the tools for files
//	history    the archive of the conversations, and the tools to search it
//	memory     explicit memories, their sources, and project-scoped search
//	openai     the provider for OpenAI-compatible APIs
//	deepseek   the provider for DeepSeek, by its API key alone
//	opencode-go  the provider for OpenCode Go, by its API key alone
//	chatgpt    the provider that signs in with ChatGPT
//	assistant  the agents
//	sessions   the conversations of the owner, which the node holds
//	node       start, stop, status, serve and the setup dialog
//	talk       list, ask, chat
//	tui        the terminal UI
//	gui        the graphical UI
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
//
// The terminal UI is no part of the frame, but it is where the owner starts
// and stops the node and decides on connection requests. For that it is
// handed a plugin.Owner in Plugins, which does these things and nothing else.
// Leave tui.Plugin out and ouro runs without a front end; put another plugin
// in its place and give it the Owner to replace it.
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
	"ouroboros/internal/chatgpt"
	"ouroboros/internal/claudecode"
	"ouroboros/internal/config"
	"ouroboros/internal/contacts"
	"ouroboros/internal/credentials"
	"ouroboros/internal/guard"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/kernel"
	"ouroboros/internal/llm"
	"ouroboros/internal/node"
	"ouroboros/internal/openai"
	"ouroboros/internal/peers"
	abilitiesplugin "ouroboros/internal/plugins/abilities"
	"ouroboros/internal/plugins/assistant"
	"ouroboros/internal/plugins/base"
	browserplugin "ouroboros/internal/plugins/browser"
	chatgptplugin "ouroboros/internal/plugins/chatgpt"
	claudecodeplugin "ouroboros/internal/plugins/claudecode"
	claudesessionsplugin "ouroboros/internal/plugins/claudesessions"
	computerplugin "ouroboros/internal/plugins/computer"
	deepseekplugin "ouroboros/internal/plugins/deepseek"
	desktopplugin "ouroboros/internal/plugins/desktop"
	"ouroboros/internal/plugins/discovery"
	filesplugin "ouroboros/internal/plugins/files"
	"ouroboros/internal/plugins/gateways"
	"ouroboros/internal/plugins/gui"
	historyplugin "ouroboros/internal/plugins/history"
	matrixplugin "ouroboros/internal/plugins/matrix"
	memoryplugin "ouroboros/internal/plugins/memory"
	nodeplugin "ouroboros/internal/plugins/node"
	openaiplugin "ouroboros/internal/plugins/openai"
	opencodeplugin "ouroboros/internal/plugins/opencode"
	peersplugin "ouroboros/internal/plugins/peers"
	scheduleplugin "ouroboros/internal/plugins/schedule"
	sessionplugin "ouroboros/internal/plugins/session"
	shellplugin "ouroboros/internal/plugins/shell"
	skillsplugin "ouroboros/internal/plugins/skills"
	"ouroboros/internal/plugins/tailscale"
	"ouroboros/internal/plugins/talk"
	"ouroboros/internal/plugins/tui"
	updateplugin "ouroboros/internal/plugins/update"
	"ouroboros/internal/plugins/websearch"
	whatsappplugin "ouroboros/internal/plugins/whatsapp"
	"ouroboros/internal/proposal"
	"ouroboros/internal/setup"
	"ouroboros/internal/suggestion"
	"ouroboros/internal/update"
	"ouroboros/plugin"
)

// Version is the ouro release version.
const Version = "0.1.0"

// Settings come from the environment and are not persisted.
type Settings struct {
	Home        string // config, PID and log directory
	HTTPPort    int    // A2A port; falls back to a free port if taken
	DiscoPort   int    // UDP discovery port
	BaseURL     string // default LLM endpoint offered during setup
	APIKey      string // key for BaseURL offered during setup
	DeepSeekKey string // DeepSeek API key offered during setup
	// ChatGPTAuth and ChatGPTAPI are where the chatgpt provider signs in and
	// answers, those of OpenAI if empty. They are no setting of the user:
	// tests point them at a fake.
	ChatGPTAuth, ChatGPTAPI string
	// OpenCodeGo is where the opencode-go provider reaches OpenCode Go; ""
	// is where it is.
	OpenCodeGo string
}

// SettingsFromEnv reads OURO_HOME, OURO_PORT, OURO_DISCO_PORT, OPENAI_BASE_URL,
// OPENAI_API_KEY and DEEPSEEK_API_KEY.
func SettingsFromEnv() (Settings, error) {
	home := os.Getenv(homeEnv)
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
		Home:        home,
		HTTPPort:    httpPort,
		DiscoPort:   discoPort,
		BaseURL:     cmp.Or(os.Getenv("OPENAI_BASE_URL"), openai.DefaultBaseURL),
		APIKey:      os.Getenv("OPENAI_API_KEY"),
		DeepSeekKey: os.Getenv("DEEPSEEK_API_KEY"),
	}, nil
}

// homeEnv names the directory of the node.
const homeEnv = home.Env

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
// user through t: the plugins of the build that its configuration does not
// turn off. The order is only the order of the commands in the usage: the
// kernel starts each plugin after those it depends on.
func Plugins(s Settings, t Terminal) []plugin.Plugin { return plugins(s, t, &profile{}) }

// plugins is Plugins, which hand what runs a profile of the node to p once
// they are started.
func plugins(s Settings, t Terminal, p *profile) []plugin.Plugin {
	// The frame, see the package comment.
	dir := home.Dir(s.Home)
	network := &peers.Network{Contacts: dir.Contacts(), Client: node.Local{Home: dir}.Client}
	// What no tool for files does, whatever the configuration says.
	files := &guard.Guard{Home: string(dir.Root()), Sockets: home.SocketDir(), Secrets: dir.Secrets(), Protected: func() ([]string, error) {
		cfg, err := dir.Config()
		return cfg.Protected(), err
	}, Toolchain: update.GoPaths, Workspace: dir.WorkingDir()}
	owner := plugin.OwnerOf(dir, network)
	return plugin.Activate(owner, []plugin.Plugin{
		base.Plugin(dir, t),
		discovery.Plugin(s.DiscoPort),
		tailscale.Plugin(),
		peersplugin.Plugin(dir, network),
		filesplugin.Plugin(files),
		shellplugin.Plugin(dir, files),
		browserplugin.Plugin(dir.Browser()),
		websearch.Plugin(),
		computerplugin.Plugin(),
		historyplugin.Plugin(dir),
		memoryplugin.Plugin(dir),
		scheduleplugin.Plugin(dir),
		skillsplugin.Plugin(dir),
		abilitiesplugin.Plugin(),
		whatsappplugin.Plugin(dir),
		openaiplugin.Plugin(openaiplugin.Offer{BaseURL: s.BaseURL, APIKey: s.APIKey}),
		deepseekplugin.Plugin(deepseekplugin.Options{APIKey: s.DeepSeekKey}),
		chatgptplugin.Plugin(chatgpt.Options{Auth: s.ChatGPTAuth, API: s.ChatGPTAPI, HostID: dir.HostID}),
		opencodeplugin.Plugin(opencodeplugin.Options{BaseURL: s.OpenCodeGo}),
		claudecodeplugin.Plugin(claudecode.Options{Dir: filepath.Join(string(dir), "claude-code")}),
		claudesessionsplugin.Plugin(claudesessionsplugin.Options{Log: dir.Log}),
		assistant.Plugin(dir),
		sessionplugin.Plugin(dir),
		gateways.Plugin(),
		matrixplugin.Plugin(dir, files),
		nodeplugin.Plugin(dir, nodeplugin.Options{
			HTTPPort: s.HTTPPort, DiscoPort: s.DiscoPort, Version: Version, Log: t.Out,
			OnStop:  network.Close, // the channels the node opened to peers
			Profile: func(n *nodeplugin.Profile) { p.node = n },
		}),
		talk.Plugin(),
		desktopplugin.Plugin(desktopplugin.Options{
			Profile: func(d *desktopplugin.Profile) { p.desktop = d },
			Log:     func(err error) { dir.Log("desktop tray: %v", err) },
		}),
		tui.Plugin(owner, func(u *tui.Profile) { p.ui = u }),
		gui.Plugin(owner, func(g *gui.Profile) { p.gui = g }),
		updateplugin.Plugin(dir),
	}, fixed)
}

// fixed are the plugins the owner cannot turn off, with why: without them
// there is no ouro to speak of. The owner turns the others on and off in the
// configuration, see plugin.Activate.
var fixed = map[string]string{
	"base":      "it runs the commands of ouro",
	"node":      "it starts, stops and runs the node",
	"assistant": "it builds the agents",
	"sessions":  "it holds the conversations of the owner with the agent",
	"peers":     "it is the network of nodes",
	"tui":       "it is the terminal UI, where the plugins are turned on again",
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
	kernel.Seal[a2a.Client](k)     // signs as the node
	kernel.Seal[a2a.Server](k)     // checks signatures and trust, runs the built-in methods
	kernel.Seal[a2a.ChannelSet](k) // keeps the limits of the channels
	kernel.Seal[node.Local](k)
	kernel.Seal[node.Server](k)
	kernel.Seal[peers.Network](k)     // holds the contacts and the client
	kernel.Seal[plugin.Owner](k)      // decides whom the node trusts: handed to a front end in Plugins
	kernel.Seal[setup.Session](k)     // saves the configuration, and holds what a provider signed in with
	kernel.Seal[credentials.Store](k) // what the node signs in to its LLMs with
	kernel.Seal[llm.Book](k)          // opens the credentials for every provider
	kernel.Seal[guard.Guard](k)       // decides which files a tool touches
	kernel.Seal[proposal.Store](k)    // what the agent proposed; accepting it changes the configuration
	kernel.Seal[suggestion.Store](k)  // what the reviews suggested; taking one has the agent change ouro
}

// App runs ouro commands. It is a kernel with plugins and has no logic of its
// own: a command belongs to the plugin that contributes it.
type App struct {
	kernel *kernel.Kernel
	// profiles are the apps of the profiles of the node, by their
	// directories, the first one a itself; nil for an app of NewWith.
	profiles map[home.Dir]*App
	home     home.Dir
}

// New returns the standard ouro for the node described by s, talking to its
// user through t: with every profile of the node, see Run.
func New(s Settings, t Terminal) (*App, error) { return newProfiles(s, t) }

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

// Run executes the command given by args (without the program name), for
// the profile of the node that --profile names, see forProfile.
func (a *App) Run(ctx context.Context, args []string) error {
	app, args, err := a.forProfile(args)
	if err != nil {
		return err
	}
	ctx, args, err = app.startDirectory(ctx, args)
	if err != nil {
		return err
	}
	return app.dispatch(ctx, args)
}

// dispatch runs the command args of a.
func (a *App) dispatch(ctx context.Context, args []string) error {
	d, ok := Service[*base.Dispatcher](a)
	if !ok {
		return errors.New("no plugin runs commands: the base plugin is missing")
	}
	// Only the owner chat command is CLI input. Other commands (including
	// servers) must not attribute later API requests to this process.
	if len(args) > 0 && args[0] == "chat" {
		ctx = plugin.WithMessageSource(ctx, plugin.MessageSource{Interface: "cli"})
	}
	return d.Run(ctx, args)
}

// Service returns the service of type T of the plugins of a, for whoever
// runs the app.
func Service[T any](a *App) (T, bool) { return kernel.Service[T](a.kernel) }
