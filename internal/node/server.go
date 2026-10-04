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
	// Responder returns what answers the messages of trusted peers for the
	// node configured by cfg.
	Responder func(cfg config.Config) (a2a.Responder, error)
	// Methods returns the A2A methods added to the built-in ones. May be nil.
	Methods func() []a2a.Method
	// Channels returns the channels peers can open. May be nil.
	Channels func() []a2a.ChannelHandler
	// OnStop is called when the node stops, before it has stopped: for what
	// would not end by itself, such as the channels the node opened to peers.
	// May be nil.
	OnStop func()
}

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
	go discovery.Serve(udp, discovery.Node{ID: id.ID(), Name: cfg.Name, Model: cfg.Model, PublicKey: id.Public(), Port: port})

	logger := log.New(s.Log, "", log.LstdFlags)
	respond, err := s.Responder(cfg)
	if err != nil {
		ln.Close()
		return err
	}
	server := &a2a.Server{
		Card:     func() a2a.AgentCard { return card(cfg, s.Version) },
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
	release, err := daemon.Hold(s.Home.Service().PIDFile, port)
	if err != nil {
		ln.Close()
		return err
	}
	defer release()

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
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
		srv.Shutdown(shutdownCtx)
		closing.Wait()
	}()

	logger.Printf("ouroboros %s (%s) listening on :%d → %s", id.ID(), cfg.Name, port, cfg.Model)
	if allowed := cfg.PeerTools(); len(allowed) > 0 {
		logger.Printf("peers may use the tools: %s", strings.Join(allowed, ", "))
	}
	// TLS only, with the certificate of the node's key: peers pin it, and
	// nobody on the way reads along.
	if err := srv.Serve(tls.NewListener(ln, a2a.TLSConfig(id))); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-stopped
	logger.Print("stopped")
	return nil
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
