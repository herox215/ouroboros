package claudesessions_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/cli"
	claudesessionsplugin "ouroboros/internal/plugins/claudesessions"
	"ouroboros/plugin"
)

// fixture is one transcript as Claude Code writes it: a question, an answer
// and a call of a tool that failed.
const fixture = `{"type":"user","sessionId":"abcd1234-0000-4e5a-8b1c-000000000001","cwd":"/home/user/proj","timestamp":"2026-03-01T10:00:05Z","message":{"role":"user","content":"Run the tests, please"}}
{"type":"assistant","sessionId":"abcd1234-0000-4e5a-8b1c-000000000001","cwd":"/home/user/proj","timestamp":"2026-03-01T10:00:09Z","message":{"role":"assistant","content":[{"type":"text","text":"I run them."},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]}}
{"type":"user","sessionId":"abcd1234-0000-4e5a-8b1c-000000000001","cwd":"/home/user/proj","timestamp":"2026-03-01T10:00:12Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":[{"type":"text","text":"FAIL ouroboros/internal/proj"}]}]}}
`

func setup(t *testing.T) *plugin.Context {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "-home-user-proj", "abcd1234-0000-4e5a-8b1c-000000000001.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	var ctx *plugin.Context
	spy := plugin.New("spy", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	})
	if _, err := cli.NewWith(claudesessionsplugin.Plugin(claudesessionsplugin.Options{Dir: dir}), spy); err != nil {
		t.Fatal(err)
	}
	return ctx
}

// tool finds the tool of a name among what the plugin contributes.
func tool(t *testing.T, ctx *plugin.Context, name string) plugin.Tool {
	t.Helper()
	for _, tl := range plugin.All[plugin.Tool](ctx) {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("the plugin contributes no tool %q", name)
	return plugin.Tool{}
}

// The plugin contributes the four tools, each with a description and a
// schema that is JSON.
func TestContributesTheTools(t *testing.T) {
	ctx := setup(t)
	all := plugin.All[plugin.Tool](ctx)
	if len(all) != 4 {
		t.Fatalf("the plugin contributes %d tools", len(all))
	}
	for _, name := range []string{claudesessionsplugin.Sessions, claudesessionsplugin.Search, claudesessionsplugin.Read, claudesessionsplugin.Calls} {
		tl := tool(t, ctx, name)
		if !strings.Contains(tl.Description, "Claude Code") {
			t.Errorf("%s says what it reads: %q", name, tl.Description)
		}
		if !json.Valid([]byte(tl.Schema)) {
			t.Errorf("%s has a schema that is no JSON: %s", name, tl.Schema)
		}
	}
}

func TestToolsReadTheTranscripts(t *testing.T) {
	ctx := setup(t)
	bg := context.Background()

	out, err := tool(t, ctx, claudesessionsplugin.Sessions).Run(bg, []byte(`{}`))
	if err != nil || !strings.Contains(out, "abcd1234") || !strings.Contains(out, "Run the tests, please") {
		t.Fatalf("sessions: %v:\n%s", err, out)
	}

	out, err = tool(t, ctx, claudesessionsplugin.Search).Run(bg, []byte(`{"query":"fail"}`))
	if err != nil || !strings.Contains(out, "FAIL ouroboros") {
		t.Fatalf("search: %v:\n%s", err, out)
	}

	out, err = tool(t, ctx, claudesessionsplugin.Read).Run(bg, []byte(`{"session":"abcd1234","from":2,"count":1}`))
	if err != nil || !strings.Contains(out, "I run them.") || !strings.Contains(out, "go test ./...") {
		t.Fatalf("read: %v:\n%s", err, out)
	}

	out, err = tool(t, ctx, claudesessionsplugin.Calls).Run(bg, []byte(`{"tool":"Bash"}`))
	if err != nil || !strings.Contains(out, "Bash failed") {
		t.Fatalf("calls: %v:\n%s", err, out)
	}
}

// A call that cannot be answered says why, and does not fail on the way.
func TestToolsRefuseWhatTheyCannotRead(t *testing.T) {
	ctx := setup(t)
	bg := context.Background()
	if _, err := tool(t, ctx, claudesessionsplugin.Read).Run(bg, []byte(`{"session":"ffffffff"}`)); err == nil {
		t.Error("a session that does not exist")
	}
	if _, err := tool(t, ctx, claudesessionsplugin.Sessions).Run(bg, []byte(`{"since":"yesterday"}`)); err == nil {
		t.Error("a day that is no day")
	}
	if _, err := tool(t, ctx, claudesessionsplugin.Search).Run(bg, []byte(`{"query":"  "}`)); err == nil {
		t.Error("a search without a text")
	}
}

// Without a home there is no place where Claude Code keeps its transcripts.
func TestDefaultWithoutAHome(t *testing.T) {
	t.Setenv("HOME", "")
	if dir := claudesessionsplugin.Default(); dir != "" {
		t.Errorf("without a home: %q", dir)
	}
	t.Setenv("HOME", "/home/user")
	if dir := claudesessionsplugin.Default(); dir != filepath.Join("/home/user", ".claude", "projects") {
		t.Errorf("with a home: %q", dir)
	}
}

// Every tool says what it is doing while it runs.
func TestToolsSayWhatTheyDo(t *testing.T) {
	ctx := setup(t)
	views := plugin.All[plugin.ToolView](ctx)
	if len(views) != 4 {
		t.Fatalf("%d views", len(views))
	}
	for _, v := range views {
		if doing := v.Show(nil); !strings.Contains(doing, "Claude Code") {
			t.Errorf("%s shows %q", v.Tool, doing)
		}
	}
}

// The transcripts are the owner's own work: a peer reads none of them, and
// what it was refused is logged.
func TestToolsRefuseAPeer(t *testing.T) {
	var logged []string
	dir := t.TempDir()
	var ctx *plugin.Context
	spy := plugin.New("spy", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	})
	log := func(format string, args ...any) { logged = append(logged, format) }
	if _, err := cli.NewWith(claudesessionsplugin.Plugin(claudesessionsplugin.Options{Dir: dir, Log: log}), spy); err != nil {
		t.Fatal(err)
	}
	peer := a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "k"})
	for _, name := range []string{claudesessionsplugin.Sessions, claudesessionsplugin.Search, claudesessionsplugin.Read, claudesessionsplugin.Calls} {
		if _, err := tool(t, ctx, name).Run(peer, []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "owner") {
			t.Errorf("%s answers a peer: %v", name, err)
		}
	}
	if len(logged) != 0 {
		t.Errorf("a refusal reads nothing to log: %v", logged)
	}
	if _, err := tool(t, ctx, claudesessionsplugin.Read).Run(context.Background(), []byte(`{"session":"x"}`)); err == nil || len(logged) != 1 {
		t.Errorf("a read that failed is logged: %v, %v", err, logged)
	}
}

