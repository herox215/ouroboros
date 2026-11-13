// Package web holds what the page of the thread on the network needs to let
// the owner in: the hash of a password, the sessions of a browser that signed
// in, and how often a login may fail before it is held up.
//
// Nothing of it is ever written in the clear: the configuration keeps the hash
// and no password, and the token of a session lives in the memory of the node
// alone, so that a node that starts anew asks again.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// MinPassword is the shortest password the owner can set for the page: what a
// login form on a phone is typed with, and what guesses at a port in the
// network have to work through.
const MinPassword = 8

// What the hash of a password is made with. The parameters are written into
// the hash itself, so that they can be raised later without a migration: a
// hash that says how it was made is checked with what it says.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

// ErrPassword is why a password cannot be set.
var ErrPassword = errors.New("the password is too short")

// Hash makes the password one that can be kept: an argon2id hash in the form
// "argon2id$v=19$m=65536,t=3,p=2$<salt>$<key>", as PHC strings write it.
func Hash(password string) (string, error) {
	if len(password) < MinPassword {
		return "", fmt.Errorf("%w: at least %d characters", ErrPassword, MinPassword)
	}
	if strings.ContainsAny(password, "\x00") {
		return "", fmt.Errorf("%w: it has a null byte", ErrPassword)
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("the salt of the password: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify reports whether password is the one encoded holds. A malformed
// encoding verifies nothing, and neither does a password of one that is
// empty: the page cannot be entered by a configuration that has no hash.
func Verify(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	if parts[1] != "v="+strconv.Itoa(argon2.Version) {
		return false
	}
	var memory uint32
	var timeCost, threads uint8
	for _, field := range strings.Split(parts[2], ",") {
		name, value, ok := strings.Cut(field, "=")
		if !ok {
			return false
		}
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return false
		}
		switch name {
		case "m":
			memory = uint32(n)
		case "t":
			timeCost = uint8(n)
		case "p":
			threads = uint8(n)
		default:
			return false
		}
	}
	// The parameters come from the file the owner keeps: bound them so that a
	// damaged one costs seconds, not the machine.
	if memory < 8 || memory > 1<<20 || timeCost < 1 || timeCost > 16 || threads < 1 || threads > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(salt) < 8 {
		return false
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(key) == 0 || len(key) > 128 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(timeCost), memory, threads, uint32(len(key)))
	return subtle.ConstantTimeCompare(got, key) == 1
}

// SessionTTL is how long a browser that signed in stays signed in.
const SessionTTL = 30 * 24 * time.Hour

// Sessions are the browsers that signed in to the page: a token they carry in
// a cookie, and until when it holds. They die with the process of the node,
// which is what a rebuild does.
type Sessions struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	expires map[string]time.Time // token: when it no longer holds
	all     int                  // how many were issued, to prune
}

// NewSessions returns the sessions of a node, each holding ttl.
func NewSessions(ttl time.Duration) *Sessions {
	return &Sessions{ttl: ttl, now: time.Now, expires: map[string]time.Time{}}
}

// Token returns a name nothing guesses: 32 bytes of randomness, as 43
// characters of the URL alphabet.
func Token() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("a random name: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Issue starts a session and returns its token, to be kept in a cookie.
func (s *Sessions) Issue() (string, error) {
	token, err := Token()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.expires[token] = s.now().Add(s.ttl)
	s.all++
	return token, nil
}

// Valid reports whether token is one of a session that holds. The session is
// not extended by it: it ends at the same time however much it is used.
func (s *Sessions) Valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.expires[token]
	if !ok {
		return false
	}
	if s.now().After(until) {
		delete(s.expires, token)
		return false
	}
	return true
}

// Revoke ends the session of token, which may be none.
func (s *Sessions) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.expires, token)
}

// RevokeAll ends every session: what turning the page off does, so that no
// browser stays signed in to a page that is no longer served.
func (s *Sessions) RevokeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.expires)
}

// pruneLocked forgets the sessions that ended, keeping the map small.
func (s *Sessions) pruneLocked() {
	if len(s.expires) < 64 {
		return
	}
	now := s.now()
	for token, until := range s.expires {
		if now.After(until) {
			delete(s.expires, token)
		}
	}
}

// Throttle holds a login up that fails over and over: a port in a network is
// found by a scanner, and a password guessed by one. It counts the failures of
// one key, the address of the caller, and blocks it after a few.
type Throttle struct {
	mu       sync.Mutex
	now      func() time.Time
	byKey    map[string]*attempt
	limit    int           // failures in a row before the first block
	block    time.Duration // the first block; each more failure doubles it
	blockMax time.Duration
}

// NewThrottle returns a throttle that blocks after limit failures in a row,
// for block, doubling up to blockMax with every further failure.
func NewThrottle(limit int, block, blockMax time.Duration) *Throttle {
	return &Throttle{now: time.Now, byKey: map[string]*attempt{}, limit: limit, block: block, blockMax: blockMax}
}

// attempt is what one key did.
type attempt struct {
	failures int       // in a row, since the last success
	until    time.Time // blocked until, zero if not
}

// Allowed reports whether key may try a password now.
func (t *Throttle) Allowed(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	a, ok := t.byKey[key]
	if !ok {
		return true
	}
	if a.until.IsZero() || t.now().After(a.until) {
		return true
	}
	_ = ok
	return false
}

// Failed counts one failed attempt of key, and blocks it once the failures in
// a row reach the limit. It returns how long key is blocked for, zero while it
// is not.
func (t *Throttle) Failed(key string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked()
	a, ok := t.byKey[key]
	if !ok {
		a = &attempt{}
		t.byKey[key] = a
	}
	a.failures++
	if a.failures < t.limit {
		return 0
	}
	// The block doubles with every failure past the limit, up to blockMax.
	steps := a.failures - t.limit
	wait := t.block
	for range steps {
		if wait >= t.blockMax {
			break
		}
		wait *= 2
	}
	if wait > t.blockMax {
		wait = t.blockMax
	}
	a.until = t.now().Add(wait)
	return wait
}

// Cleared forgets the failures of key, after it signed in.
func (t *Throttle) Cleared(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.byKey, key)
}

// pruneLocked forgets the keys that are blocked no longer for a while.
func (t *Throttle) pruneLocked() {
	if len(t.byKey) < 256 {
		return
	}
	now := t.now()
	for key, a := range t.byKey {
		if a.until.IsZero() || now.After(a.until.Add(t.blockMax)) {
			delete(t.byKey, key)
		}
	}
}
