package claudesessions_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
