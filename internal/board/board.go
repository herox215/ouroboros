// Package board keeps the tickets of a profile: the work the thread put on
// the board rather than hand it to a session. A ticket goes from New through
// Ready and Progress to Done, with Blocked beside the way, and only a session
// the orchestrator started for it works on it. One process changes the board,
// the node; others read it with Read.
package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/safefile"
)

// The columns of the board.
const (
	// New holds what the thread put on the board, until a session refined it.
	New = "new"
	// Ready holds what is refined and can be implemented.
	Ready = "ready"
	// Progress holds what a session implements.
	Progress = "progress"
	// Blocked holds what does not go on by itself, with the reason: a
	// question for the owner, or a session that ended without a result.
	Blocked = "blocked"
	// Done holds what is implemented, or dropped.
	Done = "done"
)

// Columns are the columns in the order the board shows them.
var Columns = []string{New, Ready, Progress, Blocked, Done}

// Who changes the board, besides a session, see BySession.
const (
	Thread       = "thread"
	Orchestrator = "orchestrator"
	// Code is the board itself, where no agent decided: a session that ended
	// without moving its ticket, or a node that started anew.
	Code = "board"
)

// The roles of a session that works on a ticket.
const (
	// Refinement fills a ticket of New in and moves it to Ready.
	Refinement = "refinement"
	// Implementation does a ticket of Progress and moves it to Done.
	Implementation = "implementation"
)

// The limits of the board, in bytes where they are of a text.
const (
	MaxTickets = 128
	MaxTitle   = 200
	MaxText    = 16 << 10
	MaxNote    = 4 << 10
	// MaxNotes is how many notes a ticket keeps, the latest.
	MaxNotes = 100
	// MaxRuns is how many sessions may work on a ticket one after the
	// other: one that still is not done then stays where it is.
	MaxRuns = 12
)

var (
	// ErrNotFound means that the board has no ticket of that number.
	ErrNotFound = errors.New("no such ticket")
	// ErrFull means that the board holds MaxTickets tickets none of which is done.
	ErrFull = fmt.Errorf("the board holds %d tickets that are not done: no more are taken", MaxTickets)
)

// Ticket is a piece of work on the board. Store manages all of it: a change
// goes through one of its methods.
type Ticket struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Text      string `json:"text"`
	Directory string `json:"directory,omitempty"`
	Column    string `json:"column"`
	// Reason is why a ticket is in Blocked, and From the column it came
	// from; both empty elsewhere.
	Reason string `json:"reason,omitempty"`
	From   string `json:"from,omitempty"`
	// Result is what the session that implemented it says it did.
	Result string `json:"result,omitempty"`
	// Told says that the thread was told of the column the ticket is in.
	Told bool `json:"told,omitempty"`
	// Change is what Snapshot.Changes counted when another than the
	// orchestrator changed the ticket last: more than the orchestrator saw
	// of them, see Store.Pending, for a ticket it has yet to look at.
	Change  uint64    `json:"change,omitempty"`
	Created time.Time `json:"created"`
	Changed time.Time `json:"changed"`
	Notes   []Note    `json:"notes,omitempty"`
	Runs    []Run     `json:"runs,omitempty"`
}

// Note is something said at a ticket, or something that happened to it: By
// is who, see Thread and BySession.
type Note struct {
	At   time.Time `json:"at"`
	By   string    `json:"by"`
	Text string    `json:"text"`
}

// Run is a session that works on a ticket, or did: Ended is zero while it
// does, and Outcome then says how it left the ticket. Agent is the agent of
// the profile it runs as, see agents, and AgentName what it was called when
// the session began: both "" for a session from before the orchestrator
// chose one, which ran as the agent of the thread.
type Run struct {
	Session   string    `json:"session"`
	Role      string    `json:"role"`
	Agent     string    `json:"agent,omitempty"`
	AgentName string    `json:"agent_name,omitempty"`
	Began     time.Time `json:"began"`
	Ended     time.Time `json:"ended,omitzero"`
	Outcome   string    `json:"outcome,omitempty"`
}

