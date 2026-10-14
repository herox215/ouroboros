// Package suggestion stores the changes of ouro that the reviews of answers
// suggested, see assistant.Signal. A suggestion changes nothing: it waits for
// the owner, who takes it, and then the agent makes it, or drops it.
package suggestion

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"ouroboros/internal/safefile"
)

const (
	// Max is how many suggestions wait for the owner at most. Beyond it a
	// new one is not recorded: the reviews do not fill the file.
	Max = 16
	// MaxText is how much of the text of a suggestion is kept, in bytes.
	MaxText = 1000
)

var (
	// ErrFull is the error of Add when Max suggestions are waiting.
	ErrFull = errors.New("too many suggestions are waiting for the owner")
	// ErrGone means there is no such suggestion.
	ErrGone = errors.New("no such suggestion")
)

// Suggestion is one change of ouro a review suggested.
type Suggestion struct {
	ID string `json:"id"`
	// Text is what the review would change, and why. It is the text of an
	// LLM: nothing is decided by it.
	Text string `json:"text"`
	// Signals are what the answer that was reviewed was measured to have
	// done badly, as the log says them.
	Signals []string `json:"signals,omitempty"`
	// Session is the session of the owner the answer was in.
	Session string    `json:"session,omitempty"`
	At      time.Time `json:"at"`
}

// Store is a file-backed list of suggestions. Several processes may use the
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
func (s *Store) update(fn func(list *[]Suggestion) error) error {
	unlock, err := safefile.Lock(s.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	b, err := os.ReadFile(s.path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	var list []Suggestion
	if len(b) > 0 {
		if err := json.Unmarshal(b, &list); err != nil {
			return err
		}
	}
	if err := fn(&list); err != nil {
		if errors.Is(err, errUnchanged) {
			return nil
		}
		return err
	}
	b, _ = json.MarshalIndent(list, "", "  ") // strings and times: cannot fail
	return safefile.Write(s.path, append(b, '\n'), 0o600)
}

// List returns the suggestions that wait, the oldest first.
func (s *Store) List() ([]Suggestion, error) {
	var list []Suggestion
	err := s.update(func(cur *[]Suggestion) error {
		list = slices.Clone(*cur)
		return errUnchanged
	})
	return list, err
}

// Add records text as a suggestion of the review of an answer in session,
// which had signals. The same text is not recorded twice: Add returns the one
// that waits. With Max suggestions waiting a new one fails with ErrFull.
func (s *Store) Add(text string, signals []string, session string) (Suggestion, error) {
	if len(text) > MaxText {
		text = strings.ToValidUTF8(text[:MaxText], "")
	}
	n := Suggestion{ID: newID(), Text: text, Signals: signals, Session: session, At: time.Now()}
	err := s.update(func(list *[]Suggestion) error {
		if i := slices.IndexFunc(*list, func(w Suggestion) bool { return w.Text == text }); i >= 0 {
			n = (*list)[i]
			return errUnchanged
		}
		if len(*list) >= Max {
			return ErrFull
		}
		*list = append(*list, n)
		return nil
	})
	if err != nil {
		return Suggestion{}, err
	}
	return n, nil
}

// Remove drops the suggestion id and returns it: the owner took it, or not.
func (s *Store) Remove(id string) (Suggestion, error) {
	var n Suggestion
	err := s.update(func(list *[]Suggestion) error {
		i := slices.IndexFunc(*list, func(w Suggestion) bool { return w.ID == id })
		if i < 0 {
			return ErrGone
		}
		n = (*list)[i]
		*list = slices.Delete(*list, i, i+1)
		return nil
	})
	return n, err
}

// newID returns what tells one suggestion from the others.
func newID() string {
	b := make([]byte, 6)
	rand.Read(b) // never fails
	return hex.EncodeToString(b)
}
