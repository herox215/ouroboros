// Package credentials stores the accesses of a node: how it signs in to the
// APIs its agents talk to. They are kept in a file of their own, apart from
// the configuration, which only names the access that is in use.
//
// An access belongs to one provider, the plugin that signed in. What it holds
// is for that provider alone to read: the store keeps it and tells nobody
// what it means. Everything under Secret is a secret of the node.
package credentials

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ouroboros/internal/safefile"
)

const (
	// FileName is the file in the directory of a node that holds its accesses.
	FileName = "credentials.json"
	// LockName is the file that is locked while the accesses are changed.
	LockName = FileName + ".lock"
)

// ErrGone means there is no such access.
var ErrGone = errors.New("no such access")

// Access is one way the node signs in to a provider.
type Access struct {
	// ID names the access for good: the configuration refers to it, and
	// renaming the access does not change it.
	ID string `json:"id"`
	// Provider is the name of the provider that signed in.
	Provider string `json:"provider"`
	// Name is what the owner calls the access, "" if nothing.
	Name string `json:"name,omitempty"`
	// Rev changes whenever the owner signs in anew: the agents are built
	// anew then. What the provider renews on its own leaves it as it is.
	Rev string `json:"rev,omitempty"`
	Credentials
}

// Label is what the access is called where it is shown: the name the owner
// gave it, or what its service is commonly called.
func (a Access) Label() string { return cmp.Or(a.Name, a.Title, a.Provider) }

// Credentials are what a provider keeps of an access.
type Credentials struct {
	// Title is what the service is commonly called, like "OpenAI" or
	// "localhost", and About what the owner tells the access apart by, like
	// the endpoint or the account. Both are shown, so neither is a secret.
	Title string `json:"title,omitempty"`
	About string `json:"about,omitempty"`
	// Data is what the provider needs that is no secret, and Secret what
	// is: every text in Secret is kept out of the log.
	Data   json.RawMessage `json:"data,omitempty"`
	Secret json.RawMessage `json:"secret,omitempty"`
}

// Secrets are the texts in the credentials that must not be shown: every
// text under Secret, whatever the provider keeps there.
func (c Credentials) Secrets() []string {
	var value any
	if len(c.Secret) == 0 {
		return nil
	}
	if json.Unmarshal(c.Secret, &value) != nil {
		// What cannot be read is a secret as it stands.
		return []string{string(c.Secret)}
	}
	var texts []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			if v != "" {
				texts = append(texts, v)
			}
		case []any:
			for _, e := range v {
				walk(e)
			}
		case map[string]any:
			for _, e := range v {
				walk(e)
			}
		}
	}
	walk(value)
	slices.Sort(texts)
	return slices.Compact(texts)
}

// Store is the file-backed list of the accesses. Several processes may use
// the same file; every change takes an exclusive lock on a file next to it
// and replaces the list in one step.
type Store struct {
	path string
}

// Open returns the store that keeps the accesses in the directory dir.
func Open(dir string) *Store { return &Store{path: filepath.Join(dir, FileName)} }

// Files are the files that hold the accesses of the node in dir, which no
// tool of an agent reads: the list and the file it is locked with.
func Files(dir string) []string {
	s := Open(dir)
	return []string{s.path, s.lock()}
}

func (s *Store) lock() string { return filepath.Join(filepath.Dir(s.path), LockName) }

// file is what is written.
type file struct {
	Accesses []Access `json:"accesses"`
}

// List returns the accesses, in the order they were added.
func (s *Store) List() ([]Access, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	return f.Accesses, nil
}

// Get returns the access id, or ErrGone.
func (s *Store) Get(id string) (Access, error) {
	list, err := s.List()
	if err != nil {
		return Access{}, err
	}
	if i := index(list, id); i >= 0 {
		return list[i], nil
	}
	return Access{}, ErrGone
}

func index(list []Access, id string) int {
	return slices.IndexFunc(list, func(a Access) bool { return a.ID == id })
}

// Change lets change alter the list under the lock and writes what it
// leaves, readable only by the owner. Nothing is written if change fails.
func (s *Store) Change(change func(list []Access) ([]Access, error)) error {
	unlock, err := safefile.Lock(s.lock())
	if err != nil {
		return err
	}
	defer unlock()
	list, err := s.List()
	if err != nil {
		return err
	}
	if list, err = change(list); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(file{list}, "", "  ") // texts and JSON that was read: cannot fail
	return safefile.Write(s.path, b, 0o600)
}

// NewID returns the ID of a new access.
func NewID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Put saves a as the owner signed in: an access with the ID of a replaces
// that one and keeps its name unless a has one, another is added. An access
// without an ID gets one. Every time, the access gets a new Rev. It returns
// the access as it is saved.
func (s *Store) Put(a Access) (Access, error) {
	err := s.Change(func(list []Access) ([]Access, error) {
		if a.ID == "" {
			a.ID = NewID()
		}
		a.Rev = NewID()
		if i := index(list, a.ID); i >= 0 {
			if a.Name == "" {
				a.Name = list[i].Name
			}
			list[i] = a
			return list, nil
		}
		return append(list, a), nil
	})
	return a, err
}

// Rename calls the access id name. Its ID stays.
func (s *Store) Rename(id, name string) error {
	return s.Change(func(list []Access) ([]Access, error) {
		i := index(list, id)
		if i < 0 {
			return nil, ErrGone
		}
		list[i].Name = strings.TrimSpace(name)
		return list, nil
	})
}

// Remove drops the access id.
func (s *Store) Remove(id string) error {
	return s.Change(func(list []Access) ([]Access, error) {
		i := index(list, id)
		if i < 0 {
			return nil, ErrGone
		}
		return slices.Delete(list, i, i+1), nil
	})
}
