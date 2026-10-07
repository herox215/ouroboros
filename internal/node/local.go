// Package node is the node of this host: Server runs it, Local controls the
// one that runs in the background.
//
// The node runs as its own process ("ouro serve", started by Local.Start).
// Other processes, such as the terminal UI, find it through the PID file in
// its home directory, which also records its port.
package node

import (
	"os"
	"path/filepath"

	"ouroboros/internal/a2a"
	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/home"
	"ouroboros/internal/openai"
)

// ServeCommand is the argument that makes the binary run a Server.
const ServeCommand = "serve"

// Local controls the node that runs in the background of this host.
type Local struct {
	Home home.Dir
	// Exe is the binary that is started; if nil, the running one.
	Exe func() (string, error)
}

// Status describes the local node.
type Status struct {
	Configured bool
	Running    bool
	ID         string
	Name       string
	Model      string
	// Provider is the name of the service the model is answered by, as it
	// is commonly called, like "iGate" or "localhost". It follows from the
	// base URL of the configuration; an unconfigured node has none.
	Provider string
	Port     int // A2A port while running
	// Debug says that the node is in debug mode, see home.Dir.Debug.
	Debug bool
}

// Status reports how the node is configured and whether it is running.
func (l Local) Status() (Status, error) {
	cfg, err := l.Home.Config()
	if err != nil {
		return Status{}, err
	}
	s := Status{Configured: cfg.Complete(), ID: cfg.ID(), Name: cfg.Name, Model: cfg.Model, Provider: openai.Provider(cfg.BaseURL), Debug: l.Home.Debug()}
	if p, ok := l.Running(); ok {
		s.Running, s.Port = true, p.Port
	}
	return s, nil
}

// Running returns the process of the node if it is running.
func (l Local) Running() (daemon.Process, bool) { return l.Home.Service().Running() }

// Start runs the node in the background unless it is running already. A node
// that was not set up yet fails with config.ErrIncomplete.
func (l Local) Start() (daemon.Process, error) {
	if p, ok := l.Running(); ok {
		return p, nil
	}
	cfg, err := l.Home.Config()
	if err != nil {
		return daemon.Process{}, err
	}
	if !cfg.Complete() {
		return daemon.Process{}, config.ErrIncomplete
	}
	exe, err := l.Executable()
	if err != nil {
		return daemon.Process{}, err
	}
	return l.Home.Service().Start(exe, ServeCommand)
}

// Stop stops the node and reports whether it was running.
func (l Local) Stop() (bool, error) { return l.Home.Service().Stop() }

// Client returns the client that signs requests as this node. While the node
// is running, peers learn its port from it and can call back.
func (l Local) Client() (*a2a.Client, error) {
	cfg, err := l.Home.Config()
	if err != nil {
		return nil, err
	}
	id, err := l.Home.Identity()
	if err != nil {
		return nil, err
	}
	p, _ := l.Running()
	return &a2a.Client{Identity: id, Name: cfg.Name, Model: cfg.Model, Port: p.Port}, nil
}

// osExecutable finds the running binary; tests replace it to get in the way.
var osExecutable = os.Executable

// Executable is the path of the running binary with symlinks resolved.
func Executable() (string, error) {
	exe, err := osExecutable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// Executable is the binary Start runs: Exe, or the running one.
func (l Local) Executable() (string, error) {
	if l.Exe != nil {
		return l.Exe()
	}
	return Executable()
}