// BySession is who the session id is, as By of a Note names it.
func BySession(id string) string { return "session " + id }

// Active is the run of the session that works on t now, and whether one does.
func (t Ticket) Active() (Run, bool) {
	if n := len(t.Runs); n > 0 && t.Runs[n-1].Ended.IsZero() {
		return t.Runs[n-1], true
	}
	return Run{}, false
}

// Role is what a session started for t now would do, as its column says:
// an error for a column no session starts in.
func (t Ticket) Role() (string, error) {
	switch t.Column {
	case New:
		return Refinement, nil
	case Ready:
		return Implementation, nil
	}
	return "", fmt.Errorf("ticket %d is in %s: a session starts for a ticket in %s, to refine it, or in %s, to implement it", t.ID, t.Column, New, Ready)
}

// Look is one time the orchestrator looked at the board: the session it did
// so in, and what it says it did.
type Look struct {
	At      time.Time `json:"at"`
	Session string    `json:"session"`
	Result  string    `json:"result,omitempty"`
}

// MaxLooks is how many looks of the orchestrator the board keeps, the latest.
const MaxLooks = 10

// Snapshot is the board as it was read: its tickets by number; Changes,
// which counts what others than the orchestrator changed, and Seen, how many
// of them the orchestrator looked at; and its last looks.
type Snapshot struct {
	Next    int      `json:"next"`
	Changes uint64   `json:"changes"`
	Seen    uint64   `json:"seen"`
	Tickets []Ticket `json:"tickets"`
	Looks   []Look   `json:"looks,omitempty"`
}

// Read reads the board kept at path, for a process that does not change it:
// a missing file is an empty board.
func Read(path string) (Snapshot, error) {
	var s Snapshot
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("the board %s cannot be read: %w", path, err)
	}
	return s, nil
}

// Store is the board of the process that changes it. Every change is written
// before it holds: one that cannot be written did not happen.
type Store struct {
	mu   sync.Mutex
	path string
	data Snapshot
}

// Open reads the board kept at path for the node, which starts with it: a
// session that worked on a ticket when the node before stopped is gone, so
// its ticket is blocked, see interrupted. A file that cannot be read is an
// error, and never overwritten.
func Open(path string, now time.Time) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	data, err := Read(path)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, data: data}
	return s, s.change(Code, func(d *Snapshot) (bool, error) {
		changed := false
		for i := range d.Tickets {
			t := &d.Tickets[i]
			if run, ok := t.Active(); ok {
				block(t, "interrupted", "The node started anew while session "+run.Session+" worked on it, in its "+run.Role+".", Code, now)
				t.Change, changed = d.Changes+1, true
			}
		}
		return changed, nil
	})
}

// clone is t with notes and runs of its own.
func clone(t Ticket) Ticket {
	t.Notes, t.Runs = slices.Clone(t.Notes), slices.Clone(t.Runs)
	return t
}

