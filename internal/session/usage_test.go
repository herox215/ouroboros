package session

import (
	"context"
	"errors"
	"testing"

	"ouroboros/internal/assistant"
)

// ContextOf tells what a conversation holds of its context: its title, the
// model it answers with, the bytes and the cache of the last call of the LLM,
// and whether an answer is on its way. An ID that names no conversation fails
// with ErrNoSession.
func TestContextOf(t *testing.T) {
	a := &fakeAgent{model: "m", size: 4096, cache: assistant.Cache{Tokens: 1000, Cached: 250}}
	s := service(t, a)
	ctx := context.Background()
	if _, err := s.ContextOf(ctx, "nope"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("no such session: %v", err)
	}
	_, live, err := s.Watch(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ask(ctx, "s1", "where is a"); err != nil {
		t.Fatal(err)
	}
	until(t, live, Answer)
	u, err := s.ContextOf(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != "s1" || u.Title != "where is a" || u.Model != "m" || u.Running || u.Bytes != 4096 || u.Cache != a.cache {
		t.Fatalf("answered: %+v", u)
	}
}
