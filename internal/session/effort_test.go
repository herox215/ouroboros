package session

import (
	"context"
	"testing"

	"ouroboros/internal/agents"
)

// A session thinks with the effort its owner set for it, else with that of the
// agent it runs as, else with that of the node.
func TestEffortOfTheAgentOfASession(t *testing.T) {
	s := service(t, &fakeAgent{effort: "high", efforts: map[string]string{agents.Archivist: "max"}})
	ctx := context.Background()
	s.mu.Lock()
	se := s.get("s1")
	se.agent = agents.Archivist
	s.mu.Unlock()
	if got := s.effort(se); got != "max" {
		t.Fatalf("the effort of the agent: %q", got)
	}
	// An agent without an effort of its own thinks as the node does.
	s.mu.Lock()
	se.agent = agents.Orchestrator
	s.mu.Unlock()
	if got := s.effort(se); got != "high" {
		t.Fatalf("the effort of the node: %q", got)
	}
	// What the owner set for the session holds first.
	if err := s.SetEffort(ctx, "s1", "low"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	se.agent = agents.Archivist
	s.mu.Unlock()
	if got := s.effort(se); got != "low" {
		t.Fatalf("the effort the owner set: %q", got)
	}
}
