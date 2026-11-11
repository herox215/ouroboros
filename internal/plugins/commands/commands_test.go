package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/credentials"
	"ouroboros/internal/home"
	"ouroboros/internal/llm"
	"ouroboros/internal/session"
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

// change_model changes the model without asking itself: the permission rule
// of the tool does that, see config.DefaultAsk.
func TestChangeModel(t *testing.T) {
	dir, owner, a := node(t)
	tl := tool(owner, func() []plugin.Provider { return []plugin.Provider{quota()} }, nil)
	cfg, err := tl.Configure(json.RawMessage(`{}`))
	if err != nil || !strings.Contains(cfg.Schema, "change_model") || !strings.Contains(cfg.Schema, "usage") {
		t.Fatalf("defaults: %+v, %v", cfg, err)
	}
	var asked int
	out, err := cfg.Run(answer(context.Background(), "", &asked), json.RawMessage(`{"command":"change_model","model":"new"}`))
	if err != nil || !strings.Contains(out, `"new"`) || asked != 0 {
		t.Fatalf("out %q, asked %d, %v", out, asked, err)
	}
	got, err := dir.Config()
	if err != nil || got.Model != "new" || got.Access != a.ID {
		t.Fatalf("config %+v, %v", got, err)
	}
}

// A model the access does not offer is refused before the owner is asked,
// and nothing is changed.
func TestChangeModelUnknown(t *testing.T) {
	dir, owner, _ := node(t)
	tl := tool(owner, func() []plugin.Provider { return []plugin.Provider{quota()} }, nil)
	cfg, _ := tl.Configure(json.RawMessage(`{}`))
	var asked int
	_, err := cfg.Run(answer(context.Background(), "", &asked),
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
	tl := tool(owner, func() []plugin.Provider { return nil }, nil)
	cfg, err := tl.Configure(json.RawMessage(`{"commands":["usage"]}`))
	if err != nil || !strings.Contains(cfg.Schema, `"enum":["usage"]`) || !cfg.OwnerOnly || !cfg.ThreadOnly {
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
	tl := tool(owner, func() []plugin.Provider { return []plugin.Provider{quota()} }, nil)
	cfg, err := tl.Configure(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := cfg.Run(context.Background(), json.RawMessage(`{"command":"usage"}`))
	if err != nil || !strings.Contains(out, "Work · sessions: old · thread: old") || !strings.Contains(out, "balance: 1.00 USD") {
		t.Fatalf("out %q, %v", out, err)
	}
}

// held is what usage reads of the sessions: the conversations to name, and
// what one of them holds of its context.
type held struct {
	info  []session.Info
	usage map[string]session.ContextUsage
}

func (h held) List(context.Context) ([]session.Info, error) { return h.info, nil }

func (h held) ContextOf(_ context.Context, id string) (session.ContextUsage, error) {
	u, ok := h.usage[id]
	if !ok {
		return session.ContextUsage{}, session.ErrNoSession
	}
	return u, nil
}

// TestUsageOfASession is usage with a session named: the context of that
// one, by its ID, the start of it or its title.
func TestUsageOfASession(t *testing.T) {
	_, owner, _ := node(t)
	h := held{
		info: []session.Info{
			{ID: "abc123", Title: "Merge the fixes"},
			{ID: "abc456", Title: "Another one"},
		},
		usage: map[string]session.ContextUsage{
			"abc123": {ID: "abc123", Title: "Merge the fixes", Model: "old", Bytes: 6 * 1024,
				Cache: assistant.Cache{Tokens: 12345, Cached: 10000}},
		},
	}
	cfg, err := tool(owner, func() []plugin.Provider { return []plugin.Provider{quota()} }, h).Configure(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `session abc123 "Merge the fixes" · old · 12.3k tokens sent with the last call, 10.0k cached (81%) · context 6.0 KiB`
	for _, query := range []string{"abc123", "abc1", "merge the fixes"} {
		in, _ := json.Marshal(map[string]string{"command": "usage", "session": query})
		out, err := cfg.Run(context.Background(), in)
		if err != nil || !strings.Contains(out, "balance: 1.00 USD") || !strings.Contains(out, want) {
			t.Fatalf("%q: out %q, %v", query, out, err)
		}
	}
	out, err := contextText(context.Background(), h, "abc123", "")
	if err != nil || !strings.Contains(out, `this conversation "Merge the fixes" · old · 12.3k tokens`) {
		t.Fatalf("current: out %q, %v", out, err)
	}
	if _, err := cfg.Run(context.Background(), json.RawMessage(`{"command":"usage","session":"abc"}`)); err == nil || !strings.Contains(err.Error(), "names 2 sessions") {
		t.Fatalf("ambiguous: %v", err)
	}
	if _, err := cfg.Run(context.Background(), json.RawMessage(`{"command":"usage","session":"nope"}`)); err == nil || !strings.Contains(err.Error(), "no session matches") {
		t.Fatalf("unknown: %v", err)
	}
}

// efforted records the efforts set in sessions.
type efforted struct{ id, effort string }

func (e *efforted) SetEffort(_ context.Context, id, effort string) error {
	e.id, e.effort = id, effort
	return nil
}

func (e *efforted) List(context.Context) ([]session.Info, error) { return nil, nil }

func (e *efforted) ContextOf(context.Context, string) (session.ContextUsage, error) {
	return session.ContextUsage{}, session.ErrNoSession
}

// change_effort sets the effort of the node, and "auto" leaves it to the
// model; it needs no question to the owner.
func TestChangeEffort(t *testing.T) {
	dir, owner, _ := node(t)
	cfg, err := tool(owner, func() []plugin.Provider { return []plugin.Provider{quota()} }, nil).Configure(json.RawMessage(`{}`))
	if err != nil || !strings.Contains(cfg.Schema, "change_effort") || !strings.Contains(cfg.Schema, `"effort"`) {
		t.Fatalf("defaults: %+v, %v", cfg, err)
	}
	var asked int
	out, err := cfg.Run(answer(context.Background(), "", &asked), json.RawMessage(`{"command":"change_effort","effort":"high"}`))
	if err != nil || !strings.Contains(out, "high") || !strings.Contains(out, "next message") || asked != 0 {
		t.Fatalf("out %q, asked %d, %v", out, asked, err)
	}
	if got, err := dir.Config(); err != nil || got.Effort != "high" {
		t.Fatalf("config %+v, %v", got, err)
	}
	if out, err = cfg.Run(context.Background(), json.RawMessage(`{"command":"change_effort","effort":"auto"}`)); err != nil || !strings.Contains(out, "auto") {
		t.Fatalf("auto: %q, %v", out, err)
	}
	if got, _ := dir.Config(); got.Effort != "" {
		t.Fatalf("effort %q", got.Effort)
	}
}

// An unknown effort, a missing one and an unknown scope fail, naming what
// there is, and change nothing.
func TestChangeEffortInvalid(t *testing.T) {
	dir, owner, _ := node(t)
	cfg, _ := tool(owner, func() []plugin.Provider { return []plugin.Provider{quota()} }, nil).Configure(json.RawMessage(`{}`))
	for in, want := range map[string]string{
		`{"command":"change_effort","effort":"huge"}`:                      "auto, none, low, medium, high, max",
		`{"command":"change_effort"}`:                                      "needs an effort",
		`{"command":"change_effort","effort":"low","scope":"nowhere"}`:     "node or session",
		`{"command":"change_effort","effort":" HIGH ","scope":" Nowhere"}`: "node or session",
	} {
		if _, err := cfg.Run(context.Background(), json.RawMessage(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", in, err)
		}
	}
	if got, _ := dir.Config(); got.Effort != "" {
		t.Fatalf("effort %q", got.Effort)
	}
}

// With the scope session the effort of the conversation the command runs in
// changes, and the node's stays.
func TestChangeEffortSession(t *testing.T) {
	dir, owner, _ := node(t)
	s := &efforted{}
	providers := func() []plugin.Provider { return []plugin.Provider{quota()} }
	cfg, _ := tool(owner, providers, s).Configure(json.RawMessage(`{}`))
	out, err := changeEffort(context.Background(), owner, providers, s, "thread-1", args{Effort: "low", Scope: "session"})
	if err != nil || !strings.Contains(out, "this conversation") || s.id != "thread-1" || s.effort != "low" {
		t.Fatalf("out %q, %+v, %v", out, s, err)
	}
	if got, _ := dir.Config(); got.Effort != "" {
		t.Fatalf("node effort %q", got.Effort)
	}
	if _, err := cfg.Run(context.Background(), json.RawMessage(`{"command":"change_effort","effort":"low","scope":"session"}`)); err == nil {
		t.Fatal("no conversation: want an error")
	}
}

// The setting can leave change_effort out.
func TestChangeEffortSetting(t *testing.T) {
	_, owner, _ := node(t)
	cfg, err := tool(owner, func() []plugin.Provider { return nil }, nil).Configure(json.RawMessage(`{"commands":["usage"]}`))
	if err != nil || !strings.Contains(cfg.Schema, `"enum":["usage"]`) {
		t.Fatalf("%+v, %v", cfg, err)
	}
	if _, err := cfg.Run(context.Background(), json.RawMessage(`{"command":"change_effort","effort":"low"}`)); err == nil || !strings.Contains(err.Error(), "no such command") {
		t.Fatalf("refused: %v", err)
	}
}

// Where models.dev lists the model, only the efforts it takes are accepted,
// and a refused one names them.
func TestChangeEffortListed(t *testing.T) {
	dir, owner, _ := node(t)
	q := quota()
	q.Catalog = func(llm.Access) string { return "quota" }
	listed := fmt.Sprintf(`{"at": %q, "providers": {"quota": {"old": {"efforts": ["low", "max"]}}}}`, time.Now().Format(time.RFC3339))
	if err := os.WriteFile(dir.Catalog(), []byte(listed), 0o600); err != nil {
		t.Fatal(err)
	}
	providers := func() []plugin.Provider { return []plugin.Provider{q} }
	_, err := changeEffort(context.Background(), owner, providers, nil, "", args{Effort: "high"})
	if err == nil || !strings.Contains(err.Error(), "does not take") || !strings.Contains(err.Error(), "auto, low, max") {
		t.Fatalf("err %v", err)
	}
	if _, err := changeEffort(context.Background(), owner, providers, nil, "", args{Effort: "max"}); err != nil {
		t.Fatal(err)
	}
}

// failing is a session service that refuses every effort.
type failing struct{ efforted }

func (failing) SetEffort(context.Context, string, string) error { return session.ErrNoEffort }

// What cannot be set is reported: the node without a configuration, and a
// session that refuses.
func TestChangeEffortFails(t *testing.T) {
	providers := func() []plugin.Provider { return nil }
	bare := plugin.OwnerOf(home.Dir(t.TempDir()), nil)
	if _, err := changeEffort(context.Background(), bare, providers, nil, "", args{Effort: "low"}); err == nil {
		t.Fatal("unconfigured node: want an error")
	}
	_, owner, _ := node(t)
	if _, err := changeEffort(context.Background(), owner, providers, failing{}, "x", args{Effort: "low", Scope: "session"}); err == nil {
		t.Fatal("session: want an error")
	}
}
