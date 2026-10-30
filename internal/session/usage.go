package session

import (
	"context"

	"ouroboros/internal/assistant"
)

// ContextUsage is what a conversation holds of its context: how long it was
// when the LLM was last called in it, and how many tokens that call sent, as
// the provider told it.
type ContextUsage struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Model string `json:"model,omitempty"`
	// Running says that an answer is on its way in the conversation.
	Running bool `json:"running,omitempty"`
	// Bytes is what the last call of the LLM sent, as JSON, see Event.Context.
	Bytes int `json:"bytes,omitempty"`
	// Cache is what the last call sent, and how much of it was cached, see
	// Event.Cache. Its Tokens are 0 until a call was made whose API told it.
	Cache assistant.Cache `json:"cache,omitzero"`
}

// ContextOf returns what the conversation id holds of its context, whether
// the node holds it or only its archive: the session is loaded if it is not
// in memory. A conversation that holds nothing yet is reported as it is; an
// ID that names none fails with ErrNoSession.
func (s *Service) ContextOf(_ context.Context, id string) (ContextUsage, error) {
	if err := valid(id); err != nil {
		return ContextUsage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	se := s.lookup(id)
	if se == nil {
		return ContextUsage{}, ErrNoSession
	}
	u := ContextUsage{ID: id, Title: se.title, Model: se.model, Running: se.cancel != nil}
	if u.Model == "" {
		u.Model = s.model(id)
	}
	if len(se.events) > 0 {
		last := se.events[len(se.events)-1]
		if u.Title == "" {
			u.Title = se.events[0].Text
		}
		u.Bytes, u.Cache = last.Context, last.Cache
	}
	return u, nil
}
