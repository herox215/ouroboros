package node

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/diagnosis"
	"ouroboros/internal/discovery"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
)

// shutdownTimeout is how long requests that are being answered may take when
// the node stops.
const shutdownTimeout = 5 * time.Second

// netListen opens a port; tests replace it to get in the way.
var netListen = net.Listen

// Server is a node: it answers discovery probes and the messages of the
// peers its owner trusts.
type Server struct {
	Home      home.Dir
	HTTPPort  int // A2A port; falls back to a free port if taken
	DiscoPort int // UDP discovery port
	Version   string
	Log       io.Writer
	// Responder returns what answers the messages of trusted peers, or the
	// error of a configuration it cannot answer with. It is told what goes
	// wrong later with logf.
	Responder func(logf func(format string, args ...any)) (a2a.Responder, error)
	// Note keeps what a trusted peer tells the node without asking for an
	// answer, see a2a.Server.Note. It is called once Responder was. May be
	// nil: the node takes no notes then.
	Note func(ctx context.Context, contextID, text string) error
	// Methods returns the A2A methods added to the built-in ones. May be nil.
	Methods func() []a2a.Method
	// Channels returns the channels peers can open. May be nil.
	Channels func() []a2a.ChannelHandler
	// OnStop is called when the node stops, before it has stopped: for what
	// would not end by itself, such as the channels the node opened to peers.
	// May be nil.
	OnStop func()
	// Local answers the front ends of the owner on the socket of the node
	// (home.Dir.Socket), which only the user who runs the node can open. May
	// be nil.
	Local http.Handler
	// Profiles are the other profiles of the node that it runs, with what
	// each answers with, see home.Dir.Profiles. Of them, Home, Responder,
	// Methods, Channels, OnStop and Local are used.
	Profiles []Server
	// Successor returns the binary that takes the place of the node once it
	// stopped, "" if none does: then Run hands on its PID file and its socket
	// and returns a *Succession. It is asked once the node stopped. May be
	// nil.
	Successor func() string
}

// Succession is what Run ends with when a new build takes the place of the
// node: the process runs Exe in its place, with its arguments and Env added
// to its environment. Env names what the new node takes over.
type Succession struct {
	Exe string
	Env []string
}

func (s *Succession) Error() string { return "the node is handed over to " + s.Exe }

// headerTime is how long a connection may take for its handshake and the
// header of a request before the node closes it: one that sends nothing
// does not keep a place. Tests shorten it.
var headerTime = 10 * time.Second

