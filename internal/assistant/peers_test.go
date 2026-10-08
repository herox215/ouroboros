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
	"ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
)

// The peers of a node are answered with its configuration as it is now: a
// change holds from the next message on, and the agent stays while nothing
// it is built from changes.
func TestPeersFollowTheConfiguration(t *testing.T) {
	first, second := openaitest.NewServerWithKey(t, ""), openaitest.NewServerWithKey(t, "")
	cfg := config.Config{Access: first, Model: "gpt-4o", Name: "me", PrivateKey: identity.Generate().Private()}
	var failing error
	var logged []string
	built := 0
	known := &accesses{rev: "1"}
	b := Builder{
		LLM:      func(access string) (agent.LLM, error) { built++; return overHTTP(access) },
		Accesses: known,
		Tools:    func() []agent.Tool { return PeerTools(nil, func() string { return "" }) },
	}
	peers := b.Peers(func() (config.Config, error) { return cfg, failing }, func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	conversation := "c"
	ask := func(text string) string {
		t.Helper()
		got, err := peers.Respond(from("luna"), conversation, text)
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
	peers.Forget(conversation) // and asked anew, it starts anew
	if got := ask("hello"); got != "[gpt-4o] turns=1 last=hello" {
		t.Fatalf("the agent should have forgotten the conversation: %q", got)
	}
	ask("hello")
	// What does not concern the agent leaves it alone.
	cfg.Tools = &config.Tools{H2A: config.Named("list_peers"), Protected: &[]string{}}
	if got := ask("hello"); got != "[gpt-4o] turns=5 last=hello" || built != 1 || len(logged) != 0 {
		t.Fatalf("got %q, %d agents, log %v", got, built, logged)
	}

	var last string // what the agent answered after the change before
	for i, change := range []func(){
		// Another access: what the LLM thought so far is not sent there.
		func() {
			peers.memory.Restore("thinking", []openai.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "ho", Reasoning: "hm"}})
			cfg.Access = second
		},
		// The owner signed in to the access anew: the thoughts stay.
		func() {
			if kept := peers.memory.Conversation("thinking"); len(kept) != 2 || kept[1].Reasoning != "" || kept[1].Content != "ho" {
				t.Fatalf("another access was sent the thoughts: %+v", kept)
			}
			peers.memory.Restore("thinking", []openai.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "ho", Reasoning: "hm"}})
			known.rev = "2"
		},
		func() {
			if kept := peers.memory.Conversation("thinking"); kept[1].Reasoning != "hm" {
				t.Fatalf("the thoughts are gone with the same access: %+v", kept)
			}
			cfg.Model = "gpt-4o-mini"
		},
		// A model of their own for peers, of another access: the one of
		// the owner is theirs no more.
		func() {
			if !strings.HasPrefix(last, "[gpt-4o-mini] ") {
				t.Fatalf("peers are answered like the owner until then: %q", last)
			}
			peers.memory.Restore("thinking", []openai.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "ho", Reasoning: "hm"}})
			cfg.PeerAccess, cfg.PeerModel = first, "o3-mini"
		},
		func() {
			if kept := peers.memory.Conversation("thinking"); kept[1].Reasoning != "" {
				t.Fatalf("the access of the peers was sent the thoughts: %+v", kept)
			}
			if !strings.HasPrefix(last, "[o3-mini] ") {
				t.Fatalf("peers are answered with their model: %q", last)
			}
			cfg.PeerModel = "gpt-4o"
		},
		func() {
			if !strings.HasPrefix(last, "[gpt-4o] ") {
				t.Fatalf("peers are answered with their new model: %q", last)
			}
			cfg.PeerAccess, cfg.PeerModel = "", ""
		},
		func() {
			if !strings.HasPrefix(last, "[gpt-4o-mini] ") {
				t.Fatalf("peers are answered like the owner again: %q", last)
			}
			cfg.Description = "of the owner" // nothing the agent is built from
			cfg.Name = "you"
		},
		func() { cfg.PrivateKey = identity.Generate().Private() },
		func() { cfg.Tools = &config.Tools{A2A: config.Named("list_peers")} },
		func() { cfg.Tools = &config.Tools{A2A: []config.Entry{{Tool: "list_peers", Settings: []byte(`{}`)}}} },
	} {
		change()
		// The new agent knows the conversation of the one before.
		if got, want := ask("hello"), fmt.Sprintf(" turns=%d last=hello", 7+4*i); !strings.HasSuffix(got, want) || built != i+2 {
			t.Fatalf("change %d: %q, %d agents", i, got, built)
		}
		last = ask("hello")
		if want := fmt.Sprintf(" turns=%d last=hello", 9+4*i); !strings.HasSuffix(last, want) || built != i+2 {
			t.Fatalf("change %d, again: %q, %d agents", i, last, built)
		}
	}
	if len(logged) != 10 || logged[0] != "the configuration changed: peers are answered by a new agent" {
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
	conversation, turns := "broken", 1 // one that is far from the limit of its history
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
	cfg := config.Config{Access: "http://x", Model: "m", Name: "n"}
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
