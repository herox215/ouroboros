// Package home is the directory a node keeps its state in: its configuration,
// its identity, its contacts and the files of its background process.
//
// Every file of a node is named here and nowhere else. To keep more state,
// add a method to Dir that returns its path or opens it.
package home

import (
	"os"
	"path/filepath"

	"ouroboros/internal/config"
	"ouroboros/internal/contacts"
	"ouroboros/internal/daemon"
	"ouroboros/internal/identity"
)

// Dir is the path of the directory.
type Dir string

// Create makes the directory, readable only by the owner.
func (d Dir) Create() error { return os.MkdirAll(string(d), 0o700) }

func (d Dir) path(name string) string { return filepath.Join(string(d), name) }

// UpdateDir is where the sources of a self-update are kept.
func (d Dir) UpdateDir() string { return d.path("update") }

// Config reads the configuration. A node that was never set up has an empty one.
func (d Dir) Config() (config.Config, error) { return config.Load(string(d)) }

// SaveConfig writes the configuration.
func (d Dir) SaveConfig(c config.Config) error { return config.Save(string(d), c) }

// Contacts opens the contact list of the node.
func (d Dir) Contacts() *contacts.Store { return contacts.Open(d.path("contacts.json")) }

// Service is the background process of the node.
func (d Dir) Service() daemon.Service {
	return daemon.Service{PIDFile: d.path("ouro.pid"), LogFile: d.path("ouro.log")}
}

// Identity returns the signing identity of the node, generating and
// persisting one on first use.
func (d Dir) Identity() (*identity.Identity, error) {
	cfg, err := d.Config()
	if err != nil {
		return nil, err
	}
	if cfg.PrivateKey != "" {
		return identity.FromPrivate(cfg.PrivateKey)
	}
	id := identity.Generate()
	cfg.PrivateKey = id.Private()
	return id, d.SaveConfig(cfg)
}