// change makes the change fn on a copy of the board and keeps it once it is
// written, if fn says that it changed something. What by changed counts for
// the orchestrator to look, unless it is the orchestrator itself.
func (s *Store) change(by string, fn func(*Snapshot) (bool, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.data
	next.Tickets = make([]Ticket, len(s.data.Tickets))
	for i, t := range s.data.Tickets {
		next.Tickets[i] = clone(t)
	}
	changed, err := fn(&next)
	if err != nil || !changed {
		return err
	}
	if by != Orchestrator {
		next.Changes++
	}
	out, _ := json.MarshalIndent(next, "", "  ") // of strings, numbers and times only: cannot fail
	if err := safefile.Write(s.path, append(out, '\n'), 0o600); err != nil {
		return err
	}
	s.data = next
	return nil
}

// on makes the change fn on the ticket id, as by, and returns the ticket as
// it is then.
func (s *Store) on(id int, by string, fn func(*Ticket) error) (Ticket, error) {
	var out Ticket
	err := s.change(by, func(d *Snapshot) (bool, error) {
		at := slices.IndexFunc(d.Tickets, func(t Ticket) bool { return t.ID == id })
		if at < 0 {
			return false, fmt.Errorf("%w %d", ErrNotFound, id)
		}
		if err := fn(&d.Tickets[at]); err != nil {
			return false, err
		}
		if by != Orchestrator {
			d.Tickets[at].Change = d.Changes + 1 // what change counts next
		}
		out = clone(d.Tickets[at])
		return true, nil
	})
	return out, err
}

// Pending returns how often others than the orchestrator changed the board,
// and how many of those changes the orchestrator looked at: where they
// differ, there is something for it to look at.
func (s *Store) Pending() (changes, seen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Changes, s.data.Seen
}

// Looked records that the orchestrator looked at the board in session, with
// result, and saw seen of its changes, see Pending. It returns the sessions
// of the looks the board no longer keeps for it.
func (s *Store) Looked(seen uint64, session, result string, now time.Time) ([]string, error) {
	var dropped []string
	err := s.change(Orchestrator, func(d *Snapshot) (bool, error) {
		d.Seen = seen
		d.Looks = append(slices.Clone(d.Looks), Look{At: now, Session: session, Result: result})
		for len(d.Looks) > MaxLooks {
			dropped = append(dropped, d.Looks[0].Session)
			d.Looks = d.Looks[1:]
		}
		return true, nil
	})
	return dropped, err
}

// Looks returns the last looks of the orchestrator, the latest last.
func (s *Store) Looks() []Look {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.data.Looks)
}

// List returns the tickets by number, with notes and runs of their own.
func (s *Store) List() []Ticket {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Ticket, len(s.data.Tickets))
	for i, t := range s.data.Tickets {
		out[i] = clone(t)
	}
	return out
}

// Get returns the ticket id.
func (s *Store) Get(id int) (Ticket, error) {
	for _, t := range s.List() {
		if t.ID == id {
			return t, nil
		}
	}
	return Ticket{}, fmt.Errorf("%w %d", ErrNotFound, id)
}

// Of returns the ticket the session works on now, and whether it does on one.
func (s *Store) Of(session string) (Ticket, bool) {
	for _, t := range s.List() {
		if run, ok := t.Active(); ok && run.Session == session {
			return t, true
		}
	}
	return Ticket{}, false
}

// Knows reports whether the session works on a ticket, or did, or is one
// the orchestrator looked at the board in.
func (s *Store) Knows(session string) bool {
	looked := slices.ContainsFunc(s.Looks(), func(l Look) bool { return l.Session == session })
	worked := slices.ContainsFunc(s.List(), func(t Ticket) bool {
		return slices.ContainsFunc(t.Runs, func(r Run) bool { return r.Session == session })
	})
	return looked || worked
}

// text is value without the space around it, which has to be something of
// at most max bytes: what is the name of what it is.
func text(what, value string, max int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > max {
		return "", fmt.Errorf("%s must be nonempty and at most %d bytes", what, max)
	}
	return value, nil
}

// note says text at t, as by.
func note(t *Ticket, by, text string, now time.Time) {
	t.Notes = append(t.Notes, Note{At: now, By: by, Text: text})
	if over := len(t.Notes) - MaxNotes; over > 0 {
		t.Notes = slices.Delete(t.Notes, 0, over)
	}
	t.Changed = now
}

// move puts t into column and says so, as by: the thread was not told of
// that yet, and what blocked it no longer does.
func move(t *Ticket, column, by string, now time.Time) {
	note(t, by, "moved from "+t.Column+" to "+column, now)
	t.Column, t.Reason, t.From, t.Told = column, "", "", false
}

// end ends the run of the session that works on t, with outcome.
func end(t *Ticket, outcome string, now time.Time) {
	run := &t.Runs[len(t.Runs)-1]
	run.Ended, run.Outcome = now, outcome
}

