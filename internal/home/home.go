// Package home is the directory a node keeps its state in: its configuration,
// its identity, its contacts, the proposals of its agent, the answers that
// failed and the files of its background process.
//
// Every file of a node is named here and nowhere else. To keep more state,
// add a method to Dir that returns its path or opens it.
package home

import (
	"log"
	"os"
	"path/filepath"

	"ouroboros/internal/config"
	"ouroboros/internal/contacts"
	"ouroboros/internal/daemon"
	"ouroboros/internal/diagnosis"
	"ouroboros/internal/identity"
	"ouroboros/internal/proposal"
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

// SaveConfig writes the configuration. The identity of the node is not given
// up by it: a configuration without a key, read before the key was generated,
// keeps the one that was persisted since.
func (d Dir) SaveConfig(c config.Config) error {
	return config.Update(string(d), func(cur *config.Config) error {
		if c.PrivateKey == "" {
			c.PrivateKey = cur.PrivateKey
		}
		*cur = c
		return nil
	})
}

// Secrets are the files that hold the secrets of the node, which no tool of
// an agent reads: the configuration and the file it is locked with.
func (d Dir) Secrets() []string { return config.Files(string(d)) }

// Contacts opens the contact list of the node.
func (d Dir) Contacts() *contacts.Store { return contacts.Open(d.path("contacts.json")) }

// Proposals opens the list of the changes of the configuration the agent of
// the owner proposed, which wait for the owner to decide.
func (d Dir) Proposals() *proposal.Store { return proposal.Open(d.path("proposals.json")) }

// Failures opens the list of the answers to peers that failed.
func (d Dir) Failures() *diagnosis.Store { return diagnosis.Open(d.path("failures.json")) }

// Service is the background process of the node.
func (d Dir) Service() daemon.Service {
	return daemon.Service{PIDFile: d.path("ouro.pid"), LogFile: d.path("ouro.log")}
}

// Log adds a line to the log of the node. It is for a process that is not the
// node itself, such as the terminal UI: the node writes its log on its own. A
// log that cannot be written is nothing to fail for.
func (d Dir) Log(format string, args ...any) {
	f, err := os.OpenFile(d.Service().LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	log.New(f, "", log.LstdFlags).Printf(format, args...)
}

// Identity returns the signing identity of the node, generating and
// persisting one on first use. Processes that ask at the same time get the
// same one: the first generates it under the lock on the configuration, the
// others find it there.
func (d Dir) Identity() (*identity.Identity, error) {
	cfg, err := d.Config()
	if err != nil {
		return nil, err
	}
	if cfg.PrivateKey == "" {
		err = config.Update(string(d), func(c *config.Config) error {
			if c.PrivateKey == "" {
				c.PrivateKey = identity.Generate().Private()
			}
			cfg = *c
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return identity.FromPrivate(cfg.PrivateKey)
}
