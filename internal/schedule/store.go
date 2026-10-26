// Package schedule stores owner jobs and calculates five-field cron schedules.
// A profile must have only one live scheduler. Open is a startup operation: it
// marks claims left by the previous scheduler as interrupted, without retrying.
package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/safefile"
)

const (
	// MaxJobs includes paused and exhausted one-shot jobs until removed.
	MaxJobs = 32
	// MaxText is the instruction size limit in bytes, not characters.
	MaxText = 4 << 10
)

var (
	// ErrNotFound means the ID is absent, including a concurrently removed job.
	ErrNotFound = errors.New("no such scheduled job")
	// ErrFull means creating another job would exceed MaxJobs.
	ErrFull = errors.New("at most 32 scheduled jobs are allowed")
)

// Job is an owner instruction. Exactly one of At (RFC3339) and Cron is set.
// Zone is an IANA location, defaulting to the machine location on creation.
// Ask defaults to thread; never is the only other value. Next is zero after a
// one-shot claim. Pausing preserves Next; resuming can therefore run late.
// ID, Created, Next and Runs are managed by Store, not by change callbacks.
type Job struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Text      string    `json:"text"`
	At        string    `json:"at,omitempty"`
	Cron      string    `json:"cron,omitempty"`
	Zone      string    `json:"zone"`
	Directory string    `json:"directory,omitempty"`
	Ask       string    `json:"ask"`
	Paused    bool      `json:"paused,omitempty"`
	Created   time.Time `json:"created"`
	Next      time.Time `json:"next"`
	Runs      []Run     `json:"runs,omitempty"`
}

// Run is a bounded history entry. At is the planned time for claims; callers
// may supply their execution time in Finish. Result retains only its first line.
type Run struct {
	At      time.Time `json:"at"`
	Session string    `json:"session,omitempty"`
	Status  string    `json:"status"`
	Result  string    `json:"result,omitempty"`
}

// Store serializes mutations with a mutex and a safefile lock. Each mutation
// reloads the file, then atomically replaces it. List and Get return detached
// snapshots of the last successful read/write by this Store.
type Store struct {
	mu   sync.Mutex
	path string
	jobs []Job
}

// Open reads path (a missing file means no jobs), and records abandoned running
// claims as interrupted. Invalid JSON is an error, never silently overwritten.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	err := s.mutate(func(jobs *[]Job) error {
		for i := range *jobs {
			for k := range (*jobs)[i].Runs {
				r := &(*jobs)[i].Runs[k]
				if r.Status == "running" {
					r.Status = "interrupted"
					r.Result = "The node stopped before this run finished."
				}
			}
		}
		return nil
	})
	return s, err
}

func clone(j Job) Job { j.Runs = slices.Clone(j.Runs); return j }

func (s *Store) mutate(fn func(*[]Job) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := safefile.Lock(s.path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	b, err := os.ReadFile(s.path)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	var jobs []Job
	if len(b) > 0 {
		if err = json.Unmarshal(b, &jobs); err != nil {
			return err
		}
	}
	if err = fn(&jobs); err != nil {
		return err
	}
	out, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	if !(missing && len(jobs) == 0) && !slices.Equal(b, append(out, '\n')) {
		if err = safefile.Write(s.path, append(out, '\n'), 0600); err != nil {
			return err
		}
	}
	s.jobs = jobs
	return nil
}

// List returns jobs in creation order, with detached histories.
func (s *Store) List() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, len(s.jobs))
	for i, j := range s.jobs {
		out[i] = clone(j)
	}
	return out
}

// Get returns a detached job, or ErrNotFound.
func (s *Store) Get(id string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if j.ID == id {
			return clone(j), nil
		}
	}
	return Job{}, ErrNotFound
}

// Create validates a future schedule and assigns its identity and timestamps.
func (s *Store) Create(j Job, now time.Time) (Job, error) {
	err := s.mutate(func(jobs *[]Job) error {
		if len(*jobs) >= MaxJobs {
			return ErrFull
		}
		if err := validate(&j, now, true); err != nil {
			return err
		}
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		j.ID = hex.EncodeToString(b)
		j.Created = now
		j.Runs = nil
		*jobs = append(*jobs, clone(j))
		return nil
	})
	return clone(j), err
}

// Update applies change atomically. Failed callbacks/validation persist nothing.
// Changing At, Cron or Zone recalculates Next and requires a future schedule.
func (s *Store) Update(id string, change func(*Job) error, now time.Time) (Job, error) {
	var out Job
	err := s.mutate(func(jobs *[]Job) error {
		for i, old := range *jobs {
			if old.ID != id {
				continue
			}
			j := clone(old)
			if err := change(&j); err != nil {
				return err
			}
			changed := j.At != old.At || j.Cron != old.Cron || j.Zone != old.Zone
			j.ID = old.ID
			j.Created = old.Created
			j.Next = old.Next
			j.Runs = slices.Clone(old.Runs)
			if err := validate(&j, now, changed); err != nil {
				return err
			}
			(*jobs)[i] = clone(j)
			out = j
			return nil
		}
		return ErrNotFound
	})
	return out, err
}

