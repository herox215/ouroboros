package memory

import (
	"errors"
	"sync"
)

// Lazy is the store of a directory that is opened when it is first used,
// see Open, and stays open until Close. An Open that failed is tried again
// with the next use.
type Lazy struct {
	dir    string
	mu     sync.Mutex
	store  *Store
	closed bool
}

// NewLazy returns the store of dir, which is not opened yet.
func NewLazy(dir string) *Lazy { return &Lazy{dir: dir} }

// Store is the store, opened if it is not yet: it fails after Close.
func (l *Lazy) Store() (*Store, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, errors.New("memory store is closed")
	}
	if l.store == nil {
		s, err := Open(l.dir)
		if err != nil {
			return nil, err
		}
		l.store = s
	}
	return l.store, nil
}

// Close closes the store if it was opened. What uses it still fails.
func (l *Lazy) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	if l.store != nil {
		l.store.Close()
	}
}
