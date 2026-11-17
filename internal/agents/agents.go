// Package agents holds the agents of a profile: who a session of it is. Every
// session runs as one of them, and its answers say who they are by it: the
// agent of the thread, which is called as the profile is and talks with the
// owner, the archivist, which tidies the memories, the orchestrator, which
// looks at the board, and the agents the owner made. What an agent does is
// told by the work it is given; who it is, is told here: its name, what it is
// for and its soul.
//
// The agent of the thread takes the name of the profile, which the
// configuration keeps; everything else is kept in a file of the profile. The
// three built-in agents are always there, with their defaults until the owner
// changes them; the owner's own come and go.
package agents

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"ouroboros/internal/config"
	"ouroboros/internal/safefile"
)

// The IDs of the built-in agents of a profile, which are their kinds too.
const (
	// Thread is the agent of the thread: the one that talks with the owner,
	// and the one a session runs as when nothing says otherwise.
	Thread = "thread"
	// Archivist tidies the memories, in the job the schedule seeds.
	Archivist = "archivist"
	// Orchestrator looks at the board when it changed, and starts the
	// sessions that work on its tickets.
	Orchestrator = "orchestrator"
)

// Custom is the kind of an agent the owner made.
const Custom = "custom"

// IDs are the built-in agents of a profile, in the order they are listed.
var IDs = []string{Thread, Archivist, Orchestrator}

// customPrefix begins the ID of every agent the owner made, see Valid.
const customPrefix = "agent-"

// The bounds of an agent.
const (
	// MaxDescription is how many characters what an agent is for has at
	// most: one or two sentences, which every agent that may delegate reads.
	MaxDescription = 300
	// MaxSoul is how many characters the soul of an agent has at most: it
	// goes with every call of the LLM the agent makes.
	MaxSoul = 8000
	// MaxCustom is how many agents the owner makes at most.
	MaxCustom = 32
	// maxModel is how many characters the ID of an access, and a model,
	// have at most.
	maxModel = 200
)

// defaults are the names of the built-in agents the owner did not rename.
// The agent of the thread has none: it is called as the profile is.
var defaults = map[string]string{
	Archivist:    "Archivist",
	Orchestrator: "Orchestrator",
}

// abouts are what the built-in agents are for: that of a reserved agent,
// which is fixed, and that of the agent of the thread while the owner wrote
// nothing else.
var abouts = map[string]string{
	Thread:       "The owner's personal agent, who talks with them: for any task that no other agent is made for.",
	Archivist:    "Keeps the owner's memories in order: merges, corrects and drops what is remembered.",
	Orchestrator: "Runs the board: has its tickets refined and implemented, and reports what came of them.",
}

// Builtin reports whether id is one of the built-in agents, see IDs.
func Builtin(id string) bool { return slices.Contains(IDs, id) }

// Reserved reports whether id is an agent the system keeps for its own work:
// the archivist and the orchestrator. What they are for is fixed, the owner
// gives them a name and a soul alone, and no agent hands them a task: they
// do only the work ouro gives them. The agent of the thread is built in too,
// but not reserved: the owner says what it is for, and others may choose it.
func Reserved(id string) bool { return id == Archivist || id == Orchestrator }