// block ends the run of the session that works on t, if one does, with
// outcome, and puts t into Blocked for reason, as by.
func block(t *Ticket, outcome, reason, by string, now time.Time) {
	if _, ok := t.Active(); ok {
		end(t, outcome, now)
	}
	from := t.Column
	move(t, Blocked, by, now)
	t.Reason, t.From = reason, from
}

// Create puts a ticket into New, as the thread does: with a title, what is
// to be done, and the project directory it is to be done in, "" for none
// yet. A full board drops the ticket that is done longest for it.
func (s *Store) Create(title, body, directory string, now time.Time) (Ticket, error) {
	title, err := text("title", title, MaxTitle)
	if err != nil {
		return Ticket{}, err
	}
	if body, err = text("text", body, MaxText); err != nil {
		return Ticket{}, err
	}
	var out Ticket
	err = s.change(Thread, func(d *Snapshot) (bool, error) {
		if len(d.Tickets) >= MaxTickets {
			oldest := -1
			for i, t := range d.Tickets {
				if t.Column == Done && (oldest < 0 || t.Changed.Before(d.Tickets[oldest].Changed)) {
					oldest = i
				}
			}
			if oldest < 0 {
				return false, ErrFull
			}
			d.Tickets = slices.Delete(d.Tickets, oldest, oldest+1)
		}
		d.Next++
		out = Ticket{ID: d.Next, Title: title, Text: body, Directory: strings.TrimSpace(directory), Column: New, Change: d.Changes + 1, Created: now, Changed: now}
		d.Tickets = append(d.Tickets, out)
		return true, nil
	})
	return out, err
}

// Comment says body at the ticket id, as by.
func (s *Store) Comment(id int, by, body string, now time.Time) (Ticket, error) {
	body, err := text("text", body, MaxNote)
	if err != nil {
		return Ticket{}, err
	}
	return s.on(id, by, func(t *Ticket) error {
		note(t, by, body, now)
		return nil
	})
}

// Edit gives the ticket id another title, text or directory, as by: nil
// leaves one as it is.
func (s *Store) Edit(id int, by string, title, body, directory *string, now time.Time) (Ticket, error) {
	if title == nil && body == nil && directory == nil {
		return Ticket{}, errors.New("nothing to change: give title, text or directory")
	}
	return s.on(id, by, func(t *Ticket) error {
		var changed []string
		if title != nil {
			value, err := text("title", *title, MaxTitle)
			if err != nil {
				return err
			}
			t.Title, changed = value, append(changed, "title")
		}
		if body != nil {
			value, err := text("text", *body, MaxText)
			if err != nil {
				return err
			}
			t.Text, changed = value, append(changed, "text")
		}
		if directory != nil {
			t.Directory, changed = strings.TrimSpace(*directory), append(changed, "directory")
		}
		note(t, by, "changed the "+strings.Join(changed, ", "), now)
		return nil
	})
}

// Move puts the ticket id into column, as the orchestrator does: into
// Blocked with the reason, which nothing else takes. A ticket a session
// works on stays where it is, and none gets into Progress this way, see
// Start.
func (s *Store) Move(id int, column, reason string, now time.Time) (Ticket, error) {
	reason = strings.TrimSpace(reason)
	return s.on(id, Orchestrator, func(t *Ticket) error {
		if !slices.Contains(Columns, column) {
			return fmt.Errorf("there is no column %q: there are %s", column, strings.Join(Columns, ", "))
		}
		if run, busy := t.Active(); busy {
			return fmt.Errorf("session %s works on ticket %d: stop it first", run.Session, id)
		}
		if column == t.Column {
			return fmt.Errorf("ticket %d is in %s already", id, column)
		}
		if column == Progress {
			return fmt.Errorf("a ticket gets into %s when its implementation starts: move it to %s and start a session for it", Progress, Ready)
		}
		if blocked := column == Blocked; blocked && reason == "" || len(reason) > MaxNote {
			return fmt.Errorf("a ticket is moved to %s with the reason, of at most %d bytes", Blocked, MaxNote)
		} else if !blocked && reason != "" {
			return fmt.Errorf("only a move to %s takes a reason: say anything else in a comment", Blocked)
		}
		from := t.Column
		move(t, column, Orchestrator, now)
		if column == Blocked {
			t.Reason, t.From = reason, from
		}
		return nil
	})
}

