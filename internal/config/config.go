// Package config persists the node configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// Tools says which tools the agent may use, by who it is talking to, and
	// how far they go. Without it the owner gets every tool and peers get none.
	Tools *Tools `json:"tools,omitempty"`
}

// AllTools in a list of tools allows every tool.
const AllTools = "*"

// Tools says what the agent may do, one list of tools per kind of
// conversation. The names are those the tools are registered with.
type Tools struct {
	// H2A (human to agent) applies when the owner asks. Missing or null
	// allows every tool.
	H2A []Entry `json:"h2a"`
	// A2A (agent to agent) applies when a peer asks. Missing allows none.
	A2A []Entry `json:"a2a"`
	// Protected are paths no tool for files touches, for anybody. Missing (a
	// configuration from before there were such tools) stands for
	// DefaultProtected; an empty list is the owner's decision to protect
	// nothing.
	Protected *[]string `json:"protected,omitempty"`
}

// Entry is one tool in a list of Tools: its name alone, or an object with
// the name as "tool" and settings for that tool next to it.
//
//	"read"
//	{"tool": "read", "allow": ["~/share"]}
type Entry struct {
	Tool string // the name of the tool, or AllTools
	// Settings are the settings of the tool as a JSON object, nil for an
	// entry that is a name alone. What they mean is for the tool to say.
	Settings json.RawMessage
}

// Named returns the entries for tools that are listed by their name alone.
func Named(names ...string) []Entry {
	entries := make([]Entry, len(names))
	for i, name := range names {
		entries[i] = Entry{Tool: name}
	}
	return entries
}

// entryTool is the key that names the tool in an entry with settings.
const entryTool = "tool"

// UnmarshalJSON reads a name or an object with settings.
func (e *Entry) UnmarshalJSON(b []byte) error {
	if json.Unmarshal(b, &e.Tool) == nil {
		e.Settings = nil
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(b, &object); err != nil {
		return errors.New(`a tool is a name or an object like {"tool": name, …}`)
	}
	if json.Unmarshal(object[entryTool], &e.Tool) != nil || e.Tool == "" {
		return errors.New(`a tool with settings needs its name as "tool"`)
	}
	delete(object, entryTool)
	e.Settings, _ = json.Marshal(object) // values that were just read: cannot fail
	return nil
}

// MarshalJSON writes the entry the way it is read.
func (e Entry) MarshalJSON() ([]byte, error) {
	if e.Settings == nil {
		return json.Marshal(e.Tool)
	}
	object := map[string]any{}
	if err := json.Unmarshal(e.Settings, &object); err != nil {
		return nil, fmt.Errorf("settings of the tool %s: %w", e.Tool, err)
	}
	object[entryTool] = e.Tool
	return json.Marshal(object)
}

// DefaultProtected are the paths no tool for files touches unless the owner
// says otherwise: the usual places of keys and credentials, and nothing
// else. "~" is the home directory of the user.
func DefaultProtected() []string {
	return []string{
		"~/.ssh", "~/.gnupg", "~/.aws", "~/.kube", "~/.config/gcloud",
		"~/.docker/config.json", "~/.netrc", "~/.password-store", "~/.local/share/keyrings",
	}
}

// DefaultTools is what the setup writes: every tool for the owner, none for
// peers, and the paths that are protected, for the owner to see and change.
func DefaultTools() *Tools {
	protected := DefaultProtected()
	return &Tools{H2A: Named(AllTools), A2A: []Entry{}, Protected: &protected}
}

// OwnerTools are the tools the agent may use for its owner.
func (c Config) OwnerTools() []Entry {
	if c.Tools == nil || c.Tools.H2A == nil {
		return Named(AllTools)
	}
	return c.Tools.H2A
}

// PeerTools are the tools the agent may use for a peer.
func (c Config) PeerTools() []Entry {
	if c.Tools == nil {
		return nil
	}
	return c.Tools.A2A
}

// Protected are the paths no tool for files touches.
func (c Config) Protected() []string {
	if c.Tools == nil || c.Tools.Protected == nil {
		return DefaultProtected()
	}
	return *c.Tools.Protected
}

// Files are the files in dir that hold the secrets of the node: the
// configuration, with the key of the node and the key of the API, and the
// file that is locked while it is changed.
func Files(dir string) []string {
	return []string{filepath.Join(dir, fileName), filepath.Join(dir, lockName)}
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