// The times are a day, a day with a time or RFC 3339; until a day is the end
// of that day. Anything else, and arguments that are no JSON, are refused.
func TestToolsTakeTimes(t *testing.T) {
	ctx := setup(t)
	bg := context.Background()
	for _, args := range []string{
		`{"since":"2026-03-01","until":"2026-03-01"}`,
		`{"since":"2026-03-01 00:00","until":"2026-03-01T23:59:59Z"}`,
	} {
		out, err := tool(t, ctx, claudesessionsplugin.Sessions).Run(bg, []byte(args))
		if err != nil || !strings.Contains(out, "abcd1234") {
			t.Errorf("%s: %v:\n%s", args, err, out)
		}
	}
	if out, err := tool(t, ctx, claudesessionsplugin.Read).Run(bg, []byte(`{"session":"abcd1234","since":"2026-03-01","until":"2026-03-01"}`)); err != nil || !strings.Contains(out, "Run the tests, please") {
		t.Errorf("a read of a day: %v:\n%s", err, out)
	}
	if out, err := tool(t, ctx, claudesessionsplugin.Calls).Run(bg, []byte(`{"since":"2026-03-02"}`)); err != nil || out != "no tool call matches" {
		t.Errorf("calls since a later day: %v: %q", err, out)
	}
	for _, name := range []string{claudesessionsplugin.Sessions, claudesessionsplugin.Search, claudesessionsplugin.Calls, claudesessionsplugin.Read} {
		for _, args := range []string{`{"since":"soon"}`, `{"until":"later"}`} {
			if _, err := tool(t, ctx, name).Run(bg, []byte(args)); err == nil || !strings.Contains(err.Error(), "is no date") {
				t.Errorf("%s %s: %v", name, args, err)
			}
		}
	}
	if _, err := tool(t, ctx, claudesessionsplugin.Sessions).Run(bg, []byte(`{"limit":"many"}`)); err == nil {
		t.Error("arguments that do not fit")
	}
}

// Without a directory the plugin reads where Claude Code keeps the
// transcripts, below the home.
func TestPluginReadsBelowTheHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "projects", "-home-user-proj", "abcd1234-0000-4e5a-8b1c-000000000001.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	var ctx *plugin.Context
	spy := plugin.New("spy", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	})
	if _, err := cli.NewWith(claudesessionsplugin.Plugin(claudesessionsplugin.Options{}), spy); err != nil {
		t.Fatal(err)
	}
	out, err := tool(t, ctx, claudesessionsplugin.Sessions).Run(context.Background(), []byte(`{}`))
	if err != nil || !strings.Contains(out, "Run the tests, please") {
		t.Fatalf("%v:\n%s", err, out)
	}
}
