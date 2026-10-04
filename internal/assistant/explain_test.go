package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/diagnosis"
	"ouroboros/internal/openai"
)

// explain runs explain_error with id for the conversation of ctx.
func explain(ctx context.Context, tool agent.Tool, id string) (string, error) {
	args, _ := json.Marshal(explainErrorArgs{ID: id})
	return tool.Run(ctx, args)
}

// from is the context of a conversation with the peer publicKey.
func from(publicKey string) context.Context {
	return a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: publicKey})
}

// A peer gets the explanation of what happened to its own message, in general
// terms, and of nothing else.
func TestExplainError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failures.json")
	failures := diagnosis.Open(path)
	at := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)
	failures.Record(diagnosis.Entry{ID: "AAAA2222", Peer: "alice", ContextID: "ctx-secret", MessageID: "msg-secret", At: at, Cause: diagnosis.CauseLLMAuth})
	failures.Record(diagnosis.Entry{ID: "BBBB3333", Peer: "bob", At: at, Cause: diagnosis.CauseTimeout})
	tool := explainError(failures)
	if tool.Name != toolExplainError {
		t.Fatalf("tool %q", tool.Name)
	}

	// However the LLM writes the ID down.
	for _, id := range []string{"AAAA2222", " aaaa2222\n"} {
		got, err := explain(from("alice"), tool, id)
		if err != nil || !strings.Contains(got, "Error AAAA2222") || !strings.Contains(got, "2026-10-04 12:30 UTC") || !strings.Contains(got, diagnosis.CauseLLMAuth.Explain()) {
			t.Fatalf("explain(%q) = %q, %v", id, got, err)
		}
		for _, internal := range []string{"ctx-secret", "msg-secret", "alice", path} {
			if strings.Contains(got, internal) {
				t.Fatalf("the explanation tells %q: %q", internal, got)
			}
		}
	}

	// What happened to bob is not found for alice, like an ID nobody got.
	other, err := explain(from("alice"), tool, "BBBB3333")
	unknown, _ := explain(from("alice"), tool, "CCCC4444")
	if err != nil || !strings.Contains(other, "No failed answer with the error ID BBBB3333") || strings.Contains(other, diagnosis.CauseTimeout.Explain()) {
		t.Fatalf("another peer's error: %q, %v", other, err)
	}
	if strings.ReplaceAll(unknown, "CCCC4444", "BBBB3333") != other {
		t.Fatalf("an ID of another peer can be told from an unknown one:\n%q\n%q", other, unknown)
	}

	// The owner has the log; without a peer there is nobody to look up for.
	if _, err := explain(context.Background(), tool, "AAAA2222"); err == nil {
		t.Fatal("explain_error without a peer should fail")
	}
	if _, err := explain(from("alice"), tool, " "); err == nil || !strings.Contains(err.Error(), `needs "id"`) {
		t.Fatalf("without an ID: %v", err)
	}

	// A list that cannot be read: the peer does not learn where it is.
	os.WriteFile(path, []byte("nope"), 0o600)
	if _, err := explain(from("alice"), tool, "AAAA2222"); err == nil || strings.Contains(err.Error(), "failures.json") || strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("unreadable list: %v", err)
	}
}

// promptLLM answers every message with the system prompt and the names of
// the tools it was offered.
type promptLLM struct{}

func (promptLLM) Complete(_ context.Context, _ string, msgs []openai.Message, tools []openai.Tool) (openai.Message, error) {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Function.Name
	}
	return openai.Message{Content: msgs[0].Content + "\ntools: " + strings.Join(names, ",")}, nil
}

// The agent that answers peers can explain failures whatever tools the owner
// allows peers, and knows when to; without a list of failures it cannot.
func TestPeerAgentExplainsErrors(t *testing.T) {
	cfg := config.Config{BaseURL: "http://x", Model: "m", Name: "n"}
	b := Builder{
		LLM:   func(agent.Endpoint) agent.LLM { return promptLLM{} },
		Tools: func() []agent.Tool { return PeerTools(nil, func() string { return "" }) },
	}
	ask := func() string {
		t.Helper()
		bot, err := b.ForPeers(cfg)
		if err != nil {
			t.Fatal(err)
		}
		got, err := bot.Respond(context.Background(), "c", "hi")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := ask(); strings.Contains(got, toolExplainError) || !strings.HasSuffix(got, "tools: ") {
		t.Fatalf("without failures: %q", got)
	}

	b.Failures = diagnosis.Open(filepath.Join(t.TempDir(), "failures.json"))
	if got := ask(); !strings.Contains(got, "look it up with "+toolExplainError) || !strings.HasSuffix(got, "tools: "+toolExplainError) {
		t.Fatalf("no tools for peers: %q", got)
	}
	cfg.Tools = &config.Tools{A2A: []string{config.AllTools}}
	if got := ask(); !strings.Contains(got, "look it up with "+toolExplainError) || !strings.HasSuffix(got, "tools: list_peers,ask_peer,"+toolExplainError) {
		t.Fatalf("all tools for peers: %q", got)
	}

	// It is not a tool the owner lists, and not one of the owner's agent.
	cfg.Tools = &config.Tools{A2A: []string{toolExplainError}}
	if _, err := b.ForPeers(cfg); err == nil {
		t.Fatal("explain_error is not among the tools to pick from")
	}
	cfg.Tools = nil
	owner, err := b.forOwner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := owner.Respond(context.Background(), "c", "hi")
	if !strings.HasSuffix(got, "tools: list_peers,ask_peer") {
		t.Fatalf("the owner's agent: %q", got)
	}
	// It knows that an error ID can be asked about, and that nothing is known
	// before the peer answers.
	for _, want := range []string{"error ID", "once it is able to answer again", "do not promise or make up an explanation"} {
		if !strings.Contains(got, want) {
			t.Errorf("the owner's prompt lacks %q:\n%s", want, got)
		}
	}
}