// Remove deletes a job including its history, or returns ErrNotFound.
func (s *Store) Remove(id string) error {
	return s.mutate(func(jobs *[]Job) error {
		for i, j := range *jobs {
			if j.ID == id {
				*jobs = slices.Delete(*jobs, i, i+1)
				return nil
			}
		}
		return ErrNotFound
	})
}

func validate(j *Job, now time.Time, recalculate bool) error {
	if strings.TrimSpace(j.Name) == "" || strings.TrimSpace(j.Text) == "" {
		return errors.New("name and text are required")
	}
	if len(j.Text) > MaxText {
		return errors.New("text exceeds 4 KiB")
	}
	if (j.At == "") == (j.Cron == "") {
		return errors.New("set exactly one of at and cron")
	}
	if j.Ask == "" {
		j.Ask = "thread"
	}
	if j.Ask != "thread" && j.Ask != "never" {
		return errors.New("ask must be thread or never")
	}
	if j.Zone == "" || j.Zone == "Local" {
		var err error
		j.Zone, err = MachineZone()
		if err != nil {
			return err
		}
	}
	loc, err := time.LoadLocation(j.Zone)
	if err != nil {
		return fmt.Errorf("zone: %w", err)
	}
	var next time.Time
	if j.At != "" {
		next, err = time.Parse(time.RFC3339, j.At)
		if err != nil {
			return fmt.Errorf("at must be RFC3339: %w", err)
		}
		if recalculate && !next.After(now) {
			return errors.New("at must be in the future")
		}
	} else {
		c, e := ParseCron(j.Cron)
		if e != nil {
			return e
		}
		if e = c.validateInterval(); e != nil {
			return e
		}
		if recalculate {
			if e = c.validateZone(loc, now.Year()); e != nil {
				return e
			}
		}
		next, err = c.Next(now, loc)
		if err != nil {
			return err
		}
	}
	if recalculate {
		j.Next = next
	}
	return nil
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return strings.TrimSuffix(line, "\r")
}

func appendRun(j *Job, r Run) {
	r.Result = firstLine(r.Result)
	j.Runs = append(j.Runs, r)
	if len(j.Runs) > 10 {
		j.Runs = slices.Clone(j.Runs[len(j.Runs)-10:])
	}
}

// Claim atomically advances all unpaused due jobs before returning them. Returned
// jobs retain their original Next. Recurrences skip straight to after now;
// one-shots become exhausted. More than 24h late records missed (not running),
// but still returns the job for notification. The runner detects this using
// now.Sub(job.Next)>24*time.Hour and must not execute or Finish that claim.
// Overlap decisions belong to the runner, which records skipped via Finish.
func (s *Store) Claim(now time.Time) ([]Job, error) {
	var due []Job
	err := s.mutate(func(jobs *[]Job) error {
		for i := range *jobs {
			j := &(*jobs)[i]
			if j.Paused || j.Next.IsZero() || j.Next.After(now) {
				continue
			}
			due = append(due, clone(*j))
			at := j.Next
			j.Next = time.Time{}
			if j.Cron != "" {
				c, err := ParseCron(j.Cron)
				if err != nil {
					return err
				}
				loc, err := time.LoadLocation(j.Zone)
				if err != nil {
					return err
				}
				j.Next, err = c.Next(now, loc)
				if err != nil {
					return err
				}
			}
			status := "running"
			if now.Sub(at) > 24*time.Hour {
				status = "missed"
			}
			appendRun(j, Run{At: at, Status: status})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return due, nil
}

// Finish records a terminal result, retaining the last ten entries. Skipped
// replaces the newest pending claim (leaving an earlier active claim intact);
// other statuses replace the oldest pending claim. Without a pending claim it
// appends. A removed job returns ErrNotFound. running is reserved for Claim.
func (s *Store) Finish(id string, r Run) error {
	if r.Status == "" || r.Status == "running" {
		return errors.New("a terminal run status is required")
	}
	return s.mutate(func(jobs *[]Job) error {
		for i := range *jobs {
			j := &(*jobs)[i]
			if j.ID != id {
				continue
			}
			idx := -1
			for k, p := range j.Runs {
				if p.Status == "running" {
					idx = k
					if r.Status != "skipped" {
						break
					}
				}
			}
			if idx >= 0 {
				if r.At.IsZero() {
					r.At = j.Runs[idx].At
				}
				r.Result = firstLine(r.Result)
				j.Runs[idx] = r
			} else {
				appendRun(j, r)
			}
			return nil
		}
		return ErrNotFound
	})
}

// MachineZone returns the IANA name of the machine's time zone. Persist a name
// rather than Local or today's offset: Local would silently follow a machine
// zone change on restart, and an offset would break cron across daylight
// saving. If no IANA name is discoverable, require an explicit zone.
func MachineZone() (string, error) {
	candidates := []string{strings.TrimPrefix(os.Getenv("TZ"), ":"), time.Local.String()}
	if path, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(path, "/zoneinfo/"); ok {
			candidates = append(candidates, name)
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		candidates = append(candidates, strings.TrimSpace(string(b)))
	}
	for _, name := range candidates {
		if name == "" || name == "Local" || filepath.IsAbs(name) {
			continue
		}
		if _, err := time.LoadLocation(name); err == nil {
			return name, nil
		}
	}
	return "", errors.New("cannot determine machine IANA time zone; set zone explicitly")
}
