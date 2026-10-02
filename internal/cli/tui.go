package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"

	tea "charm.land/bubbletea/v2"

	"ouroboros/internal/a2a"
	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/discovery"
	"ouroboros/internal/tui"
)

// tui runs the terminal UI until the user quits.
func (a *App) tui(ctx context.Context) error {
	_, err := tea.NewProgram(tui.New(tuiBackend{a}), tea.WithContext(ctx)).Run()
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
	return discovery.Discover(b.a.settings.DiscoPort, b.a.discoverWait)
}

func (b tuiBackend) Send(ctx context.Context, addr, contextID, text string) (string, error) {
	return a2a.Send(ctx, "http://"+addr+"/", contextID, text)
}

// SetupCommand runs the interactive setup, which starts the node afterwards.
func (b tuiBackend) SetupCommand() (*exec.Cmd, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(exe, "start", "--setup"), nil
}
