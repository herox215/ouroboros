package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/identity"
	"ouroboros/internal/openai/openaitest"
)

// The peers of a node are answered with its configuration as it is now: a
// change holds from the next message on, and the agent stays while nothing
// it is built from changes.
func TestPeersFollowTheConfiguration(t *testing.T) {
	cfg := config.Config{BaseURL: openaitest.NewServerWithKey(t, ""), APIKey: "sk-1", Model: "gpt-4o", Name: "me", PrivateKey: identity.Generate().Private()}
	var failing error
	var logged []string
	built := 0
	b := Builder{
		LLM:   func(at agent.Endpoint) agent.LLM { built++; return overHTTP(at) },
		Tools: func() []agent.Tool { return PeerTools(nil, func() string { return "" }) },
	}
	peers := b.Peers(func() (config.Config, error) { return cfg, failing }, func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	ask := func(text string) string {
		t.Helper()
		got, err := peers.Respond(from("luna"), "c", text)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if err := peers.Ready(); err != nil {
		t.Fatal(err)
	}
	if got := ask("hello"); got != "[gpt-4o] turns=1 last=hello" {
		t.Fatalf("got %q", got)
	}
	if got := ask("hello"); got != "[gpt-4o] turns=3 last=hello" {
		t.Fatalf("the agent forgot the conversation: %q", got)
	}
	// What does not concern the agent leaves it alone.
	cfg.Tools = &config.Tools{H2A: config.Named("list_peers"), Protected: &[]string{}}
	if got := ask("hello"); got != "[gpt-4o] turns=5 last=hello" || built != 1 || len(logged) != 0 {
		t.Fatalf("got %q, %d agents, log %v", got, built, logged)
	}

	for i, change := range []func(){
		func() { cfg.BaseURL += "/" },
		func() { cfg.APIKey = "sk-2" },
		func() { cfg.Model = "gpt-4o-mini" },
		func() { cfg.Name = "you" },
		func() { cfg.PrivateKey = identity.Generate().Private() },
		func() { cfg.Tools = &config.Tools{A2A: config.Named("list_peers")} },
		func() { cfg.Tools = &config.Tools{A2A: []config.Entry{{Tool: "list_peers", Settings: []byte(`{}`)}}} },
	} {
		change()
		if got := ask("hello"); !strings.HasSuffix(got, " turns=1 last=hello") || built != i+2 {
			t.Fatalf("change %d: %q, %d agents", i, got, built)
		}
		if got := ask("hello"); !strings.HasSuffix(got, " turns=3 last=hello") || built != i+2 {
			t.Fatalf("change %d, again: %q, %d agents", i, got, built)
		}
	}
	if len(logged) != 7 || logged[0] != "the configuration changed: peers are answered by a new agent" {
		t.Fatalf("log: %v", logged)
	}
	// The new agent is the one that answers: with the tools of the new list.
	if got := ask("?system"); !strings.Contains(got, `You are "you"`) || !strings.Contains(got, toolsHint) {
		t.Fatalf("the prompt: %q", got)
	}
	logged = nil

	// A configuration that cannot be used leaves the agent that is there, and
	// the log says why, once.
	broken := []struct {
		name   string
		breaks func()
		want   string
	}{
		{"unreadable", func() { failing = errTest }, "test error"},
		{"incomplete", func() { failing, cfg.Model = nil, "" }, "node is not configured"},
		{"a typo", func() { cfg.Model, cfg.Tools = "gpt-4o-mini", &config.Tools{A2A: config.Named("ask_peers")} }, `tools.a2a: unknown tool "ask_peers"`},
	}
	turns := 7
	for _, tt := range broken {
		tt.breaks()
		for range 2 {
			if got := ask("hello"); got != fmt.Sprintf("[gpt-4o-mini] turns=%d last=hello", turns) {
				t.Fatalf("%s: %q", tt.name, got)
			}
			turns += 2
		}
		if len(logged) != 1 || !strings.HasPrefix(logged[0], "the configuration cannot be used, peers are answered as before: "+tt.want) {
			t.Fatalf("%s: log %v", tt.name, logged)
		}
		if err := peers.Ready(); err != nil {
			t.Fatalf("%s: a node that runs is not ready: %v", tt.name, err)
		}
		logged = nil
	}
	// Mended, it is the agent it was, and the next mistake is told again.
	cfg.Tools = &config.Tools{A2A: []config.Entry{{Tool: "list_peers", Settings: []byte(`{}`)}}}
	if got := ask("hello"); got != fmt.Sprintf("[gpt-4o-mini] turns=%d last=hello", turns) || len(logged) != 0 {
		t.Fatalf("mended: %q, log %v", got, logged)
	}
	failing = errTest
	ask("hello")
	if len(logged) != 1 {
		t.Fatalf("the same mistake again: log %v", logged)
	}
}

// A node that has no agent yet does not answer with a configuration it
// cannot use: it does not start.
func TestPeersNeedAConfiguration(t *testing.T) {
	cfg := config.Config{BaseURL: "http://x", Model: "m", Name: "n"}
	var failing error
	b := Builder{LLM: overHTTP, Tools: func() []agent.Tool { return nil }}
	silent := func(format string, args ...any) { t.Errorf("logged: "+format, args...) }
	peers := b.Peers(func() (config.Config, error) { return cfg, failing }, silent)

	failing = errTest
	if err := peers.Ready(); !errors.Is(err, errTest) {
		t.Fatalf("unreadable: %v", err)
	}
	if got, err := peers.Respond(context.Background(), "c", "hello"); !errors.Is(err, errTest) || got != "" {
		t.Fatalf("unreadable: %q, %v", got, err)
	}
	failing, cfg.Name = nil, ""
	if err := peers.Ready(); !errors.Is(err, config.ErrIncomplete) {
		t.Fatalf("incomplete: %v", err)
	}
	cfg.Name, cfg.Tools = "n", &config.Tools{A2A: config.Named("nope")}
	if err := peers.Ready(); err == nil || !strings.Contains(err.Error(), "tools.a2a") {
		t.Fatalf("a typo: %v", err)
	}
	cfg.Tools = nil
	if err := peers.Ready(); err != nil {
		t.Fatalf("set up: %v", err)
	}
}
