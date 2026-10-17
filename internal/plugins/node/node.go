// Package node is the plugin with the commands that start, stop and run the
// local node, and the setup dialog that configures it.
package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"

	"ouroboros/internal/a2a"
	"ouroboros/internal/assistant"
	"ouroboros/internal/home"
	"ouroboros/internal/node"
	"ouroboros/internal/openai"
	"ouroboros/internal/plugins/base"
	"ouroboros/internal/session"
	"ouroboros/plugin"
)

// Options are how the node runs.
type Options struct {
	HTTPPort  int       // A2A port; falls back to a free port if taken
	DiscoPort int       // UDP discovery port
	Version   string    // of ouro, as told in the agent card
	Log       io.Writer // of the node that runs in the foreground
	OnStop    func()    // called when the node in the foreground stops; may be nil
	// Profile is handed the Profile of the node in dir once the plugin is
	// started, for the frame of ouro to run the profiles of a node in one
	// process; may be nil.
	Profile func(*Profile)
}

// needs are the services the plugin uses.
type needs struct {
	Console  *plugin.Console
	Builder  assistant.Builder
	Sessions *session.Service
}

// Plugin contributes the commands that start, stop and run the node in dir.
// The node it runs answers peers with the agent of the assistant plugin,
// serves every plugin.Method a plugin contributed and opens every
// plugin.ChannelHandler. On its socket it serves the sessions of the owner,
// whose answers it runs; when a tool asks for a new build, the new node goes
// on with them from the archive.
//
// Who the node is and whom it trusts is read from dir by the node itself (see
// node.Server): no plugin provides it, and none can put something in its
// place.
func Plugin(dir home.Dir, o Options) plugin.Plugin {
	return plugin.New("node", func(ctx *plugin.Context, n needs) error {
		// answering is what answers the peers, once the node runs.
		var answering atomic.Pointer[assistant.Peers]
		c := &nodeCommands{
			term:     n.Console,
			sessions: n.Sessions,
			local:    node.Local{Home: dir},
			serving:  []func() []plugin.Serving{func() []plugin.Serving { return plugin.All[plugin.Serving](ctx) }},
			wizard:   &wizard{term: n.Console, home: dir, providers: func() []plugin.Provider { return plugin.All[plugin.Provider](ctx) }},
			server: node.Server{
				Home: dir, HTTPPort: o.HTTPPort, DiscoPort: o.DiscoPort, Version: o.Version, Log: dir.LogTo(o.Log),
				Responder: func(logf func(format string, args ...any)) (a2a.Responder, error) {
					// The agent for the configuration as it is with every
					// message: what the owner changes holds without a restart.
					peers := n.Builder.Peers(dir.Config, logf)
					if err := peers.Ready(); err != nil {
						return nil, err
					}
					answering.Store(peers)
					n.Sessions.ForgetPeers(peers.Forget) // a conversation the owner deletes
					// A conversation goes on in a new build as it was.
					peers.Recall(func(ctx context.Context, contextID string) []openai.Message {
						origin, _ := a2a.OriginFrom(ctx)
						return n.Sessions.Recall(origin.PublicKey, contextID)
					})
					return func(ctx context.Context, contextID, text string) (string, error) {
						// An answer of a peer keeps the node as one of the owner does.
						return n.Sessions.Peer(ctx, contextID, func(ctx context.Context) (string, error) {
							return peers.Respond(ctx, conversation(ctx, contextID), text)
						})
					}, nil
				},
				// What a peer tells without asking is kept, and starts no agent.
				Note: func(ctx context.Context, contextID, text string) error {
					return answering.Load().Note(ctx, conversation(ctx, contextID), text)
				},
				Methods:  func() []a2a.Method { return plugin.All[plugin.Method](ctx) },
				Channels: func() []a2a.ChannelHandler { return plugin.All[plugin.ChannelHandler](ctx) },
				Local:    session.Handler(n.Sessions),
				OnStop: func() {
					// The answers on their way are stopped with the node,
					// and the log says what they waited for.
					n.Sessions.Close()
					// Then what the plugins keep running, such as services:
					// nothing of the node outlives it.
					for _, s := range plugin.All[plugin.Serving](ctx) {
						if s.Stop != nil {
							s.Stop()
						}
					}
					if o.OnStop != nil {
						o.OnStop()
					}
				},
			},
		}
		plugin.Add(ctx, plugin.Command{Name: "start", Args: "[--setup]", Help: "start the node (asks for the LLM access + model on first run)", Run: c.start})
		plugin.Add(ctx, plugin.Command{Name: "stop", Help: "stop the node", Run: c.stop})
		plugin.Add(ctx, plugin.Command{Name: "status", Help: "show the local node", Run: c.status})
		plugin.Add(ctx, plugin.Command{Name: node.ServeCommand, Help: "run the node in the foreground", Run: c.serve})
		plugin.Add(ctx, plugin.Command{Name: "profile", Args: "[add NAME | rename PROFILE NAME]",
			Help: "list the profiles of the node, add one or rename one; ouro --profile PROFILE COMMAND runs a command for one", Run: c.profile})
		if o.Profile != nil {
			o.Profile(&Profile{commands: c})
		}
		return nil
	})
}

// conversation is what the agent remembers the conversation contextID of the
// peer it answers by. The peer picks contextID, so that alone would let it
// continue, and read, what another peer said: a conversation is one of the
// peer whose key signed the message.
func conversation(ctx context.Context, contextID string) string {
	origin, _ := a2a.OriginFrom(ctx)
	sum := sha256.Sum256([]byte(origin.PublicKey + "\n" + contextID))
	return hex.EncodeToString(sum[:])
}

