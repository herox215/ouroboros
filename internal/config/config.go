// Package config persists the node configuration.
package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"

	"ouroboros/internal/identity"
	"ouroboros/internal/safefile"
)

const (
	fileName = "config.json"
	// lockName is the file that is locked while the configuration is changed.
	// The configuration itself cannot be: saving replaces the file.
	lockName = fileName + ".lock"
)

// Config is the persistent node configuration.
type Config struct {
	BaseURL string `json:"base_url"` // OpenAI-compatible endpoint
	APIKey  string `json:"api_key"`  // optional, e.g. not needed for a local Ollama
	Model   string `json:"model"`
	Name    string `json:"name"`
	// PrivateKey is the base64 Ed25519 key that identifies this node to peers.
	// It is generated on first use, so older configs may not have it yet.
	// The ID of the node follows from it, see ID. Older configs also stored
	// a random "id"; it is ignored and gone when the file is written again.
	PrivateKey string `json:"private_key,omitempty"`
	// Tools says which tools the agent may use, by who it is talking to.
	// Without it the owner gets every tool and peers get none.
	Tools *Tools `json:"tools,omitempty"`
}

// AllTools in a list of tools allows every tool.
const AllTools = "*"

// Tools lists the tools of the agent by name, one list per kind of
// conversation. The names are those the tools are registered with.
type Tools struct {
	// H2A (human to agent) applies when the owner asks. Missing or null
	// allows every tool.
	H2A []string `json:"h2a"`
	// A2A (agent to agent) applies when a peer asks. Missing allows none.
	A2A []string `json:"a2a"`
}

// OwnerTools are the names of the tools the agent may use for its owner.
func (c Config) OwnerTools() []string {
	if c.Tools == nil || c.Tools.H2A == nil {
		return []string{AllTools}
	}
	return c.Tools.H2A
}

// PeerTools are the names of the tools the agent may use for a peer.
func (c Config) PeerTools() []string {
	if c.Tools == nil {
		return nil
	}
	return c.Tools.A2A
}

// ID is the ID of the node: the fingerprint of its public key. A node
// without a valid key has none yet.
func (c Config) ID() string {
	id, err := identity.FromPrivate(c.PrivateKey)
	if err != nil {
		return ""
	}
	return id.ID()
}

// ErrIncomplete means that the node was not set up yet.
var ErrIncomplete = errors.New("node is not configured")

// Complete reports whether the node can be started with this configuration.
// The key is not needed for that: a node without one generates it.
func (c Config) Complete() bool {
	return c.BaseURL != "" && c.Model != "" && c.Name != ""
}

// Load reads the configuration from dir. A missing file yields an empty Config.
func Load(dir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(filepath.Join(dir, fileName))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// Save writes the configuration to dir, readable only by the owner. The file
// is replaced in one step: a crash leaves the old configuration or the new,
// never a part of one.
func Save(dir string, c Config) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err == nil {
		err = safefile.Write(filepath.Join(dir, fileName), b, 0o600)
	}
	return err
}

// Update loads the configuration from dir, lets change alter it and saves
// the result, all under a lock that keeps other processes from changing it in
// between. Nothing is written if change fails or leaves it as it was.
func Update(dir string, change func(*Config) error) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unlock, err := safefile.Lock(filepath.Join(dir, lockName))
	if err != nil {
		return err
	}
	defer unlock()
	old, err := Load(dir)
	if err != nil {
		return err
	}
	c := old
	if err := change(&c); err != nil || reflect.DeepEqual(c, old) {
		return err
	}
	return Save(dir, c)
}
