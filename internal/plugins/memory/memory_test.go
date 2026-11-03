package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	store "ouroboros/internal/memory"
	"ouroboros/internal/openai"
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

func open(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func load(t *testing.T, s *store.Store, args string) []store.Entry {
	t.Helper()
	out := call(t, s, context.Background(), "owner", Load, args)
	var found []store.Entry
	if err := json.Unmarshal([]byte(out), &found); err != nil {
		t.Fatalf("load %s: %s: %v", args, out, err)
	}
	return found
}

// The plugin has the three tools load, add and forget, and no others.
func TestThreeTools(t *testing.T) {
	var names []string
	for _, tool := range tools(nil) {
		names = append(names, tool.Name)
	}
	if got := strings.Join(names, " "); got != Load+" "+Add+" "+Forget {
		t.Fatalf("tools: %s", got)
	}
}

// Add and load need no project directory: a topic of a new name is created,
// the same key overwrites, the source is the trusted conversation, and
// forget deletes.
func TestAddLoadForgetWithoutDirectory(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	out := call(t, s, ctx, "trusted-session", Add, `{"topic":"Tea","key":"preference","content":"green tea"}`)
	var e store.Entry
	if err := json.Unmarshal([]byte(out), &e); err != nil {
		t.Fatalf("add %q: %v", out, err)
	}
	if e.Topic != "Tea" || e.Source != "trusted-session" || e.Version != 1 {
		t.Fatalf("untrusted metadata: %+v", e)
	}
	if topics, err := s.Topics(ctx); err != nil || len(topics) != 1 || topics[0].Name != "Tea" || topics[0].Count != 1 {
		t.Fatalf("new topic: %+v, %v", topics, err)
	}
	for _, bad := range []string{`{"topic":"t","key":"x","content":"y","project":"/evil"}`, `{"topic":"t","key":"x","content":"y","source_session":"forged"}`, `{"topic":"t","key":"x","content":"y","record":12}`} {
		if got := call(t, s, ctx, "s", Add, bad); !strings.HasPrefix(got, "error:") {
			t.Fatalf("forged source/path accepted: %s", got)
		}
	}
	out = call(t, s, ctx, "replacement-session", Add, `{"topic":"tea","key":"preference","content":"black tea"}`)
	if err := json.Unmarshal([]byte(out), &e); err != nil || e.Source != "replacement-session" || e.Version != 2 || e.Topic != "Tea" {
		t.Fatalf("overwrite: %s %v", out, err)
	}
	if found := load(t, s, `{"topic":"TEA"}`); len(found) != 1 || found[0].Content != "black tea" {
		t.Fatalf("load after overwrite: %+v", found)
	}
	if found := load(t, s, `{"topic":"tea","search":"black"}`); len(found) != 1 || found[0].Key != "preference" {
		t.Fatalf("search: %+v", found)
	}
	if found := load(t, s, `{"topic":"other"}`); len(found) != 0 {
		t.Fatalf("another topic: %+v", found)
	}
	if out = call(t, s, ctx, "s", Forget, `{"topic":"tea","key":"preference"}`); out != "Memory forgotten." {
		t.Fatal(out)
	}
	if found := load(t, s, `{"topic":"tea"}`); len(found) != 0 {
		t.Fatalf("forgotten memory loaded: %+v", found)
	}
	if out = call(t, s, ctx, "s", Forget, `{"topic":"tea","key":"preference"}`); !strings.HasPrefix(out, "error:") || !strings.Contains(out, "not found") {
		t.Fatalf("forget twice: %s", out)
	}
}

// What a tool cannot do is an error the model is told, and nothing is
// changed; core has a budget.
func TestToolsRefuseWhatTheyCannotDo(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	call(t, s, ctx, "owner", Add, `{"topic":"core","key":"name","content":"Lu"}`)
	for _, tt := range []struct{ name, args, want string }{
		{Add, `{"topic":"core","key":"long","content":"` + strings.Repeat("x", store.MaxCore) + `"}`, "core memories are full"},
		{Add, `{"topic":"` + strings.Repeat("t", store.MaxTopic+1) + `","key":"k","content":"c"}`, "topic"},
		{Add, `{"topic":"t","key":"","content":"c"}`, "memory key"},
		{Add, `{"topic":"t","key":"k","content":""}`, "memory content"},
		{Load, `{"topic":"t","search":"` + strings.Repeat("w", 1025) + `"}`, "1024 bytes"},
		{Forget, `{"topic":"core","key":"missing"}`, "not found"},
	} {
		if got := call(t, s, ctx, "owner", tt.name, tt.args); !strings.HasPrefix(got, "error:") || !strings.Contains(got, tt.want) {
			t.Errorf("%s %.80s: %q, want an error with %q", tt.name, tt.args, got, tt.want)
		}
	}
	if core, err := s.CoreMemories(ctx); err != nil || len(core) != 1 || core[0].Content != "Lu" {
		t.Fatalf("refused calls changed core: %+v, %v", core, err)
	}
	if topics, err := s.Topics(ctx); err != nil || len(topics) != 1 {
		t.Fatalf("refused calls created topics: %+v, %v", topics, err)
	}
}

func TestPeerAndMissingConversationRejected(t *testing.T) {
	for _, tool := range tools(nil) {
		if !tool.OwnerOnly || !json.Valid([]byte(tool.Schema)) || !strings.Contains(tool.Description, "when in doubt") {
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

// What a search of load finds counts as used; a load without one does not.
func TestSearchCountsAsUsed(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	e, _, err := s.Add(ctx, "drinks", "drink", "oolong", "owner", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		args string
		used int64
	}{
		{`{"topic":"drinks"}`, 0},
		{`{"topic":"drinks","search":"oolong"}`, 1},
	} {
		load(t, s, step.args)
		if got, err := s.Read(ctx, e.ID); err != nil || got.Used != step.used || got.Version != 1 {
			t.Fatalf("used after %s: %+v, %v", step.args, got, err)
		}
	}
}
