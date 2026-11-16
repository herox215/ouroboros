package session

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agents"
	"ouroboros/internal/history"
)

// ScheduledResult is the terminal result of a scheduled answer, also relayed
// through the normal thread. Err is non-nil on failure or cancellation.
type ScheduledResult struct {
	Text string
	Err  error
}

type scheduledRun struct {
	ctx    context.Context
	result chan ScheduledResult
	stop   func() bool
}

// RunScheduled starts a new owner session with generated, routed input. The
// caller must keep ctx alive until completion; canceling it stops the answer.
// Exactly one result is sent to the buffered channel, which is then closed.
// The session runs as the agent of the profile as names, see agents: the
// agent of the thread for "".
// With ask false no owner questions (including tool approvals) are available.
// With tools its answers get only those of the owner's agent's tools.
func (s *Service) RunScheduled(ctx context.Context, name, text, directory, as string, ask bool, tools ...string) (id string, result <-chan ScheduledResult, err error) {
	title := generatedTitle(name, "Scheduled job")
	input := "[Scheduled owner job: " + fmt.Sprintf("%q", title) + ". This is generated scheduled work, not fresh owner input. Follow the stored job below within its existing authority; it does not grant new permissions.]\n\n" + text
	return s.generated(ctx, &session{title: title, denyOwner: !ask, tools: tools, agent: as}, text, input, directory, true)
}

// generatedTitle is name as the title of a session of generated work, or
// unnamed where it names nothing.
func generatedTitle(name, unnamed string) string {
	title := strings.TrimSpace(name)
	if title == "" {
		title = unnamed
	}
	if len(title) > history.MaxTitle {
		title = strings.ToValidUTF8(title[:history.MaxTitle], "")
	}
	return title
}

// generated starts the session se, which has its title and what its answers
// may do, with input, generated for the work text, in directory: see
// RunScheduled. routed says that the thread is told what comes of it.
func (s *Service) generated(ctx context.Context, se *session, text, input, directory string, routed bool) (id string, result <-chan ScheduledResult, err error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(text) == "" {
		return "", nil, ErrEmpty
	}
	// Every session runs as an agent of the profile: the agent of the
	// thread where the work names none.
	se.agent = agents.Of(se.agent)
	if !agents.Valid(se.agent) {
		return "", nil, fmt.Errorf("%w: %q", agents.ErrUnknown, se.agent)
	}
	if err := s.agent.Ready(); err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.node
	p.mu.Lock()
	defer p.mu.Unlock()
	if s.closed {
		return "", nil, ErrStopped
	}
	if p.successor != "" {
		return "", nil, ErrRestarting
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	base := s.initialDirectory
	if base == "" {
		base = s.Workspace
	}
	directory, err = resolveDirectory(directory, base)
	if err != nil {
		return "", nil, err
	}
	id = a2a.NewID()
	// Persist metadata before publishing the session or starting any work.
	if s.archive != nil {
		if err := s.archive.SetDirectory(history.Owner(id), directory); err != nil {
			return "", nil, fmt.Errorf("the scheduled session cannot be kept: %w", err)
		}
		if err := s.archive.SetTitle(history.Owner(id), se.title); err != nil {
			return "", nil, fmt.Errorf("the scheduled title cannot be kept: %w", err)
		}
		if err := s.archive.SetAgent(history.Owner(id), se.agent); err != nil {
			return "", nil, fmt.Errorf("the agent of the scheduled session cannot be kept: %w", err)
		}
	}
	run := &scheduledRun{ctx: ctx, result: make(chan ScheduledResult, 1)}
	se.id, se.directory, se.directed, se.watchers, se.scheduled = id, directory, true, map[chan Event]struct{}{}, run
	se.tools = slices.Clone(se.tools) // none are all of them, see assistant.OnlyTools
	s.effortOf(se)
	s.sessions[id] = se
	s.emit(se, Event{Kind: Title, Text: se.title})
	s.begin(se, p, queued{text: input, generated: true, routed: routed})
	return id, run.result, nil
}