// Start records that session works on the ticket id from now on, in the role
// its column asks for, see Ticket.Role: a ticket of Ready goes into Progress
// with it, and agent is the agent of the profile the session runs as, ""
// for the agent of the thread, called name. One a session works on already, or that
// MaxRuns sessions worked on, takes no other.
func (s *Store) Start(id int, session, agent, name string, now time.Time) (Ticket, error) {
	return s.on(id, Orchestrator, func(t *Ticket) error {
		role, err := t.Role()
		if err != nil {
			return err
		}
		if len(t.Runs) >= MaxRuns {
			return fmt.Errorf("%d sessions worked on ticket %d already, and no more do: move it to %s with what is wrong, and tell the thread", MaxRuns, id, Blocked)
		}
		// A ticket stays in New while it is refined.
		if run, busy := t.Active(); busy {
			return fmt.Errorf("session %s works on ticket %d already", run.Session, id)
		}
		if role == Implementation {
			move(t, Progress, Orchestrator, now)
		}
		t.Runs = append(t.Runs, Run{Session: session, Role: role, Agent: agent, AgentName: name, Began: now})
		started := "started session " + session + " for its " + role
		if name != "" {
			started += ", as " + strconv.Quote(name)
		}
		note(t, Orchestrator, started, now)
		return nil
	})
}

// mine is the run of session at t, which has to be the one that works on it.
func mine(t *Ticket, session string) (Run, error) {
	run, ok := t.Active()
	if !ok || run.Session != session {
		return Run{}, fmt.Errorf("session %s does not work on ticket %d", session, t.ID)
	}
	return run, nil
}

// Close ends the work of session on the ticket id with what it did, summary:
// a refinement moves the ticket to Ready, an implementation to Done, where
// the summary is its result.
func (s *Store) Close(id int, session, summary string, now time.Time) (Ticket, error) {
	summary, err := text("summary", summary, MaxNote)
	if err != nil {
		return Ticket{}, err
	}
	by := BySession(session)
	return s.on(id, by, func(t *Ticket) error {
		run, err := mine(t, session)
		if err != nil {
			return err
		}
		to := Ready
		if run.Role == Implementation {
			to, t.Result = Done, summary
		}
		end(t, to, now)
		note(t, by, summary, now)
		move(t, to, by, now)
		return nil
	})
}

// Block ends the work of session on the ticket id without a result: the
// ticket goes into Blocked for reason, the questions to the owner or what
// else keeps it from going on.
func (s *Store) Block(id int, session, reason string, now time.Time) (Ticket, error) {
	reason, err := text("reason", reason, MaxNote)
	if err != nil {
		return Ticket{}, err
	}
	by := BySession(session)
	return s.on(id, by, func(t *Ticket) error {
		if _, err := mine(t, session); err != nil {
			return err
		}
		block(t, Blocked, reason, by, now)
		return nil
	})
}

// Release takes the ticket from session where it still works on one, as by:
// the session ended without moving it, or was stopped, for reason, and the
// ticket is blocked with it. It reports whether there was a ticket to take.
func (s *Store) Release(session, by, reason string, now time.Time) (Ticket, bool, error) {
	t, ok := s.Of(session)
	if !ok {
		return Ticket{}, false, nil
	}
	t, err := s.on(t.ID, by, func(t *Ticket) error {
		block(t, "ended without a result", reason, by, now)
		return nil
	})
	return t, err == nil, err
}

// Tell records that the thread was told of the column the ticket id is in.
func (s *Store) Tell(id int, now time.Time) (Ticket, error) {
	return s.on(id, Orchestrator, func(t *Ticket) error {
		t.Told = true
		note(t, Orchestrator, "told the thread", now)
		return nil
	})
}
