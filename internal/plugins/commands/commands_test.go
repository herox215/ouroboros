package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/credentials"
	"ouroboros/internal/home"
	"ouroboros/internal/llm"
	"ouroboros/plugin"
)

// node is a configured node with one access that offers old and new.
func node(t *testing.T) (home.Dir, *plugin.Owner, credentials.Access) {
	t.Helper()
	dir := home.Dir(t.TempDir())
	a, err := dir.Credentials().Put(credentials.Access{Provider: "quota", Name: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := `{"name":"alpha","access":"` + a.ID + `","model":"old"}`
	if err := os.WriteFile(filepath.Join(string(dir), "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, plugin.OwnerOf(dir, nil), a
}

// quota lists the models and tells a balance, so that usage has something to
// report.
func quota() plugin.Provider {
	return plugin.Provider{
		Name: "quota",
		Models: func(context.Context, llm.Access) (llm.Models, error) {
			return llm.Models{Chat: []string{"old", "new"}}, nil
		},
		Usage: func(context.Context, llm.Access) (llm.Usage, error) {
			return llm.Usage{Balances: []llm.UsageBalance{{Currency: "USD", Amount: "1.00"}}}, nil
		},
	}
}

// answer is a conversation with an owner who says answer, and counts the
// questions asked.
func answer(ctx context.Context, text string, asked *int) context.Context {
	return agent.WithOwner(ctx, func(_ context.Context, _ json.RawMessage) (string, error) {
		*asked++
		return text, nil
	})
}

// change_model asks the owner before it changes anything, and applies only
// on Apply change.
func TestChangeModel(t *testing.T) {
	dir, owner, a := node(t)
	tl := tool(owner, func() []plugin.Provider { return []plugin.Provider{quota()} })
	cfg, err := tl.Configure(json.RawMessage(`{}`))
	if err != nil || !strings.Contains(cfg.Schema, "change_model") || !strings.Contains(cfg.Schema, "usage") {
		t.Fatalf("defaults: %+v, %v", cfg, err)
	}
	var asked int
	var question string
	ctx := agent.WithOwner(context.Background(), func(_ context.Context, q json.RawMessage) (string, error) {
		asked++
		question = string(q)
		return "Apply change", nil
	})
	out, err := cfg.Run(ctx, json.RawMessage(`{"command":"change_model","model":"new"}`))
	if err != nil || !strings.Contains(out, `"new"`) || asked != 1 {
		t.Fatalf("out %q, asked %d, %v", out, asked, err)
	}
	if !strings.Contains(question, "Change the model of you") || !strings.Contains(question, "new") {
		t.Fatalf("question %s", question)
	}
	got, err := dir.Config()
	if err != nil || got.Model != "new" || got.Access != a.ID {
		t.Fatalf("config %+v, %v", got, err)
	}
}

// A rejected change leaves the model as it was.
func TestChangeModelRejected(t *testing.T) {
	dir, owner, _ := node(t)
	tl := tool(owner, func() []plugin.Provider { return []plugin.Provider{quota()} })
	cfg, _ := tl.Configure(json.RawMessage(`{}`))
	var asked int
	out, err := cfg.Run(answer(context.Background(), "Reject change", &asked),
		json.RawMessage(`{"command":"change_model","model":"new"}`))
	if err != nil || !strings.Contains(out, "not changed") || asked != 1 {
		t.Fatalf("out %q, asked %d, %v", out, asked, err)
	}
	if got, _ := dir.Config(); got.Model != "old" {
		t.Fatalf("model %q", got.Model)
	}
}

// A model the access does not offer is refused before the owner is asked,
// and nothing is changed.
func TestChangeModelUnknown(t *testing.T) {
	dir, owner, _ := node(t)
	tl := tool(owner, func() []plugin.Provider { return []plugin.Provider{quota()} })
	cfg, _ := tl.Configure(json.RawMessage(`{}`))
	var asked int
	_, err := cfg.Run(answer(context.Background(), "Apply change", &asked),
		json.RawMessage(`{"command":"change_model","model":"nope"}`))
	if err == nil || !strings.Contains(err.Error(), "does not offer") || !strings.Contains(err.Error(), "old, new") || asked != 0 {
		t.Fatalf("err %v, asked %d", err, asked)
	}
	if got, _ := dir.Config(); got.Model != "old" {
		t.Fatalf("model %q", got.Model)
	}
}

// Only the commands the settings name are offered, and one it does not know
// keeps the node from starting.
func TestSettings(t *testing.T) {
	_, owner, _ := node(t)
	tl := tool(owner, func() []plugin.Provider { return nil })
	cfg, err := tl.Configure(json.RawMessage(`{"commands":["usage"]}`))
	if err != nil || !strings.Contains(cfg.Schema, `"enum":["usage"]`) || !cfg.OwnerOnly {
		t.Fatalf("restricted: %+v, %v", cfg, err)
	}
	if _, err := cfg.Run(context.Background(), json.RawMessage(`{"command":"change_model","model":"new"}`)); err == nil || !strings.Contains(err.Error(), "no such command") {
		t.Fatalf("refused: %v", err)
	}
	if _, err := tl.Configure(json.RawMessage(`{"commands":["nope"]}`)); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unknown: %v", err)
	}
}

// usage is what /usage shows: only the access the sessions and the thread
// are answered through now, with both models when they differ.
func TestUsage(t *testing.T) {
	_, owner, _ := node(t)
	tl := tool(owner, func() []plugin.Provider { return []plugin.Provider{quota()} })
	cfg, err := tl.Configure(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := cfg.Run(context.Background(), json.RawMessage(`{"command":"usage"}`))
	if err != nil || !strings.Contains(out, "Work · sessions: old · thread: old") || !strings.Contains(out, "balance: 1.00 USD") {
		t.Fatalf("out %q, %v", out, err)
	}
}
