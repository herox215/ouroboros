package gui

import (
	"context"
	"slices"
	"time"

	"ouroboros/internal/agents"
	"ouroboros/plugin"
)

// The window lists the sessions of its profile beside the thread: the
// conversations the thread handed work to, what each does now, and what
// happened in one lately. It reads them through the same sessions service as
// the thread, as a front end does, and changes nothing in them.

// Session is a session as the window lists it.
type Session struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Running bool      `json:"running"`
	Last    time.Time `json:"last"`
	// Ticket, Work and Role are of the ticket of the board the session works
	// on since Since: its number, its title and the role of the session in it.
	// They are empty while it works on none.
	Ticket int        `json:"ticket,omitempty"`
	Work   string     `json:"work,omitempty"`
	Role   string     `json:"role,omitempty"`
	Since  *time.Time `json:"since,omitempty"`
	// Jobs are the commands that run in the background for its answer, the
	// oldest first.
	Jobs []Job `json:"jobs,omitempty"`
	// Agent is the ID of the agent of the profile it runs as, see agents.
	Agent string `json:"agent,omitempty"`
}

// Job is a command that runs in the background for a session, as the window
// lists it.
type Job struct {
	N       int       `json:"n"`
	Command string    `json:"command"`
	Since   time.Time `json:"since"`
}

// SessionTrack is one session in full: its line of the list, and what
// happened in it lately, the oldest first.
type SessionTrack struct {
	Session
	Events []Event `json:"events"`
}

// What the window shows of a session: how many of its latest events, and how
// much of the text of one.
const (
	sessionEvents = 40
	sessionText   = 2000
)

// Sessions lists the sessions of the profile, the one with the latest event
// first, as Sessions.List names them: with what each works on and what runs
// in the background for it.
func (w *Window) Sessions() ([]Session, error) { return w.sessions() }

// Session returns one session in full, with what happened in it lately. The
// events are read once: the window keeps no watch on a session it only looks
// at, and what it shows of it is as fresh as the last time the page asked.
func (w *Window) Session(id string) (SessionTrack, error) {
	list, err := w.sessions()
	if err != nil {
		return SessionTrack{}, err
	}
	// The watch reads the session from the archive; the context ends once its
	// past is read, so that nothing stays watched of a session looked at.
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	past, _, err := w.profile.sessions.Watch(ctx, id)
	if err != nil {
		return SessionTrack{}, err
	}
	track := SessionTrack{Events: w.lately(past)}
	if i := slices.IndexFunc(list, func(s Session) bool { return s.ID == id }); i >= 0 {
		track.Session = list[i]
	} else {
		track.Session = Session{ID: id}
	}
	return track, nil
}

// sessions lists the sessions as Sessions does, with what the board and the
// background jobs add to them.
func (w *Window) sessions() ([]Session, error) {
	if err := w.ready(); err != nil {
		return nil, err
	}
	list, err := w.profile.sessions.List(w.ctx)
	if err != nil {
		return nil, err
	}
	working := w.profile.working()
	jobs := w.jobs()
	out := make([]Session, 0, len(list))
	for _, info := range list {
		s := Session{ID: info.ID, Title: info.Title, Running: info.Running, Last: info.Last, Jobs: jobs[info.ID], Agent: agents.Of(info.Agent)}
		if work, ok := working[info.ID]; ok {
			s.Ticket, s.Work, s.Role = work.ticket, work.title, work.role
			since := work.since
			s.Since = &since
		}
		out = append(out, s)
	}
	return out, nil
}

// lately is the latest of what a session told, as the window shows it: the
// same events the thread shows, without the state that only applies while it
// runs.
func (w *Window) lately(past []plugin.SessionEvent) []Event {
	var out []Event
	for _, e := range past {
		if !historyEntry(e) {
			continue
		}
		event := w.event(e)
		event.Text = clip(event.Text, sessionText)
		out = append(out, event)
	}
	if len(out) > sessionEvents {
		out = out[len(out)-sessionEvents:]
	}
	if out == nil {
		out = []Event{}
	}
	return out
}

// clip keeps at most n bytes of text, cut at a rune.
func clip(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return text[:n] + " …"
}

// work is what the board says a session works on.
type work struct {
	ticket int
	title  string
	role   string
	since  time.Time
}

// working is the ticket each session works on now, by session ID, as the
// board has it. A board that cannot be read tells nothing: the list of the
// sessions does not depend on it.
func (p *profile) working() map[string]work {
	out := map[string]work{}
	board, err := p.board()
	if err != nil {
		return out
	}
	for _, lane := range board.Lanes {
		for _, card := range lane.Cards {
			if card.Session == "" || card.Since == nil {
				continue
			}
			out[card.Session] = work{ticket: card.ID, title: card.Title, role: card.Role, since: *card.Since}
		}
	}
	return out
}

// jobs are the commands that run in the background for each session, by
// session ID. What runs is a hint about a session, not the list itself: a
// node that cannot tell it leaves the list as it is.
func (w *Window) jobs() map[string][]Job {
	out := map[string][]Job{}
	running, err := w.profile.sessions.Jobs(w.ctx)
	if err != nil {
		return out
	}
	for _, job := range running {
		if job.Peer != "" {
			continue
		}
		out[job.Session] = append(out[job.Session], Job{N: job.N, Command: clip(job.Command, sessionText), Since: job.Since})
	}
	return out
}
