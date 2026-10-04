// Package proposal stores the changes of the configuration the agent of the
// owner proposed. A proposal changes nothing: it waits for the owner, who
// accepts or refuses it. What an agent may do and what a peer may reach is
// trust, and trust comes only from the decision of a human.
package proposal

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"slices"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/safefile"
)

const (
	// Max is how many proposals wait for a decision at most. Beyond it a
	// new one is not recorded: an agent does not fill the file.
	Max = 16
	// MaxReason is how much of the reason of a proposal is kept, in bytes.
	MaxReason = 500
)

var (
	// ErrFull is the error of Add when Max proposals are waiting.
	ErrFull = errors.New("too many proposals are waiting for a decision")
	// ErrGone means there is no such proposal to decide on.
	ErrGone = errors.New("no such proposal")
	// ErrStale is the error of Accept for a proposal that was made for a
	// configuration that is no longer the one of the node: the field was
	// changed since. It is not applied, and it is dropped.
	ErrStale = errors.New("the configuration was changed since this was proposed: it was not applied")
)

// Proposal is one change of the configuration that waits for the owner.
type Proposal struct {
	ID string `json:"id"`
	// Path names the field, as config.Field takes it.
	Path string `json:"path"`
	// Value is what the field is to be, and Before what it was when the
	// change was proposed, both as config.Field returns them.
	Value  json.RawMessage `json:"value"`
	Before json.RawMessage `json:"before"`
	// Reason is what the agent said the change is for. It is the text of an
	// agent: nothing is decided by it.
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// Store is a file-backed list of proposals. Several processes may use the
// same file; every change takes an exclusive lock on a file next to it, and
// replaces the list in one step.
type Store struct {
	path string
}

// Open returns the store at path. The file is created on the first change.
func Open(path string) *Store { return &Store{path: path} }

// errUnchanged makes update skip writing when only reading.
var errUnchanged = errors.New("unchanged")

// update loads the list, applies fn and writes it back, under the lock.
func (s *Store) update(fn func(list *[]Proposal) error) error {
	unlock, err := safefile.Lock(s.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	b, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var list []Proposal
	if len(b) > 0 {
		if err := json.Unmarshal(b, &list); err != nil {
			return err
		}
	}
	// The file is written for a human to read; values are compared as
	// config.Field returns them.
	for i := range list {
		list[i].Value, list[i].Before = compact(list[i].Value), compact(list[i].Before)
	}
	if err := fn(&list); err != nil {
		if errors.Is(err, errUnchanged) {
			return nil
		}
		return err
	}
	b, _ = json.MarshalIndent(list, "", "  ") // values that were read as JSON: cannot fail
	return safefile.Write(s.path, append(b, '\n'), 0o600)
}

// List returns the proposals that wait, the oldest first.
func (s *Store) List() ([]Proposal, error) {
	var list []Proposal
	err := s.update(func(cur *[]Proposal) error {
		list = slices.Clone(*cur)
		return errUnchanged
	})
	return list, err
}

// Add records that path is proposed to become value, for reason, on the
// configuration cfg: the value is set on a copy of it, so one the field does
// not take is an error and no proposal. What is the value already is none
// either. The same change is not recorded twice; with Max proposals waiting
// a new one fails with ErrFull.
func (s *Store) Add(cfg config.Config, path string, value json.RawMessage, reason string) (Proposal, error) {
	before, err := cfg.Field(path)
	if err != nil {
		return Proposal{}, err
	}
	if err := cfg.SetField(path, value); err != nil {
		return Proposal{}, err
	}
	after, _ := cfg.Field(path) // the path was just read
	if bytes.Equal(before, after) {
		return Proposal{}, errors.New(path + " is that already: there is nothing to change")
	}
	if len(reason) > MaxReason {
		reason = reason[:MaxReason]
	}
	p := Proposal{ID: newID(), Path: path, Value: after, Before: before, Reason: reason, At: time.Now()}
	err = s.update(func(list *[]Proposal) error {
		same := func(q Proposal) bool { return q.Path == path && bytes.Equal(q.Value, after) }
		if i := slices.IndexFunc(*list, same); i >= 0 {
			p = (*list)[i]
			return errUnchanged
		}
		if len(*list) >= Max {
			return ErrFull
		}
		*list = append(*list, p)
		return nil
	})
	if err != nil {
		return Proposal{}, err
	}
	return p, nil
}

// Accept applies the proposal id to the configuration in dir and drops it.
// A proposal whose field is no longer what it was when the change was
// proposed is dropped without being applied: ErrStale. If the configuration
// cannot be written, nothing has changed and the proposal still waits.
func (s *Store) Accept(dir, id string) (Proposal, error) {
	var p Proposal
	stale := false
	err := s.update(func(list *[]Proposal) error {
		i := slices.IndexFunc(*list, func(q Proposal) bool { return q.ID == id })
		if i < 0 {
			return ErrGone
		}
		p = (*list)[i]
		err := config.Update(dir, func(c *config.Config) error {
			now, err := c.Field(p.Path)
			if err != nil || !bytes.Equal(now, p.Before) {
				stale = true
				return nil
			}
			return c.SetField(p.Path, p.Value)
		})
		if err != nil {
			return err
		}
		*list = slices.Delete(*list, i, i+1)
		return nil
	})
	if err == nil && stale {
		err = ErrStale
	}
	return p, err
}

// Refuse drops the proposal id.
func (s *Store) Refuse(id string) (Proposal, error) {
	var p Proposal
	err := s.update(func(list *[]Proposal) error {
		i := slices.IndexFunc(*list, func(q Proposal) bool { return q.ID == id })
		if i < 0 {
			return ErrGone
		}
		p = (*list)[i]
		*list = slices.Delete(*list, i, i+1)
		return nil
	})
	return p, err
}

// compact returns the JSON value raw without the white space it was written
// with.
func compact(raw json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	json.Compact(&b, raw) // a value that was just read as JSON: cannot fail
	return b.Bytes()
}

// newID returns what tells one proposal from the others.
func newID() string {
	b := make([]byte, 6)
	rand.Read(b) // never fails
	return hex.EncodeToString(b)
}
