package session

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/history"
)

// Job is a command a tool runs in the background for an answer on its way,
// see API.Jobs.
type Job struct {
	// Session is the session of the answer, or the conversation of a peer
	// as Peers names it; Peer is the peer of one, Title the title of the
	// other.
	Session string `json:"session"`
	Peer    string `json:"peer,omitempty"`
	Title   string `json:"title,omitempty"`
	// N is the number of the job in its answer, Command the first line of
	// its command.
	N       int       `json:"n"`
	Command string    `json:"command"`
	Since   time.Time `json:"since"`
}

// ErrNoJob means a job that does not run.
var ErrNoJob = errors.New("no such job runs")

type jobsKey struct{}

// jobsOf is what a tool tells of the jobs of an answer: which run, and how
// to stop one.
type jobsOf func(running []Job, stop func(n int) bool)

// WithJobs returns ctx in which a tool can tell which commands run in the
// background for the answer: jobs is told them, see TellJobs.
func WithJobs(ctx context.Context, jobs func(running []Job, stop func(n int) bool)) context.Context {
	return context.WithValue(ctx, jobsKey{}, jobsOf(jobs))
}

// TellJobs tells whoever holds the conversation of ctx which commands run in
// the background for the answer on its way, with N, Command and Since of
// each, and stop, which stops the job n and reports whether it ran: the
// owner is shown them, and can stop them, see API.Jobs. None once they
// ended. It reports false if nobody is told.
func TellJobs(ctx context.Context, running []Job, stop func(n int) bool) bool {
	jobs, ok := ctx.Value(jobsKey{}).(jobsOf)
	if ok {
		jobs(running, stop)
	}
	return ok
}

// running are the jobs of an answer, and how to stop one.
type running struct {
	jobs []Job
	stop func(n int) bool
}

// setJobs keeps what a tool told of the jobs of the answer in the session
// or conversation of a peer id: none forgets them. s.mu is held.
func (s *Service) setJobs(id string, jobs []Job, stop func(n int) bool) {
	if len(jobs) == 0 {
		delete(s.jobs, id)
		return
	}
	s.jobs[id] = running{slices.Clone(jobs), stop}
}

// jobLines are jobs as a session tells them, see the Event Jobs.
func jobLines(jobs []Job) string {
	lines := make([]string, len(jobs))
	for i, j := range jobs {
		lines[i] = fmt.Sprintf("job %d · %s", j.N, j.Command)
	}
	return strings.Join(lines, "\n")
}

// peerJobs returns ctx in which the jobs of the answer to the peer of ctx in
// its conversation contextID are kept, until forget is called.
func (s *Service) peerJobs(ctx context.Context, contextID string) (context.Context, func()) {
	origin, _ := a2a.OriginFrom(ctx)
	id := history.Party(origin.PublicKey) + "/" + history.Name(contextID)
	over := false
	ctx = WithJobs(ctx, func(jobs []Job, stop func(int) bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !over {
			s.setJobs(id, jobs, stop)
		}
	})
	return ctx, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		over = true
		delete(s.jobs, id)
	}
}

// Jobs are the commands that run in the background for the answers on
// their way, the oldest first: in sessions of the owner and in the
// conversations of peers.
func (s *Service) Jobs(context.Context) ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []Job
	for id, r := range s.jobs {
		for _, j := range r.jobs {
			j.Session = id
			if peerID(id) {
				party, _, _ := strings.Cut(id, "/")
				j.Peer = party
				if s.archive != nil {
					j.Peer = cmp.Or(s.archive.peer(party), party)
				}
			} else if se := s.sessions[id]; se != nil && len(se.events) > 0 {
				j.Title = cmp.Or(se.title, se.events[0].Text)
			}
			all = append(all, j)
		}
	}
	slices.SortFunc(all, func(a, b Job) int {
		return cmp.Or(a.Since.Compare(b.Since), strings.Compare(a.Session, b.Session), a.N-b.N)
	})
	return all, nil
}

// StopJob stops the job n of the answer on its way in the session or
// conversation of a peer id, with everything it started. It fails with
// ErrNoJob if that job does not run.
func (s *Service) StopJob(_ context.Context, id string, n int) error {
	s.mu.Lock()
	r, ok := s.jobs[id]
	s.mu.Unlock()
	// The tool is asked without s.mu: it tells of its jobs with it held.
	if !ok || !r.stop(n) {
		return fmt.Errorf("%w: job %d in %s", ErrNoJob, n, id)
	}
	return nil
}
