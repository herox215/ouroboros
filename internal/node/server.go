package node

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/diagnosis"
	"ouroboros/internal/discovery"
	"ouroboros/internal/home"
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
	// How the node is called and what it answers with may change while it
	// runs: both are read when they are told.
	now := s.current(cfg)
	go discovery.Serve(udp, func() discovery.Node {
		cfg := now()
		return discovery.Node{ID: id.ID(), Name: cfg.Name, Model: cfg.Model, PublicKey: id.Public(), Port: port}
	})

	logger := log.New(s.Log, "", log.LstdFlags)
	respond, err := s.Responder(logger.Printf)
	if err != nil {
		ln.Close()
		return err
	}
	server := &a2a.Server{
		Card:     func() a2a.AgentCard { return card(now(), s.Version) },
		Identity: id,
		Trust:    s.Home.Contacts(),
		Respond:  respond,
		Methods:  s.Methods,
		Channels: s.Channels,
		Log:      logger.Printf,
		Failed:   keep(s.Home.Failures(), logger),
	}
	srv := &http.Server{
		Handler:           logRequests(logger, server.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
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
	localSrv := &http.Server{Handler: s.Local, ErrorLog: logger, BaseContext: func(net.Listener) context.Context { return localCtx }}
	if localSrv.Handler == nil {
		localSrv.Handler = http.NotFoundHandler()
	}
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
		closing.Go(server.Close)
		if s.OnStop != nil {
			closing.Go(s.OnStop)
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		endLocal()
		closing.Go(func() { localSrv.Shutdown(shutdownCtx) })
		srv.Shutdown(shutdownCtx)
		closing.Wait()
	}()

	logger.Printf("ouroboros %s (%s) listening on :%d → %s", id.ID(), cfg.Name, port, cfg.Model)
	if allowed := cfg.PeerTools(); len(allowed) > 0 {
		names := make([]string, len(allowed))
		for i, e := range allowed {
			names[i] = e.Tool
		}
		logger.Printf("peers may use the tools: %s", strings.Join(names, ", "))
	}
	// TLS only, with the certificate of the node's key: peers pin it, and
	// nobody on the way reads along.
	if err := srv.Serve(tls.NewListener(ln, a2a.TLSConfig(id))); !errors.Is(err, http.ErrServerClosed) {
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
		Description:        "Ouroboros instance named " + cfg.Name,
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
