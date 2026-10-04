// Package contacts stores the peers a node trusts and the connection requests
// that are waiting for a human decision.
package contacts

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"sync"
	"syscall"
	"time"
)

// Contact is a trusted peer.
type Contact struct {
	PublicKey string    `json:"public_key"`
	Name      string    `json:"name"`
	Model     string    `json:"model,omitempty"`
	Added     time.Time `json:"added"`
}

// Request is a connection request, incoming or outgoing.
type Request struct {
	PublicKey string    `json:"public_key"`
	Name      string    `json:"name"`
	Model     string    `json:"model,omitempty"`
	Addr      string    `json:"addr,omitempty"` // host:port to call back
	At        time.Time `json:"at"`
}

// Data is the whole contact list.
type Data struct {
	Trusted  []Contact `json:"trusted,omitempty"`
	Incoming []Request `json:"incoming,omitempty"`
	Outgoing []Request `json:"outgoing,omitempty"`
	Blocked  []string  `json:"blocked,omitempty"`
}

// Store is a file-backed contact list. Several processes (the node and the UI)
// may use the same file; every change takes an exclusive file lock.
type Store struct {
	path string
	mu   sync.Mutex
}

// Open returns the store at path. The file is created on the first change.
func Open(path string) *Store { return &Store{path: path} }

// flock locks a file; tests replace it to make locking fail.
var flock = syscall.Flock

// update loads the file, applies fn and writes it back under an exclusive lock.
func (s *Store) update(fn func(*Data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer flock(int(f.Fd()), syscall.LOCK_UN)

	var d Data
	if err := json.NewDecoder(f).Decode(&d); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if err := fn(&d); err != nil {
		if errors.Is(err, errUnchanged) {
			return nil
		}
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(&d)
}

// Snapshot returns a copy of the whole contact list.
func (s *Store) Snapshot() (Data, error) {
	var d Data
	err := s.update(func(cur *Data) error {
		d = clone(*cur)
		return errUnchanged
	})
	return d, err
}

// errUnchanged makes update skip writing when only reading.
var errUnchanged = errors.New("unchanged")

// State reports whether publicKey is trusted and/or blocked.
func (s *Store) State(publicKey string) (trusted, blocked bool) {
	s.update(func(d *Data) error {
		trusted = slices.ContainsFunc(d.Trusted, func(c Contact) bool { return c.PublicKey == publicKey })
		blocked = slices.Contains(d.Blocked, publicKey)
		return errUnchanged
	})
	return trusted, blocked
}

// HadOutgoing reports whether this node asked publicKey to connect.
func (s *Store) HadOutgoing(publicKey string) bool {
	var found bool
	s.update(func(d *Data) error {
		found = slices.ContainsFunc(d.Outgoing, func(r Request) bool { return r.PublicKey == publicKey })
		return errUnchanged
	})
	return found
}

// AddIncoming records (or refreshes) a pending request from publicKey.
func (s *Store) AddIncoming(publicKey, name, model, addr string) {
	s.update(func(d *Data) error {
		if slices.Contains(d.Blocked, publicKey) || slices.ContainsFunc(d.Trusted, trusted(publicKey)) {
			return nil
		}
		r := Request{PublicKey: publicKey, Name: name, Model: model, Addr: addr, At: time.Now()}
		i := slices.IndexFunc(d.Incoming, match(publicKey))
		if i >= 0 {
			r.At = d.Incoming[i].At
			d.Incoming[i] = r
			return nil
		}
		d.Incoming = append(d.Incoming, r)
		return nil
	})
}

// AddOutgoing records that this node asked publicKey to connect.
func (s *Store) AddOutgoing(publicKey, name, model, addr string) {
	s.update(func(d *Data) error {
		if slices.ContainsFunc(d.Outgoing, match(publicKey)) {
			return nil
		}
		d.Outgoing = append(d.Outgoing, Request{PublicKey: publicKey, Name: name, Model: model, Addr: addr, At: time.Now()})
		return nil
	})
}

// AddTrusted marks publicKey as trusted and drops any pending request for it.
func (s *Store) AddTrusted(publicKey, name, model string) {
	s.update(func(d *Data) error {
		d.Incoming = slices.DeleteFunc(d.Incoming, match(publicKey))
		d.Outgoing = slices.DeleteFunc(d.Outgoing, match(publicKey))
		d.Blocked = slices.DeleteFunc(d.Blocked, func(k string) bool { return k == publicKey })
		if !slices.ContainsFunc(d.Trusted, trusted(publicKey)) {
			d.Trusted = append(d.Trusted, Contact{PublicKey: publicKey, Name: name, Model: model, Added: time.Now()})
		}
		return nil
	})
}

// RemoveOutgoing drops a pending outgoing request, e.g. after a rejection.
func (s *Store) RemoveOutgoing(publicKey string) {
	s.update(func(d *Data) error {
		d.Outgoing = slices.DeleteFunc(d.Outgoing, match(publicKey))
		return nil
	})
}

// Approve accepts a pending incoming request and returns it.
func (s *Store) Approve(publicKey string) (Request, bool) {
	var r Request
	var ok bool
	s.update(func(d *Data) error {
		i := slices.IndexFunc(d.Incoming, match(publicKey))
		if i < 0 {
			return nil
		}
		r, ok = d.Incoming[i], true
		d.Incoming = slices.Delete(d.Incoming, i, i+1)
		d.Outgoing = slices.DeleteFunc(d.Outgoing, match(publicKey))
		if !slices.ContainsFunc(d.Trusted, trusted(publicKey)) {
			d.Trusted = append(d.Trusted, Contact{PublicKey: publicKey, Name: r.Name, Model: r.Model, Added: time.Now()})
		}
		return nil
	})
	return r, ok
}

// Reject refuses a pending incoming request, blocks the peer and returns it.
func (s *Store) Reject(publicKey string) (Request, bool) {
	var r Request
	var ok bool
	s.update(func(d *Data) error {
		i := slices.IndexFunc(d.Incoming, match(publicKey))
		if i < 0 {
			return nil
		}
		r, ok = d.Incoming[i], true
		d.Incoming = slices.Delete(d.Incoming, i, i+1)
		if !slices.Contains(d.Blocked, publicKey) {
			d.Blocked = append(d.Blocked, publicKey)
		}
		return nil
	})
	return r, ok
}

func match(publicKey string) func(Request) bool {
	return func(r Request) bool { return r.PublicKey == publicKey }
}

func trusted(publicKey string) func(Contact) bool {
	return func(c Contact) bool { return c.PublicKey == publicKey }
}

func clone(d Data) Data {
	return Data{
		Trusted:  slices.Clone(d.Trusted),
		Incoming: slices.Clone(d.Incoming),
		Outgoing: slices.Clone(d.Outgoing),
		Blocked:  slices.Clone(d.Blocked),
	}
}
