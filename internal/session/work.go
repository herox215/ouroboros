package session

import (
	"context"

	"ouroboros/internal/agent"
)

// Work is generated work for a session of its own that a part of ouro starts
// for itself, as the board does for a ticket, see RunWork.
type Work struct {
	// Name is the title of the session, Text its first message, and
	// Directory the project it works in, "" for the workspace.
	Name, Text, Directory string
	// Agent is the agent of the profile the session runs as, see agents:
	// the agent of the thread for "".
	Agent string
	// Ask says that what needs the leave of the owner is asked through the
	// thread; without it nobody can be asked, and such a call is refused.
	Ask bool
	// Tools names the only tools of the owner's agent the session gets; none
	// for all of them. A list that names none of them, like the names of
	// Own, leaves the session Own alone.
	Tools []string
	// Own are tools for this session alone, which no other answer has, and
	// which it has whatever Tools names.
	Own []agent.Tool
}

// RunWork starts a session for w and returns its ID, and a channel that
// takes how its answer ended, once, and is closed then. Canceling ctx stops
// the answer. Unlike a scheduled run it is apart: the thread is not told of
// the session, hands it nothing and gets no report of it, as it reports to
// who started it. Only what needs the leave of the owner still goes through
// the thread, with Ask.
func (s *Service) RunWork(ctx context.Context, w Work) (id string, result <-chan ScheduledResult, err error) {
	se := &session{title: generatedTitle(w.Name, "Work"), denyOwner: !w.Ask, tools: w.Tools, apart: true, extra: w.Own, agent: w.Agent}
	return s.generated(ctx, se, w.Text, w.Text, w.Directory, false)
}

// TellWork hands text to the answer on its way in the work id, see RunWork,
// which takes it as soon as it can, like a message of the owner. It reports
// whether the work still runs to take it: one that ended is told nothing.
func (s *Service) TellWork(id, text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	se := s.sessions[id]
	if se == nil || se.scheduled == nil || !se.apart {
		return false
	}
	se.queue = append(se.queue, queued{text: text, generated: true, note: true})
	return true
}

// WorkState tells what the work id, see RunWork, does now, as the thread
// reads it of a session, see threading.State; "" for a session that is no
// such work, or none the node holds.
func (s *Service) WorkState(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	se := s.sessions[id]
	if se == nil || !se.apart {
		return ""
	}
	return s.state(se)
}
