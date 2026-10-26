package assistant

import (
	"context"
	"errors"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/identity"
)

// Model tells the model of the scope: the one the owner chose for it, or
// else their own.
func TestModelOfAScope(t *testing.T) {
	cfg := config.Config{Access: "a", Model: "m", Name: "me", PrivateKey: identity.Generate().Private(),
		SessionAccess: "b", SessionModel: "s", ThreadAccess: "c", ThreadModel: "t"}
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{}, discard)
	if got := a.Model(false); got != "s" {
		t.Errorf("sessions answer with %q", got)
	}
	if got := a.Model(true); got != "t" {
		t.Errorf("the thread answers with %q", got)
	}
}

// Each scope has its own agent, built for its access and model, and kept
// until its choice changes; the other scope is not built anew then.
func TestAgentsOfScopes(t *testing.T) {
	cfg := config.Config{Access: "a", Model: "m", Name: "me", PrivateKey: identity.Generate().Private(),
		SessionAccess: "b", SessionModel: "s", ThreadAccess: "c", ThreadModel: "t",
		Fallbacks: []config.Fallback{{Access: "f", Model: "small"}}}
	built := map[string]int{}
	b := Builder{
		LLM: func(access string) (agent.LLM, error) {
			built[access]++
			return &accessLLM{name: access}, nil
		},
		Tools: func() []agent.Tool { return nil },
	}
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	sessions, scfg, err := a.current(false, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if scfg.Access != "b" || scfg.Model != "s" {
		t.Fatalf("sessions answer with %q of %q", scfg.Model, scfg.Access)
	}
	thread, tcfg, err := a.current(true, "thread")
	if err != nil {
		t.Fatal(err)
	}
	if tcfg.Access != "c" || tcfg.Model != "t" {
		t.Fatalf("the thread answers with %q of %q", tcfg.Model, tcfg.Access)
	}
	if sessions == thread {
		t.Fatal("one agent answers both scopes")
	}
	if built["b"] != 1 || built["c"] != 1 {
		t.Fatalf("built %v", built)
	}
	// What is asked again is not built again.
	if bot, _, _ := a.current(false, "s1"); bot != sessions || built["b"] != 1 {
		t.Fatalf("the sessions were built anew: %v", built)
	}
	if bot, _, _ := a.current(true, "thread"); bot != thread || built["c"] != 1 {
		t.Fatalf("the thread was built anew: %v", built)
	}
	// Another choice for the sessions builds their agent anew, not the thread's.
	cfg.SessionModel = "s2"
	if bot, _, _ := a.current(false, "s1"); bot == sessions || built["b"] != 2 {
		t.Fatalf("the sessions: built %v", built)
	}
	if built["c"] != 1 {
		t.Fatalf("the thread was built anew: %v", built)
	}
	// The node's fallbacks are part of what both are built from.
	cfg.Fallbacks = append(cfg.Fallbacks, config.Fallback{Access: "g", Model: "small"})
	a.current(false, "s1")
	a.current(true, "thread")
	if built["b"] != 3 || built["c"] != 2 {
		t.Fatalf("the fallbacks: built %v", built)
	}
}

// The thread that has no model of its own answers with the node's, even when
// the sessions have one of theirs: its agent is kept apart.
func TestThreadOfTheNode(t *testing.T) {
	cfg := config.Config{Access: "a", Model: "m", Name: "me", PrivateKey: identity.Generate().Private(),
		SessionAccess: "b", SessionModel: "s"}
	built := map[string]int{}
	b := Builder{
		LLM: func(access string) (agent.LLM, error) {
			built[access]++
			return &accessLLM{name: access}, nil
		},
		Tools: func() []agent.Tool { return nil },
	}
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	sessions, _, err := a.current(false, "s1")
	if err != nil {
		t.Fatal(err)
	}
	thread, tcfg, err := a.current(true, "thread")
	if err != nil {
		t.Fatal(err)
	}
	if tcfg.Access != "a" || tcfg.Model != "m" || built["a"] != 1 {
		t.Fatalf("the thread answers with %q of %q, built %v", tcfg.Model, tcfg.Access, built)
	}
	if sessions == thread {
		t.Fatal("the thread answers with the agent of the sessions")
	}
}

// Note is kept by the agent of the scope the context tells: an access of the
// sessions that cannot answer does not keep the thread from keeping what it
// showed.
func TestNoteFollowsTheScope(t *testing.T) {
	cfg := config.Config{Access: "a", Model: "m", Name: "me", PrivateKey: identity.Generate().Private(),
		SessionAccess: "gone", SessionModel: "s", ThreadAccess: "c", ThreadModel: "t"}
	b := Builder{
		LLM: func(access string) (agent.LLM, error) {
			if access == "gone" {
				return nil, errors.New("no access")
			}
			return &accessLLM{name: access}, nil
		},
		Tools: func() []agent.Tool { return nil },
	}
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	if err := a.Note(context.Background(), "s1", "hi"); err == nil {
		t.Fatal("the sessions have no access to keep it")
	}
	if err := a.Note(WithThread(context.Background(), &fakeThread{}), "thread", "shown"); err != nil {
		t.Fatalf("the thread cannot keep what it showed: %v", err)
	}
}

// Another access clears what the LLM thought in the conversations of its
// scope alone: the other scope answers on with its own.
func TestAccessChangeUnthinksItsScope(t *testing.T) {
	cfg := config.Config{Access: "a", Model: "m", Name: "me", PrivateKey: identity.Generate().Private(),
		SessionAccess: "b", SessionModel: "s", ThreadAccess: "c", ThreadModel: "t"}
	b := Builder{
		LLM:   func(access string) (agent.LLM, error) { return &accessLLM{name: access}, nil },
		Tools: func() []agent.Tool { return nil },
	}
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	ctx := context.Background()
	if _, err := a.Ask(ctx, "s1", "hi", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ask(WithThread(ctx, &fakeThread{}), "thread", "hi", nil); err != nil {
		t.Fatal(err)
	}
	reasoning := func(id string) string {
		msgs := a.memory.Conversation(id)
		if len(msgs) == 0 {
			return ""
		}
		return msgs[len(msgs)-1].Reasoning
	}
	if reasoning("s1") == "" || reasoning("thread") == "" {
		t.Fatalf("no reasoning was kept: %q, %q", reasoning("s1"), reasoning("thread"))
	}
	// The access of the sessions changes: only their conversations are cleared.
	cfg.SessionAccess, cfg.SessionModel = "d", "s3"
	if _, _, err := a.current(false, "s1"); err != nil {
		t.Fatal(err)
	}
	if reasoning("s1") != "" {
		t.Fatalf("the sessions think on: %q", reasoning("s1"))
	}
	if reasoning("thread") == "" {
		t.Fatal("the thread lost what it thought")
	}
	// The access of the thread changes: the sessions keep what they thought.
	if _, err := a.Ask(ctx, "s2", "hi", nil); err != nil {
		t.Fatal(err)
	}
	kept := reasoning("s2")
	cfg.ThreadAccess, cfg.ThreadModel = "e", "t2"
	if _, _, err := a.current(true, "thread"); err != nil {
		t.Fatal(err)
	}
	if reasoning("thread") != "" {
		t.Fatalf("the thread thinks on: %q", reasoning("thread"))
	}
	if reasoning("s2") != kept || kept == "" {
		t.Fatalf("the sessions lost what they thought: %q, was %q", reasoning("s2"), kept)
	}
}