// Run serves until ctx is done. It opens the A2A port for TLS, answers discovery
// probes, holds the PID file and hands the messages of trusted peers to an
// Responder. What the node offers over A2A is defined in a2a.Server plus
// Methods and Channels.
func (s Server) Run(ctx context.Context) error {
	cfg, err := s.Home.Config()
	if err != nil {
		return err
	}
	if !cfg.Complete() {
		return config.ErrIncomplete
	}
	id, err := s.Home.Identity()
	if err != nil {
		return err
	}

	ln, err := listen(s.HTTPPort)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	udp, err := discovery.Listen(s.DiscoPort)
	if err != nil {
		ln.Close()
		return fmt.Errorf("discovery: %w", err)
	}
	defer udp.Close()
	logger := log.New(s.Log, "", log.LstdFlags)
	var agents []*agent
	for _, profile := range append([]Server{s}, s.Profiles...) {
		a, err := profile.agent(logger, s.Version)
		if err != nil {
			ln.Close()
			return err
		}
		agents = append(agents, a)
	}
	// How the profiles are called may change while the node runs: it is
	// read when it is told.
	go discovery.ServeAll(udp, func() []discovery.Node {
		nodes := make([]discovery.Node, len(agents))
		for i, a := range agents {
			c := a.now()
			nodes[i] = discovery.Node{ID: a.id.ID(), Name: c.Name, Model: c.Model, PublicKey: a.id.Public(), Port: port}
		}
		return nodes
	})
	// A peer names the profile it writes to by the name of the server in
	// the handshake, see a2a.ServerName: the handshake is made with its
	// certificate, which the peer pins, and the request goes to it. A peer
	// that names none, as an ouro from before there were profiles, reaches
	// the first one.
	byName := map[string]*agent{}
	for _, a := range agents {
		byName[a2a.ServerName(a.id.Public())] = a
	}
	addressed := func(name string) *agent { return cmp.Or(byName[name], agents[0]) }
	tlsConfig := a2a.TLSConfig(id)
	tlsConfig.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		return &addressed(hello.ServerName).cert, nil
	}
	public := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addressed(r.TLS.ServerName).handler.ServeHTTP(w, r)
	})
	// A front end names the profile by its ID, see ProfileHeader.
	localHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := agents[0]
		if want := r.Header.Get(ProfileHeader); want != "" {
			i := slices.IndexFunc(agents, func(a *agent) bool { return a.id.ID() == want })
			if i < 0 {
				http.Error(w, "the node runs no profile "+want, http.StatusNotFound)
				return
			}
			a = agents[i]
		}
		if a.local == nil {
			http.NotFound(w, r)
			return
		}
		a.local.ServeHTTP(w, r)
	})
	srv := &http.Server{
		Handler:           logRequests(logger, public),
		ReadHeaderTimeout: headerTime,
		ErrorLog:          logger, // failed handshakes, such as plain HTTP
	}

	// Held until the node is gone: the lock on the file is what tells other
	// processes that it runs.
	lease, err := daemon.Hold(s.Home.Service().PIDFile, port)
	if err != nil {
		ln.Close()
		return err
	}
	// The socket is opened once the PID file is the node's: a socket left
	// behind is none of another node then.
	local, err := listenLocal(s.Home.Socket())
	if err != nil {
		lease.Release()
		ln.Close()
		return fmt.Errorf("the socket of the node: %w", err)
	}
	// What waits for more on the socket, such as a front end that watches a
	// session, ends when the node stops.
	localCtx, endLocal := context.WithCancel(context.Background())
	defer endLocal()
	localSrv := &http.Server{Handler: localHandler, ErrorLog: logger, BaseContext: func(net.Listener) context.Context { return localCtx }}
	go localSrv.Serve(local)

	var successor *Succession // once the node stopped
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		if s.Successor != nil {
			if exe := s.Successor(); exe != "" {
				successor = handOver(exe, local, logger)
			}
		}
		// A channel is no request that the shutdown waits for: the ones peers
		// opened are closed here, and no further one is opened. That goes
		// along with the shutdown, so that stopping takes no longer for it.
		var closing sync.WaitGroup
		for _, a := range agents {
			closing.Go(a.server.Close)
			if a.onStop != nil {
				closing.Go(a.onStop)
			}
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		endLocal()
		closing.Go(func() { localSrv.Shutdown(shutdownCtx) })
		srv.Shutdown(shutdownCtx)
		closing.Wait()
	}()

	for _, a := range agents {
		cfg := a.now()
		logger.Printf("ouroboros %s (%s) listening on :%d → %s", a.id.ID(), cfg.Name, port, cfg.Model)
		if allowed := cfg.PeerTools(); len(allowed) > 0 {
			names := make([]string, len(allowed))
			for i, e := range allowed {
				names[i] = e.Tool
			}
			logger.Printf("peers may use the tools: %s", strings.Join(names, ", "))
		}
	}
	// TLS only, with the certificate of the node's key: peers pin it, and
	// nobody on the way reads along.
	if err := srv.Serve(tls.NewListener(ln, tlsConfig)); !errors.Is(err, http.ErrServerClosed) {
		localSrv.Close()
		lease.Release()
		return err
	}
	<-stopped
	if successor == nil {
		lease.Release()
		logger.Print("stopped")
		return nil
	}
	// The PID file stays locked across the exec: no other node starts in
	// between.
	env, err := bequeath(lease)
	if err != nil {
		lease.Release()
		return fmt.Errorf("the node cannot be handed over: %w", err)
	}
	successor.Env = append(successor.Env, env)
	logger.Printf("stopped, handing over to %s", successor.Exe)
	return successor
}

// agent is a profile the node runs: who it is, what it answers peers and
// the front ends of its owner with, and how it is called now.
type agent struct {
	id      *identity.Identity
	cert    tls.Certificate
	server  *a2a.Server
	handler http.Handler
	local   http.Handler // nil for none
	onStop  func()
	now     func() config.Config
}

