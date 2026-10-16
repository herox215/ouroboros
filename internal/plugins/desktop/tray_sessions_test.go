package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type namedSessions struct {
	testSessions
	names    []string
	namesErr error
}

func (s *namedSessions) RunningSessions(context.Context) ([]string, error) {
	return s.names, s.namesErr
}

func TestTraySessionNames(t *testing.T) {
	a := &Profile{source: &namedSessions{names: []string{"Private topic A"}}}
	b := &Profile{source: &namedSessions{names: []string{"Private topic B"}}}
	Together([]*Profile{a, b})
	got := a.controller.sample(context.Background())
	if len(got.Sessions) != 2 || got.Sessions[0] != "Private topic A" || got.Sessions[1] != "Private topic B" {
		t.Fatalf("names: %+v", got)
	}
	wire, err := json.Marshal(got)
	if err != nil || strings.Contains(string(wire), "Private") || strings.Contains(string(wire), "Sessions") {
		t.Fatalf("names leaked into CLI: %s %v", wire, err)
	}
	b.source = &namedSessions{namesErr: errors.New("private error")}
	if got := a.controller.sample(context.Background()); got.State != "error" {
		t.Fatalf("failed names reported as ready: %+v", got)
	}
}
