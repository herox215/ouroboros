package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/discovery"
	"ouroboros/internal/home"
)

// shutdownTimeout is how long requests that are being answered may take when
// the node stops.
const shutdownTimeout = 5 * time.Second

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
}

// Run serves until ctx is done. It opens the A2A port, answers discovery
// probes, holds the PID file and hands the messages of trusted peers to an
// Responder. What the node offers over A2A is defined in a2a.Server plus
// Methods.
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
	}
	srv := &http.Server{
		Handler:           logRequests(logger, server.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	logger.Printf("ouroboros %s (%s) listening on :%d → %s", id.ID(), cfg.Name, port, cfg.Model)
	if allowed := cfg.PeerTools(); len(allowed) > 0 {
		logger.Printf("peers may use the tools: %s", strings.Join(allowed, ", "))
	}
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-stopped
	logger.Print("stopped")
	return nil
}

// listen opens the A2A port, falling back to a free port if it is taken.
func listen(port int) (net.Listener, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil && port != 0 {
		return net.Listen("tcp", ":0")
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
