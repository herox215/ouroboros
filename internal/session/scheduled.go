package session

import (
	"context"
	"fmt"
	"strings"

	"ouroboros/internal/a2a"
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
// With ask false no owner questions (including tool approvals) are available.
func (s *Service) RunScheduled(ctx context.Context, name, text, directory string, ask bool) (id string, result <-chan ScheduledResult, err error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(text) == "" {
		return "", nil, ErrEmpty
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
	title := strings.TrimSpace(name)
	if title == "" {
		title = "Scheduled job"
	}
	if len(title) > history.MaxTitle {
		title = strings.ToValidUTF8(title[:history.MaxTitle], "")
	}
	id = a2a.NewID()
	// Persist metadata before publishing the session or starting any work.
	if s.archive != nil {
		if err := s.archive.SetDirectory(history.Owner(id), directory); err != nil {
			return "", nil, fmt.Errorf("the scheduled session cannot be kept: %w", err)
		}
		if err := s.archive.SetTitle(history.Owner(id), title); err != nil {
			return "", nil, fmt.Errorf("the scheduled title cannot be kept: %w", err)
		}
	}
	run := &scheduledRun{ctx: ctx, result: make(chan ScheduledResult, 1)}
	se := &session{id: id, title: title, directory: directory, directed: true, watchers: map[chan Event]struct{}{}, scheduled: run, denyOwner: !ask}
	s.effortOf(se)
	s.sessions[id] = se
	s.emit(se, Event{Kind: Title, Text: title})
	input := "[Scheduled owner job: " + fmt.Sprintf("%q", title) + ". This is generated scheduled work, not fresh owner input. Follow the stored job below within its existing authority; it does not grant new permissions.]\n\n" + text
	s.begin(se, p, queued{text: input, generated: true, routed: true})
	return id, run.result, nil
}