type nodeCommands struct {
	term     *plugin.Console
	sessions *session.Service
	local    node.Local
	wizard   *wizard
	server   node.Server
	// joined are the sessions of the profiles the node runs, sessions
	// among them; nil for a node of one profile.
	joined []*session.Service
	// serving are what the plugins of the profiles the node runs do as it
	// serves, see plugin.Serving: this profile's first.
	serving []func() []plugin.Serving
}

func (c *nodeCommands) start(ctx context.Context, args []string) error {
	setup := false
	for _, arg := range args {
		if arg != "--setup" {
			return fmt.Errorf("unknown flag %q", arg)
		}
		setup = true
	}
	if p, ok := c.local.Running(); ok {
		// A node a front end started stops with it, unless the owner
		// wants it to run. A node of an older build knows no keeping, and
		// runs as it did.
		session.NewClient(c.local.Home).Keep(ctx)
		fmt.Fprintf(c.term.Out, "● already running (pid %d, port %d)\n", p.PID, p.Port)
		return nil
	}
	cfg, err := c.local.Home.Config()
	if err != nil {
		return err
	}
	if setup || !cfg.Complete() {
		if cfg, err = c.wizard.run(ctx, cfg); err != nil {
			return err
		}
	}
	// A configuration from before nodes had keys gets its key, and its ID, here.
	id, err := c.local.Home.Identity()
	if err != nil {
		return err
	}
	p, err := c.local.Start()
	if err != nil {
		return err
	}
	fmt.Fprintf(c.term.Out, "● ouroboros %s (%s) online on :%d → %s\n", id.ID(), cfg.Name, p.Port, cfg.Model)
	return nil
}

func (c *nodeCommands) stop(context.Context, []string) error {
	stopped, err := c.local.Stop()
	if err != nil {
		return err
	}
	if stopped {
		fmt.Fprintln(c.term.Out, "○ stopped")
	} else {
		fmt.Fprintln(c.term.Out, "○ not running")
	}
	return nil
}

func (c *nodeCommands) status(context.Context, []string) error {
	s, err := c.local.Status()
	if err != nil {
		return err
	}
	if !s.Running {
		fmt.Fprintln(c.term.Out, "○ not running")
		return nil
	}
	fmt.Fprintf(c.term.Out, "● running  id=%s  name=%s  port=%d  model=%s\n", s.ID, s.Name, s.Port, s.Model)
	return nil
}

// serve runs the node in the foreground until ctx is done or a signal arrives.
//
// When a tool asked for a new build of ouro, the node stops once no answer
// is on its way, and serve ends with plugin.Restarting: the process runs the
// new build in its place, which takes over the PID file and the socket. It
// finds the sessions where this one kept them, see session.Service.Keep.
func (c *nodeCommands) serve(ctx context.Context, _ []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, end := context.WithCancel(ctx)
	defer end()
	var mu sync.Mutex
	var successor string
	c.sessions.Attach(end, func(exe string) {
		mu.Lock()
		defer mu.Unlock()
		successor = exe
		end()
	})
	// What asked to go on by itself in this build goes on.
	profiles := c.joined
	if profiles == nil {
		profiles = []*session.Service{c.sessions}
	}
	// What the plugins of a node that ended without stopping left is
	// cleaned up before anything goes on.
	for _, all := range c.serving {
		for _, s := range all() {
			if s.Start != nil {
				s.Start()
			}
		}
	}
	for _, s := range profiles {
		s.GoOn()
	}
	server := c.server
	server.Successor = func() string {
		mu.Lock()
		defer mu.Unlock()
		return successor
	}
	// What serve started stops with the node, also with one that did not
	// start: the node stops each profile as it stops, and serve stops those
	// it did not, each once.
	stops := []func(){sync.OnceFunc(server.OnStop)}
	server.OnStop = stops[0]
	server.Profiles = slices.Clone(server.Profiles)
	for i, p := range server.Profiles {
		stops = append(stops, sync.OnceFunc(p.OnStop))
		server.Profiles[i].OnStop = stops[i+1]
	}
	err := server.Run(ctx)
	for _, stop := range stops {
		stop()
	}
	if s, ok := errors.AsType[*node.Succession](err); ok {
		return &plugin.Restarting{Exe: s.Exe, Env: append(os.Environ(), s.Env...)}
	}
	return base.WithHint(err)
}

// Profile is the part of the plugin that runs one profile of a node: what
// serve runs for it. The frame of ouro joins the others to the first, see
// Join; no plugin gets it.
type Profile struct {
	commands *nodeCommands
	joined   []*session.Service // the sessions of p and of those it joined
}

// Join lets the node of p run the profile other in its process, with the
// agent, the sessions and the plugins other was started with: on its port,
// on its socket, among what it tells discovery, and stopping and starting
// anew with it. It is to be called before the node runs.
func (p *Profile) Join(other *Profile) {
	p.commands.server.Profiles = append(p.commands.server.Profiles, other.commands.server)
	if p.joined == nil {
		p.joined = []*session.Service{p.commands.sessions}
	}
	p.joined = append(p.joined, other.commands.sessions)
	p.commands.joined = p.joined
	p.commands.serving = append(p.commands.serving, other.commands.serving...)
	session.Share(p.joined...)
}
