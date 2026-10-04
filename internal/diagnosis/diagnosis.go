// Package diagnosis keeps why answers to peers failed, so that the agent of
// the node can explain it to the peer that asks about it later.
//
// A peer whose message could not be answered is only told an error ID (see
// a2a.Server). The error itself stays in the log of the node: its text names
// endpoints and whatever else a provider or a tool put into it. What is kept
// here under the ID is the Cause, one of a fixed set of categories, together
// with the peer and the message it belongs to. Nothing else of the error is
// stored, so nothing else can reach a peer.
package diagnosis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/openai"
	"ouroboros/internal/safefile"
)

// MaxEntries is how many failed answers are kept. The oldest make room.
const MaxEntries = 256

// maxIDBytes is how much of an ID a peer chose is kept.
const maxIDBytes = 128

// Cause is the category of what went wrong with an answer.
type Cause string

const (
	// CauseLLMUnreachable: no connection to the language model.
	CauseLLMUnreachable Cause = "llm_unreachable"
	// CauseLLMAuth: the language model service refused the node's credentials.
	CauseLLMAuth Cause = "llm_auth"
	// CauseLLMRateLimited: the language model service allowed no more requests.
	CauseLLMRateLimited Cause = "llm_rate_limited"
	// CauseLLMRejected: the language model service refused the request itself.
	CauseLLMRejected Cause = "llm_rejected"
	// CauseLLMFailed: the language model service failed on its side.
	CauseLLMFailed Cause = "llm_failed"
	// CauseTimeout: the answer took too long.
	CauseTimeout Cause = "timeout"
	// CauseCancelled: the answer was stopped before it was done.
	CauseCancelled Cause = "cancelled"
	// CauseTooManyToolCalls: the agent kept using tools without an answer.
	CauseTooManyToolCalls Cause = "too_many_tool_calls"
	// CauseInternal: anything else.
	CauseInternal Cause = "internal"
)

// explanations say what a Cause means to whoever asked, in general terms.
// They are all a peer learns: keep endpoints, names and numbers out of them.
var explanations = map[Cause]string{
	CauseLLMUnreachable:   "The node could not reach the language model it answers with. The node itself was running; the message was not the problem.",
	CauseLLMAuth:          "The language model service refused the credentials of the node. Its owner has to fix the configuration before it can answer.",
	CauseLLMRateLimited:   "The language model service the node answers with allowed no more requests at that time (rate limit or quota).",
	CauseLLMRejected:      "The language model service refused the request, for example because the conversation was too long for the model or the model is not available to the node.",
	CauseLLMFailed:        "The language model service the node answers with had a failure on its side.",
	CauseTimeout:          "The answer took too long and was given up.",
	CauseCancelled:        "The answer was stopped before it was done, for example because the connection was closed or the node was stopped.",
	CauseTooManyToolCalls: "The agent used its tools too many times without arriving at an answer and gave up.",
	CauseInternal:         "An internal error of the node. Its owner finds the details in the log of the node.",
}

// Explain says what c means in general terms.
func (c Cause) Explain() string {
	if text, ok := explanations[c]; ok {
		return text
	}
	return explanations[CauseInternal]
}

// Classify returns the category of the error an answer failed with. The
// category is all that is taken from err.
func Classify(err error) Cause {
	var api *openai.APIError
	var network net.Error
	switch {
	case errors.Is(err, agent.ErrTooManyToolCalls):
		return CauseTooManyToolCalls
	case errors.Is(err, context.Canceled):
		return CauseCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return CauseTimeout
	case errors.As(err, &api):
		switch {
		case api.Unauthorized():
			return CauseLLMAuth
		case api.Status == http.StatusTooManyRequests:
			return CauseLLMRateLimited
		case api.Status >= 500:
			return CauseLLMFailed
		}
		return CauseLLMRejected
	case errors.As(err, &network):
		if network.Timeout() {
			return CauseTimeout
		}
		return CauseLLMUnreachable
	}
	return CauseInternal
}

// Entry is an answer to a peer that failed.
type Entry struct {
	ID        string    `json:"id"`         // what the peer was told
	Peer      string    `json:"peer"`       // public key of the peer that asked
	ContextID string    `json:"context_id"` // conversation the message was part of
	MessageID string    `json:"message_id"` // the message that was not answered
	At        time.Time `json:"at"`
	Cause     Cause     `json:"cause"`
}

// Store is a file-backed list of the last MaxEntries failed answers. The node
// writes it and its agent reads it; like the contacts, every access takes a
// lock on a file next to it and the list is replaced in one step.
type Store struct {
	path string
	mu   sync.Mutex
}

// Open returns the store at path. The file is created by the first Record.
func Open(path string) *Store { return &Store{path: path} }

// Record keeps e. With MaxEntries kept, the oldest is dropped for it.
func (s *Store) Record(e Entry) error {
	// The peer chose these: they are cut to a size worth keeping.
	e.ContextID, e.MessageID = clip(e.ContextID), clip(e.MessageID)
	return s.access(func(entries []Entry) ([]Entry, bool) {
		entries = append(entries, e)
		return entries[max(0, len(entries)-MaxEntries):], true
	})
}

// Lookup returns the failed answer with the given ID if it was an answer to
// peer. The failed answers of other peers are not found: an ID is no key to
// what happened to somebody else.
func (s *Store) Lookup(peer, id string) (Entry, bool, error) {
	var found Entry
	var ok bool
	err := s.access(func(entries []Entry) ([]Entry, bool) {
		i := slices.IndexFunc(entries, func(e Entry) bool { return e.ID == id && e.Peer == peer })
		if i >= 0 {
			found, ok = entries[i], true
		}
		return nil, false
	})
	return found, ok, err
}

// access loads the list and hands it to fn under an exclusive lock. If fn
// reports a change, what it returns replaces the list.
func (s *Store) access(fn func([]Entry) (changed []Entry, write bool)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := safefile.Lock(s.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	b, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var entries []Entry
	if err := json.NewDecoder(bytes.NewReader(b)).Decode(&entries); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	entries, write := fn(entries)
	if !write {
		return nil
	}
	if b, err = json.MarshalIndent(entries, "", "  "); err != nil {
		return err
	}
	return safefile.Write(s.path, append(b, '\n'), 0o600)
}

// clip cuts an ID to maxIDBytes.
func clip(id string) string {
	return id[:min(len(id), maxIDBytes)]
}
