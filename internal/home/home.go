// Package home is the directory a node keeps its state in: its configuration,
// its identity, its contacts, the proposals of its agent, the answers that
// failed and the files of its background process.
//
// Every file of a node is named here and nowhere else. To keep more state,
// add a method to Dir that returns its path or opens it.
package home

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"ouroboros/internal/config"
	"ouroboros/internal/contacts"
	"ouroboros/internal/daemon"
	"ouroboros/internal/diagnosis"
	"ouroboros/internal/identity"
	"ouroboros/internal/proposal"
	"ouroboros/internal/safefile"
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

// History is the directory the conversations of the agents are archived in.
func (d Dir) History() string { return d.path("history") }

// Failures opens the list of the answers to peers that failed.
func (d Dir) Failures() *diagnosis.Store { return diagnosis.Open(d.path("failures.json")) }

// Env names the directory of a node to the process that is started for it.
const Env = "OURO_HOME"

// Service is the background process of the node. It is started for this
// directory, whatever the environment of the process that starts it says.
func (d Dir) Service() daemon.Service {
	return daemon.Service{PIDFile: d.path("ouro.pid"), LogFile: d.path("ouro.log"), Env: []string{Env + "=" + string(d)}}
}

// maxSocket is the longest path a socket of the file system can have: its
// address holds 104 bytes on macOS and 108 on Linux, the byte that ends it
// included.
const maxSocket = 103

// Socket is the socket the front ends of the owner reach the node on, which
// only the user who runs it can open: ouro.sock in the directory. Where that
// path is too long for a socket, the socket is in SocketDir, named after the
// directory.
func (d Dir) Socket() string {
	if p := d.path("ouro.sock"); len(p) <= maxSocket {
		return p
	}
	sum := sha256.Sum256([]byte(filepath.Clean(string(d))))
	return filepath.Join(SocketDir(), hex.EncodeToString(sum[:8])+".sock")
}

// SocketDir is where the sockets of the nodes are whose directory is too
// deep for one: a directory of the user in the temporary directory of the
// system. Whoever binds a socket there makes sure that it is the user's
// alone.
func SocketDir() string { return filepath.Join(os.TempDir(), fmt.Sprintf("ouro-%d", os.Getuid())) }

// maxLog is the size from which the log is rotated; tests make it smaller.
var maxLog int64 = 10 << 20

// Log adds a line to the log of the node. It is for a process that is not the
// node itself, such as the terminal UI: the node writes its log through
// LogTo. A log that cannot be written is nothing to fail for.
func (d Dir) Log(format string, args ...any) {
	var line bytes.Buffer
	log.New(&line, "", log.LstdFlags).Printf(format, args...)
	d.logging(func() {
		f, err := os.OpenFile(d.Service().LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		f.Write(line.Bytes())
	})
}

// LogTo returns what writes to w, which is where the node in this directory
// logs to: ouro.log, when it runs in the background. Every write keeps the
// log from growing beyond its limit, like Log does.
func (d Dir) LogTo(w io.Writer) io.Writer { return logWriter{d, w} }

type logWriter struct {
	dir Dir
	w   io.Writer
}

func (l logWriter) Write(p []byte) (n int, err error) {
	l.dir.logging(func() { n, err = l.w.Write(p) })
	return n, err
}

// logging runs write, which adds to the log, after the log was rotated if it
// has reached its limit: then it is copied to ouro.log.1, which is replaced,
// and emptied. The node and the processes that log beside it take turns by a
// lock on ouro.log.lock, so that no line is lost between copying and
// emptying. A writer that appends to the log without the lock, such as what
// the runtime prints when the node crashes, can lose a line written in that
// moment, but never leaves a gap: the log is opened for appending, so it goes
// on at its end.
func (d Dir) logging(write func()) {
	if unlock, err := safefile.Lock(d.path("ouro.log.lock")); err == nil {
		defer unlock()
	}
	d.rotate()
	write()
}

// rotate moves the content of the log to ouro.log.1 if it has reached its
// limit.
func (d Dir) rotate() {
	path := d.Service().LogFile
	info, err := os.Stat(path)
	if err != nil || info.Size() < maxLog {
		return
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if safefile.Write(d.path("ouro.log.1"), content, 0o600) == nil {
		os.Truncate(path, 0)
	}
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
