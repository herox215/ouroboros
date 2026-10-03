package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/discovery"
	"ouroboros/internal/openai"
)

// serve runs the node in the foreground until ctx is done or a signal arrives.
func (a *App) serve(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(a.settings.Home)
	if err != nil {
		return err
	}
	if !cfg.Complete() {
		return errors.New("node is not configured, run: ouro start")
	}
	id, err := a.identity(&cfg)
	if err != nil {
		return err
	}

	ln, err := listen(a.settings.HTTPPort)
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port

	udp, err := discovery.Listen(a.settings.DiscoPort)
	if err != nil {
		ln.Close()
		return fmt.Errorf("discovery: %w", err)
	}
	defer udp.Close()
	go discovery.Serve(udp, discovery.Node{ID: cfg.ID, Name: cfg.Name, Model: cfg.Model, PublicKey: id.Public(), Port: port})

	logger := log.New(a.out, "", log.LstdFlags)
	// Peers get an agent without tools: it cannot be made to write to others.
	bot := agent.New(openai.New(cfg.BaseURL, cfg.APIKey), cfg.Model,
		fmt.Sprintf("You are %q, an Ouroboros agent reachable via A2A. Be helpful and concise.", cfg.Name))
	server := &a2a.Server{Card: agentCard(cfg), Identity: id, Trust: a.contacts(), Respond: bot.Respond}
	srv := &http.Server{
		Handler:           logRequests(logger, server.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
	}

	if err := daemon.WritePIDFile(a.pidPath(), port); err != nil {
		ln.Close()
		return err
	}
	defer os.Remove(a.pidPath())

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	logger.Printf("ouroboros %s (%s) listening on :%d → %s", cfg.ID, cfg.Name, port, cfg.Model)
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

func agentCard(cfg config.Config) a2a.AgentCard {
	return a2a.AgentCard{
		ProtocolVersion:    a2a.ProtocolVersion,
		Name:               cfg.Name,
		Description:        fmt.Sprintf("Ouroboros node %s (%s)", cfg.ID, cfg.Model),
		PreferredTransport: "JSONRPC",
		Version:            Version,
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
