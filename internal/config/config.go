// Package config persists the node configuration.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const fileName = "config.json"

// Config is the persistent node configuration.
type Config struct {
	BaseURL string `json:"base_url"` // OpenAI-compatible endpoint
	APIKey  string `json:"api_key"`  // optional, e.g. not needed for a local Ollama
	Model   string `json:"model"`
	Name    string `json:"name"`
	ID      string `json:"id"`
	// PrivateKey is the base64 Ed25519 key that identifies this node to peers.
	// It is generated on first use, so older configs may not have it yet.
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

// ErrIncomplete means that the node was not set up yet.
var ErrIncomplete = errors.New("node is not configured")

// Complete reports whether the node can be started with this configuration.
func (c Config) Complete() bool {
	return c.BaseURL != "" && c.Model != "" && c.Name != "" && c.ID != ""
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

// Save writes the configuration to dir, readable only by the owner.
func Save(dir string, c Config) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, fileName)
	b, err := json.MarshalIndent(c, "", "  ")
	if err == nil {
		err = os.WriteFile(path, b, 0o600)
	}
	if err == nil {
		err = os.Chmod(path, 0o600) // WriteFile keeps the mode of existing files
	}
	return err
}

// NewID returns a short random node ID (6 hex chars).
func NewID() string {
	b := make([]byte, 3)
	rand.Read(b)
	return hex.EncodeToString(b)
}
