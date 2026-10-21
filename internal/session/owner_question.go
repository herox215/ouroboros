package session

import (
	"context"
	"encoding/json"

	"ouroboros/internal/a2a"
)

// OwnerQuestion asks a backend question in the thread without invoking an agent.
// It shares the normal question queue and is cancelled when the service stops.
func (s *Service) OwnerQuestion(ctx context.Context, question json.RawMessage) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", ErrStopped
	}
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	se := s.get("approval-" + a2a.NewID())
	se.backendQuestion = true
	se.routed = true
	se.cancel = cancel
	s.answers.Add(1)
	s.mu.Unlock()
	defer s.answers.Done()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, se.id)
		s.mu.Unlock()
	}()
	reply, err := s.ask(ctx, se, question)
	return reply.Text, err
}