// Valid reports whether id can name an agent of a profile: a built-in one,
// or one the owner made, which a profile has only while it keeps it. A
// session or a job keeps such an ID; where the agent is gone by the time it
// runs, it runs as the agent of the thread.
func Valid(id string) bool {
	if Builtin(id) {
		return true
	}
	rest, ok := strings.CutPrefix(id, customPrefix)
	if !ok || len(rest) != 8 {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil && strings.ToLower(rest) == rest
}

// Of is the agent a session runs as that names id: the agent of the thread
// for none, as a session from before agents had them is.
func Of(id string) string {
	if id == "" {
		return Thread
	}
	return id
}

// Agent is an agent of a profile: ID tells it, Kind is what it is, its ID
// for a built-in agent and Custom for one of the owner's, and Name is what
// it is called now. Description is what it is for and Soul who it is, as the
// owner wrote them; "" for nothing. Access and Model are the model of its
// own it answers with, through that access of the node, wherever it works:
// both "" for the default of the settings, see config.Config.ThreadLLM and
// SessionLLM, which an agent's own model overrides. Effort is how much the
// model thinks when this agent answers, "" for the effort of the settings.
// The agent of the thread is no exception: its own model answers the thread
// and the sessions that run as it. Reserved says that it is kept for the work
// of the system, see Reserved.
type Agent struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Soul        string `json:"soul,omitempty"`
	Access      string `json:"access,omitempty"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	Reserved    bool   `json:"reserved,omitempty"`
}

// OwnModel reports whether a answers with a model of its own, see Agent.
func (a Agent) OwnModel() bool { return a.Access != "" && a.Model != "" }

// OwnEffort reports whether a answers with an effort of its own, see Agent.
func (a Agent) OwnEffort() bool { return a.Effort != "" }

// About is what a is for: its description, or what a built-in agent is for
// while the owner wrote none; always that of a reserved agent.
func (a Agent) About() string {
	if Reserved(a.Kind) {
		return abouts[a.Kind]
	}
	return cmp.Or(a.Description, abouts[a.Kind])
}

// ErrUnknown is the error for an ID that names no agent.
var ErrUnknown = errors.New("the profile has no such agent")

// ErrBuiltin is the error for removing a built-in agent.
var ErrBuiltin = errors.New("a built-in agent cannot be removed")

// ErrReserved is the error for saying what a reserved agent is for.
var ErrReserved = errors.New("what a system agent is for is fixed")

// entry is an agent as the file keeps it: of a built-in agent only what the
// owner changed, Name "" for its default.
type entry struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Soul        string `json:"soul,omitempty"`
	Access      string `json:"access,omitempty"`
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
}

// entryOf is a as the file keeps it.
func entryOf(a Agent) entry {
	return entry{ID: a.ID, Name: a.Name, Description: a.Description, Soul: a.Soul, Access: a.Access, Model: a.Model, Effort: a.Effort}
}

// file is what the file of the agents holds: the built-in agents the owner
// changed, and the owner's own, in the order they were made.
type file struct {
	Agents []entry `json:"agents,omitempty"`
}

// Store keeps the agents of a profile in a file. A nil Store keeps nothing:
// its built-in agents have their defaults, and it has no others.
type Store struct {
	path string
	mu   sync.Mutex
}

// Open returns the store of the agents in the file path; a file that does
// not exist yet holds the defaults.
func Open(path string) *Store { return &Store{path: path} }

// List lists the agents of the profile, the built-in ones first, the
// agent of the thread called thread, as the profile is. While the file
// cannot be read, it lists the built-in agents as they are by default, with
// why.
func (s *Store) List(thread string) ([]Agent, error) {
	f, err := s.read()
	return f.list(thread), err
}

// Get is the agent id of the profile, the agent of the thread called
// thread; ErrUnknown for an ID it has not. While the file cannot be read, a
// built-in agent is as it is by default, with why.
func (s *Store) Get(id, thread string) (Agent, error) {
	list, err := s.List(thread)
	at := slices.IndexFunc(list, func(a Agent) bool { return a.ID == id })
	if at < 0 {
		return Agent{}, cmp.Or(err, fmt.Errorf("%w: %q", ErrUnknown, id))
	}
	return list[at], err
}

// Create makes an agent of the owner's from a, of which it takes the name,
// the description and the soul, and returns it as it is kept, with its ID.
// The name is none that taken holds or another agent has, the name of the
// agent of the thread among taken.
func (s *Store) Create(a Agent, taken ...string) (Agent, error) {
	a.ID, a.Kind = "", Custom
	if err := normalize(&a); err != nil {
		return Agent{}, err
	}
	err := s.change(func(f *file) error {
		if n := len(f.custom()); n >= MaxCustom {
			return fmt.Errorf("the profile has %d agents of its own already, at most %d are allowed", n, MaxCustom)
		}
		if err := f.free("", a.Name, taken); err != nil {
			return err
		}
		for a.ID == "" || slices.ContainsFunc(f.Agents, func(e entry) bool { return e.ID == a.ID }) {
			a.ID = newID()
		}
		f.Agents = append(f.Agents, entryOf(a))
		return nil
	})
	if err != nil {
		return Agent{}, err
	}
	return a, nil
}

// Set changes the agent id to what a says: its name, its description and
// its soul, its model and its effort. The name of the agent of the thread is
// not kept here, it is renamed with its profile: a.Name is left as it is for
// it. Any other name is none that taken holds or another agent has, as for
// Create. A reserved agent takes no description: what it is for is fixed.
func (s *Store) Set(id string, a Agent, taken ...string) error {
	if Reserved(id) && strings.TrimSpace(a.Description) != "" {
		return ErrReserved
	}
	a.ID, a.Kind = id, id
	if !Builtin(id) {
		a.Kind = Custom
	}
	if id == Thread {
		a.Name = "Thread" // valid, and not kept: see below
	}
	if err := normalize(&a); err != nil {
		return err
	}
	return s.change(func(f *file) error {
		at := slices.IndexFunc(f.Agents, func(e entry) bool { return e.ID == id })
		if at < 0 && !Builtin(id) {
			return fmt.Errorf("%w: %q", ErrUnknown, id)
		}
		e := entryOf(a)
		switch id {
		case Thread:
			e.Name = ""
		default:
			if err := f.free(id, a.Name, taken); err != nil {
				return err
			}
			if e.Name == defaults[id] {
				e.Name = ""
			}
		}
		switch {
		case at >= 0 && Builtin(id) && e == (entry{ID: id}):
			// A built-in agent as it is by default needs no entry.
			f.Agents = slices.Delete(f.Agents, at, at+1)
		case at >= 0:
			f.Agents[at] = e
		case e != (entry{ID: id}):
			f.Agents = append(f.Agents, e)
		}
		return nil
	})
}

// Delete removes the agent id of the owner's. A session or a job that runs
// as it runs as the agent of the thread from then on.
func (s *Store) Delete(id string) error {
	if Builtin(id) {
		return ErrBuiltin
	}
	return s.change(func(f *file) error {
		at := slices.IndexFunc(f.Agents, func(e entry) bool { return e.ID == id })
		if at < 0 {
			return fmt.Errorf("%w: %q", ErrUnknown, id)
		}
		f.Agents = slices.Delete(f.Agents, at, at+1)
		return nil
	})
}

// ValidName reports why name cannot be the name of an agent, nil if it can.
func ValidName(name string) error {
	if err := config.ValidName("the name of an agent", name); err != nil {
		return err
	}
	if n := utf8.RuneCountInString(name); n > config.MaxName {
		return fmt.Errorf("the name of an agent has %d characters, at most %d are allowed", n, config.MaxName)
	}
	return nil
}

// normalize trims what a says of itself and reports why it cannot be an
// agent, nil if it can.
func normalize(a *Agent) error {
	a.Name = strings.TrimSpace(a.Name)
	a.Description = strings.TrimSpace(a.Description)
	a.Soul = strings.TrimSpace(strings.ReplaceAll(a.Soul, "\r\n", "\n"))
	a.Access, a.Model = strings.TrimSpace(a.Access), strings.TrimSpace(a.Model)
	a.Effort = strings.TrimSpace(a.Effort)
	if err := ValidName(a.Name); err != nil {
		return err
	}
	if !slices.Contains(config.AllEfforts, a.Effort) {
		return fmt.Errorf("effort %q is none of %s", a.Effort, strings.Join(config.AllEfforts[1:], ", "))
	}
	if (a.Access == "") != (a.Model == "") {
		return errors.New("an agent has an access and a model of its own, or neither")
	}
	if err := validText("the access of an agent", a.Access, maxModel, false); err != nil {
		return err
	}
	if err := validText("the model of an agent", a.Model, maxModel, false); err != nil {
		return err
	}
	if err := validText("what an agent is for", a.Description, MaxDescription, false); err != nil {
		return err
	}
	return validText("the soul of an agent", a.Soul, MaxSoul, true)
}

// validText reports why s cannot be field, of at most max characters, on
// lines of its own where lines says so; nil if it can be.
func validText(field, s string, max int, lines bool) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%s is no valid text", field)
	}
	if n := utf8.RuneCountInString(s); n > max {
		return fmt.Errorf("%s has %d characters, at most %d are allowed", field, n, max)
	}
	for _, r := range s {
		if lines && (r == '\n' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			if lines {
				return fmt.Errorf("%s has control characters", field)
			}
			return fmt.Errorf("%s is one line, without tabs or other control characters", field)
		}
	}
	return nil
}

// newID is a new ID for an agent of the owner's, see Valid.
func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b) // never fails, see crypto/rand
	return customPrefix + hex.EncodeToString(b)
}

// list is the agents of f, as Store.List lists them.
func (f file) list(thread string) []Agent {
	list := make([]Agent, 0, len(IDs)+len(f.Agents))
	for _, id := range IDs {
		a := Agent{ID: id, Kind: id, Name: defaults[id], Reserved: Reserved(id)}
		if at := slices.IndexFunc(f.Agents, func(e entry) bool { return e.ID == id }); at >= 0 {
			e := f.Agents[at]
			a.Name, a.Description, a.Soul, a.Access, a.Model = cmp.Or(e.Name, a.Name), e.Description, e.Soul, e.Access, e.Model
			a.Effort = e.Effort
		}
		if id == Thread {
			a.Name = thread
		}
		list = append(list, a)
	}
	for _, e := range f.custom() {
		list = append(list, Agent{ID: e.ID, Kind: Custom, Name: e.Name, Description: e.Description, Soul: e.Soul, Access: e.Access, Model: e.Model, Effort: e.Effort})
	}
	return list
}

// custom are the agents of the owner's in f.
func (f file) custom() []entry {
	return slices.DeleteFunc(slices.Clone(f.Agents), func(e entry) bool { return Builtin(e.ID) })
}

// free reports why the agent id cannot be called name: because taken holds
// it, or another agent is called so already; nil if it can.
func (f file) free(id, name string, taken []string) error {
	for _, a := range f.list("") {
		if a.ID != id && a.ID != Thread {
			taken = append(taken, a.Name)
		}
	}
	for _, other := range taken {
		if strings.EqualFold(strings.TrimSpace(other), name) {
			return fmt.Errorf("another agent is called %q already", other)
		}
	}
	return nil
}

// change changes the file with do, under its lock, and writes it unless do
// fails. A file that cannot be read is not overwritten.
func (s *Store) change(do func(f *file) error) error {
	if s == nil {
		return errors.New("the agents of this profile are not kept")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := safefile.Lock(s.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	if err := do(&f); err != nil {
		return err
	}
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return safefile.Write(s.path, append(out, '\n'), 0o600)
}

// read reads the file of the agents: nothing for a store that keeps nothing
// or a file that is not there or empty. An entry no agent can be is left
// out, as is a second one of an ID.
func (s *Store) read() (file, error) {
	var f file
	if s == nil {
		return f, nil
	}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return f, nil
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return file{}, fmt.Errorf("the agents in %s cannot be read: %w", s.path, err)
	}
	seen := map[string]bool{}
	for i := range f.Agents {
		if Reserved(f.Agents[i].ID) {
			f.Agents[i].Description = "" // fixed, see Reserved
		}
	}
	f.Agents = slices.DeleteFunc(f.Agents, func(e entry) bool {
		drop := seen[e.ID] || !Valid(e.ID) || !valid(e)
		seen[e.ID] = true
		return drop
	})
	return f, nil
}

// valid reports whether e can be an agent as the file keeps it.
func valid(e entry) bool {
	a := Agent{ID: e.ID, Name: e.Name, Description: e.Description, Soul: e.Soul, Access: e.Access, Model: e.Model, Effort: e.Effort}
	if Builtin(e.ID) && e.Name == "" {
		a.Name = "Default"
	}
	if e.ID == Thread && e.Name != "" {
		return false
	}
	before := a
	return normalize(&a) == nil && a == before
}
