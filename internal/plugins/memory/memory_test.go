package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	store "ouroboros/internal/memory"
	"ouroboros/internal/openai"
	"ouroboros/internal/workdir"
	"ouroboros/plugin"
)

type model struct{ name, args string }

func (m model) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	last := msgs[len(msgs)-1]
	if last.Role == "tool" {
		return openai.Message{Content: last.Content}, nil
	}
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: "call", Function: openai.FunctionCall{Name: m.name, Arguments: m.args}}}}, nil
}
func call(t *testing.T, s *store.Store, ctx context.Context, session, name, args string) string {
	t.Helper()
	return callTools(t, tools(s), ctx, session, name, args)
}

func callTools(t *testing.T, ts []plugin.Tool, ctx context.Context, session, name, args string) string {
	t.Helper()
	bot := agent.New(model{name, args}, "m", "", ts...)
	out, err := bot.Respond(ctx, session, "explicit user request")
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestOwnerToolsAndTrustedSourceProject(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := workdir.With(context.Background(), "/project/a")
	out := call(t, s, ctx, "trusted-session", Create, `{"key":"preference","content":"green tea","scope":"project"}`)
	var e store.Entry
	if err = json.Unmarshal([]byte(out), &e); err != nil {
		t.Fatalf("create %q: %v", out, err)
	}
	if e.Project != "/project/a" || e.Source != "trusted-session" || e.Version != 1 {
		t.Fatalf("untrusted metadata: %+v", e)
	}
	raw := `{"id":"` + e.ID + `","scope":"project"}`
	out = call(t, s, ctx, "read-session", Read, raw)
	if !strings.Contains(out, "green tea") {
		t.Fatal(out)
	}
	out = call(t, s, workdir.With(ctx, "/project/b"), "s", Read, raw)
	if !strings.Contains(out, "not found") {
		t.Fatalf("cross-project disclosure: %s", out)
	}
	out = call(t, s, workdir.With(ctx, "/project/b"), "s", Search, `{"query":"tea"}`)
	if out != "[]" {
		t.Fatalf("cross-project search: %s", out)
	}
	for _, bad := range []string{`{"key":"x","content":"y","project":"/evil"}`, `{"key":"x","content":"y","source_session":"forged"}`, `{"key":"x","content":"y","record":12}`} {
		if got := call(t, s, ctx, "s", Create, bad); !strings.HasPrefix(got, "error:") {
			t.Fatalf("forged source/path accepted: %s", got)
		}
	}
	out = call(t, s, ctx, "replacement-session", Update, `{"id":"`+e.ID+`","scope":"project","version":1,"key":"preference","content":"black tea"}`)
	if err = json.Unmarshal([]byte(out), &e); err != nil || e.Source != "replacement-session" || e.Version != 2 {
		t.Fatalf("update: %s %v", out, err)
	}
	out = call(t, s, ctx, "s", Forget, `{"id":"`+e.ID+`","scope":"project","version":2}`)
	if out != "Memory forgotten." {
		t.Fatal(out)
	}
}
func TestPeerAndMissingConversationRejected(t *testing.T) {
	for _, tool := range tools(nil) {
		if !tool.OwnerOnly || !json.Valid([]byte(tool.Schema)) || !strings.Contains(tool.Description, "When in doubt") {
			t.Fatalf("bad metadata: %+v", tool)
		}
		for _, ctx := range []context.Context{context.Background(), a2a.WithOrigin(context.Background(), a2a.Origin{}), a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"})} {
			if _, err := tool.Run(ctx, json.RawMessage(`{}`)); err == nil {
				t.Fatalf("unauthorized %s", tool.Name)
			}
		}
		// Calling the handler within an actual peer conversation must also fail,
		// independently of the agent's OwnerOnly advertisement/filtering.
		probe := agent.NewTool("probe", "", nil, func(ctx context.Context, _ struct{}) (string, error) { return tool.Run(ctx, json.RawMessage(`{}`)) })
		bot := agent.New(model{"probe", "{}"}, "m", "", probe)
		out, err := bot.Respond(a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"}), "peer-session", "hi")
		if err != nil || !strings.Contains(out, "owner-only") {
			t.Fatalf("peer handler %s: %s %v", tool.Name, out, err)
		}
	}
}
