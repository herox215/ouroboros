package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/contacts"
	"ouroboros/internal/daemon"
	"ouroboros/internal/discovery"
	"ouroboros/internal/tui"
)

// tui runs the terminal UI until the user quits.
func (a *App) tui(ctx context.Context) error {
	exe, err := executable()
	if err != nil {
		return err
	}
	final, err := tea.NewProgram(tui.New(tuiBackend{a}), tea.WithContext(ctx)).Run()
	if m, ok := final.(*tui.Model); ok && err == nil && m.Restart() {
		return syscall.Exec(exe, os.Args, os.Environ()) // continue as the updated binary
	}
	return err
}

// tuiBackend gives the terminal UI access to the local node and the network.
type tuiBackend struct{ a *App }

func (b tuiBackend) Status() (tui.Status, error) {
	cfg, err := config.Load(b.a.settings.Home)
	if err != nil {
		return tui.Status{}, err
	}
	s := tui.Status{Configured: cfg.Complete(), ID: cfg.ID, Name: cfg.Name, Model: cfg.Model}
	if p, ok := daemon.Running(b.a.pidPath()); ok {
		s.Running, s.Port = true, p.Port
	}
	return s, nil
}

func (b tuiBackend) Start() error {
	if _, ok := daemon.Running(b.a.pidPath()); ok {
		return nil
	}
	cfg, err := config.Load(b.a.settings.Home)
	if err != nil {
		return err
	}
	if !cfg.Complete() {
		return errors.New("node is not configured, press c")
	}
	_, err = b.a.launch()
	return err
}

func (b tuiBackend) Stop() error {
	_, err := b.a.stopNode()
	return err
}

func (b tuiBackend) Discover() ([]discovery.Node, error) {
	return b.a.discover()
}

func (b tuiBackend) Ask(ctx context.Context, contextID, text string, step func(tui.Step)) (string, error) {
	bot, err := b.a.assistant()
	if err != nil {
		return "", err
	}
	return bot.Run(ctx, contextID, text, func(s agent.Step) {
		if s, ok := peerStep(s); ok {
			step(s)
		}
	})
}

func (b tuiBackend) Contacts() (tui.Contacts, error) {
	data, err := b.a.contacts().Snapshot()
	if err != nil {
		return tui.Contacts{}, err
	}
	trusted := make(map[string]contacts.Contact, len(data.Trusted))
	for _, c := range data.Trusted {
		trusted[c.PublicKey] = c
	}
	return tui.Contacts{Trusted: trusted, Incoming: data.Incoming, Outgoing: data.Outgoing}, nil
}

func (b tuiBackend) Accept(publicKey string) error {
	req, ok := b.a.contacts().Approve(publicKey)
	if !ok {
		return errors.New("request is gone")
	}
	return b.notify(req.Addr, func(c *a2a.Client, ctx context.Context, url string) error {
		return c.Accept(ctx, url)
	})
}

func (b tuiBackend) Reject(publicKey string) error {
	req, ok := b.a.contacts().Reject(publicKey)
	if !ok {
		return errors.New("request is gone")
	}
	return b.notify(req.Addr, func(c *a2a.Client, ctx context.Context, url string) error {
		return c.Reject(ctx, url)
	})
}

// notify tells the peer at addr about a decision. The local decision already
// stands; a failed callback is reported but not rolled back.
func (b tuiBackend) notify(addr string, fn func(*a2a.Client, context.Context, string) error) error {
	if addr == "" {
		return nil
	}
	cfg, err := config.Load(b.a.settings.Home)
	if err != nil {
		return err
	}
	client, err := b.a.client(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := fn(client, ctx, "http://"+addr+"/"); err != nil {
		return fmt.Errorf("decided locally, but %s did not answer: %w", addr, err)
	}
	return nil
}

func (b tuiBackend) UpdateAvailable() (bool, error) {
	u := b.a.updater
	u.Batch = true
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rev, err := u.Latest(ctx)
	// A build without a known revision cannot tell, so it does not nag.
	return err == nil && u.Revision != "" && rev != u.Revision, err
}

func (b tuiBackend) Update() (bool, error) {
	_, updated, err := b.a.selfUpdate(context.Background(), io.Discard, true)
	return updated, err
}

// SetupCommand runs the interactive setup, which starts the node afterwards.
func (b tuiBackend) SetupCommand() (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(exe, "start", "--setup"), nil
}
