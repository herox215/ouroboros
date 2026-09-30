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
	APIKey string `json:"api_key"`
	Model  string `json:"model"`
	Name   string `json:"name"`
	ID     string `json:"id"`
}

// Complete reports whether the node can be started with this configuration.
func (c Config) Complete() bool {
	return c.APIKey != "" && c.Model != "" && c.Name != "" && c.ID != ""
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
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600) // WriteFile keeps the mode of existing files
}

// NewID returns a short random node ID (6 hex chars).
func NewID() string {
	b := make([]byte, 3)
	rand.Read(b)
	return hex.EncodeToString(b)
}