// agent returns the profile of the node that s describes, which logs to
// logger.
func (s Server) agent(logger *log.Logger, version string) (*agent, error) {
	cfg, err := s.Home.Config()
	if err != nil {
		return nil, err
	}
	id, err := s.Home.Identity()
	if err != nil {
		return nil, err
	}
	respond, err := s.Responder(logger.Printf)
	if err != nil {
		return nil, err
	}
	// What the profile answers with may change while the node runs: it is
	// read when it is told.
	now := s.current(cfg)
	server := &a2a.Server{
		Card:     func() a2a.AgentCard { return card(now(), version) },
		Identity: id,
		Trust:    s.Home.Contacts(),
		Respond:  respond,
		Note:     s.Note,
		Methods:  s.Methods,
		Channels: s.Channels,
		Log:      logger.Printf,
		Failed:   keep(s.Home.Failures(), logger),
	}
	return &agent{id: id, cert: id.Certificate(), server: server, handler: server.Handler(), local: s.Local, onStop: s.OnStop, now: now}, nil
}

// ProfileHeader is the header of a request on the socket of the node that
// names the profile it is for by its ID: the first one without it.
const ProfileHeader = "Ouro-Profile"

// bequeath hands the PID file of a lease on; tests replace it to get in the
// way.
var bequeath = (*daemon.Lease).Bequeath

// handOver prepares the succession of the node by exe: the socket local
// stays open for it, and front ends that connect meanwhile wait in it until
// the new node answers. A socket that cannot be handed on is let go: the
// new node opens it anew.
func handOver(exe string, local *net.UnixListener, logger *log.Logger) *Succession {
	succession := &Succession{Exe: exe}
	fd, err := bequeathSocket(local)
	if err != nil {
		logger.Printf("the socket is not handed over, the new node opens it anew: %v", err)
		return succession
	}
	succession.Env = append(succession.Env, fmt.Sprintf("%s=%d", socketEnv, fd))
	return succession
}

// current returns what reads the configuration of the node as it is now.
// One that cannot be read or is not complete does not rename a node that
// runs: the last one that was stays, at first cfg.
func (s Server) current(cfg config.Config) func() config.Config {
	var mu sync.Mutex
	return func() config.Config {
		mu.Lock()
		defer mu.Unlock()
		if now, err := s.Home.Config(); err == nil && now.Complete() {
			cfg = now
		}
		return cfg
	}
}

// keep returns what records the answers to peers that failed, for the agent
// to explain them to the peer that asks. Of the error only its category is
// kept: the error itself is in the log.
func keep(failures *diagnosis.Store, logger *log.Logger) func(a2a.Failure) {
	return func(f a2a.Failure) {
		err := failures.Record(diagnosis.Entry{
			ID: f.ID, Peer: f.Peer, ContextID: f.ContextID, MessageID: f.MessageID,
			At: time.Now(), Cause: diagnosis.Classify(f.Err),
		})
		if err != nil {
			logger.Printf("error %s is not kept for the peer to ask about: %v", f.ID, err)
		}
	}
}

// listen opens the A2A port, falling back to a free port if it is taken.
func listen(port int) (net.Listener, error) {
	ln, err := netListen("tcp", fmt.Sprintf(":%d", port))
	if err != nil && port != 0 {
		return netListen("tcp", ":0")
	}
	return ln, err
}

// card is what the node tells everyone about itself. It is public: anyone
// can read it without being connected. Add a skill here when the node learns
// something peers should know about.
func card(cfg config.Config, version string) a2a.AgentCard {
	return a2a.AgentCard{
		ProtocolVersion:    a2a.ProtocolVersion,
		Name:               cfg.Name,
		Description:        cfg.About(),
		PreferredTransport: "JSONRPC",
		Version:            version,
		// No streaming: that is A2A's message/stream, which the node does not
		// have. What it has for data that flows is announced as an extension.
		Capabilities:       a2a.Capabilities{Streaming: false, Extensions: []a2a.Extension{a2a.ChannelExtension()}},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.Skill{{
			ID:          "chat",
			Name:        "Chat",
			Description: "Chat with " + cfg.Model,
			Tags:        []string{"chat", "llm"},
		}},
	}
}

func logRequests(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Printf("%s %s %s", r.RemoteAddr, r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
