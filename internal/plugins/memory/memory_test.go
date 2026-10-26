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

// A memory is only in a scope the conversation can name: global, or the
// trusted current project, which is an absolute path. What a tool cannot do
// is an error the model is told, and nothing is changed.
func TestToolsRefuseWhatTheyCannotDo(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	project := workdir.With(ctx, "/project/a")
	var e store.Entry
	if err := json.Unmarshal([]byte(call(t, s, ctx, "owner", Create, `{"key":"drink","content":"oolong"}`)), &e); err != nil {
		t.Fatal(err)
	}
	id := `"id":"` + e.ID + `"`
	for _, tt := range []struct {
		ctx        context.Context
		name, args string
		want       string
	}{
		{workdir.With(ctx, "relative/project"), Search, `{}`, "current project must be an absolute trusted path"},
		{ctx, Create, `{"key":"k","content":"c","scope":"project"}`, "no current project selected"},
		{project, Create, `{"key":"k","content":"c","scope":"everywhere"}`, "scope must be global, core or project"},
		{ctx, Create, `{"id":"x","key":"k","content":"c"}`, "create takes no ID or version"},
		{ctx, Create, `{"version":1,"key":"k","content":"c"}`, "create takes no ID or version"},
		{ctx, Update, `{` + id + `,"version":1,"key":"drink","content":"tea","scope":"project"}`, "no current project selected"},
		{ctx, Forget, `{` + id + `,"version":1,"scope":"project"}`, "no current project selected"},
		{ctx, Forget, `{"id":"missing","version":1}`, "memory not found"},
		{ctx, Read, `{` + id + `,"scope":"project"}`, "no current project selected"},
	} {
		if got := call(t, s, tt.ctx, "owner", tt.name, tt.args); !strings.HasPrefix(got, "error:") || !strings.Contains(got, tt.want) {
			t.Errorf("%s %s: %q, want an error with %q", tt.name, tt.args, got, tt.want)
		}
	}
	if got, err := s.Read(ctx, "", e.ID); err != nil || got != e {
		t.Fatalf("refused calls changed the memory: %+v, %v", got, err)
	}
}

// A memory of scope core is global and a core one; read, update and forget
// find it as a global one.
func TestCoreScope(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := workdir.With(context.Background(), "/project/a")
	var e store.Entry
	if err := json.Unmarshal([]byte(call(t, s, ctx, "owner", Create, `{"key":"name","content":"Lu","scope":"core"}`)), &e); err != nil {
		t.Fatal(err)
	}
	if !e.Core || e.Project != "" {
		t.Fatalf("core: %+v", e)
	}
	if out := call(t, s, ctx, "owner", Read, `{"id":"`+e.ID+`","scope":"core"}`); !strings.Contains(out, `"core":true`) {
		t.Fatalf("read: %s", out)
	}
	if core, err := s.Core(ctx); err != nil || len(core) != 1 || core[0].ID != e.ID {
		t.Fatalf("core: %+v, %v", core, err)
	}
}

// What the agent reads, or finds by words, counts as used; a list of the
// latest does not.
func TestReadAndSearchCountAsUsed(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	e, err := s.Create(ctx, "", "drink", "oolong", "owner")
	if err != nil {
		t.Fatal(err)
	}
	call(t, s, ctx, "owner", Read, `{"id":"`+e.ID+`"}`)
	call(t, s, ctx, "owner", Search, `{"query":"oolong"}`)
	call(t, s, ctx, "owner", Search, `{}`)
	if got, err := s.Read(ctx, "", e.ID); err != nil || got.Used != 2 || got.Version != 1 {
		t.Fatalf("used: %+v, %v", got, err)
	}
}
