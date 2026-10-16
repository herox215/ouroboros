package files_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/plugin"
)

// user makes a home directory for the user of the test, with files in it.
func user(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	for path, content := range map[string]string{
		"notes.md": "hello\n", "share/hello.txt": "hello peer\n", "share/inbox/keep/x": "kept\n", ".ssh/id_ed25519": "key\n",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(home, path)), 0o755)
		if err := os.WriteFile(filepath.Join(home, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// call is the message that makes the fake LLM call a tool.
func call(tool string, args map[string]any) string {
	b, _ := json.Marshal(args)
	return "!" + tool + " " + string(b)
}

// The agent of the owner works with files, everywhere but where no tool goes.
func TestOwnerWorksWithFiles(t *testing.T) {
	home := user(t)
	s := clitest.Settings(t)
	owner := clitest.NodeConfig(s)
	owner.Tools = &config.Tools{Ask: &[]config.Rule{}} // nobody is there to allow a delete
	if err := config.Save(s.Home, owner); err != nil {
		t.Fatal(err)
	}
	chat := func(tool string, args map[string]any) string {
		t.Helper()
		out, err := clitest.Run(t, s, "", "chat", call(tool, args))
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		return strings.TrimSuffix(strings.TrimPrefix(out, "[gpt-4o] turns=3 last="), "\n")
	}

	if got := chat("read", map[string]any{"path": "~/notes.md"}); got != "hello\n" {
		t.Errorf("read: %q", got)
	}
	if got := chat("write", map[string]any{"path": "~/new/todo.md", "content": "one\ntwo\n"}); got != "created ~/new/todo.md (8 bytes)" {
		t.Errorf("write: %q", got)
	}
	if got := chat("edit", map[string]any{"path": "~/new/todo.md", "old": "two", "new": "three"}); got != "edited ~/new/todo.md; lines 1 to 2 of 2 are now:\none\nthree" {
		t.Errorf("edit: %q", got)
	}
	if got := chat("read", map[string]any{"path": "~/new/todo.md", "offset": 2, "limit": 1}); got != "three\n[these were the lines 2 to 2 of 2, the end of the file]" {
		t.Errorf("read: %q", got)
	}
	// The default protects the keys of the user.
	if got := chat("list", map[string]any{"path": "~"}); got != "new/\nnotes.md (6 bytes)\nshare/" {
		t.Errorf("list: %q", got)
	}
	if got := chat("delete", map[string]any{"path": "~/new", "recursive": true}); got != "deleted ~/new" {
		t.Errorf("delete: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, "new")); err == nil {
		t.Error("the directory is still there")
	}
	for tool, args := range map[string]map[string]any{
		"read":   {"path": "~/.ssh/id_ed25519"},
		"write":  {"path": filepath.Join(s.Home, "contacts.json"), "content": "{}"},
		"edit":   {"path": filepath.Join(s.Home, "config.json"), "old": "alpha", "new": "omega"},
		"delete": {"path": s.Home, "recursive": true},
	} {
		if got := chat(tool, args); !strings.HasPrefix(got, "error: ") || !strings.Contains(got, " is refused: ") {
			t.Errorf("%s %v: %q", tool, args, got)
		}
	}
	// The key of the API never reaches the LLM.
	if got := chat("read", map[string]any{"path": filepath.Join(s.Home, "config.json")}); !strings.Contains(got, "the secrets of this node") || strings.Contains(got, "sk-") {
		t.Errorf("the configuration: %q", got)
	}
	if clitest.MustLoad(t, s).Name != "alpha" {
		t.Error("the configuration was changed")
	}
	// It is in the credentials of the node by now, which no tool reads or
	// changes either.
	if kept, _ := os.ReadFile(filepath.Join(s.Home, "credentials.json")); !strings.Contains(string(kept), "sk-") {
		t.Fatalf("the credentials: %s", kept)
	}
	for tool, args := range map[string]map[string]any{
		"read":   {"path": filepath.Join(s.Home, "credentials.json")},
		"search": {"path": filepath.Join(s.Home, "credentials.json"), "pattern": "sk-"},
		"write":  {"path": filepath.Join(s.Home, "credentials.json"), "content": "{}"},
		"delete": {"path": filepath.Join(s.Home, "credentials.json.lock")},
	} {
		if got := chat(tool, args); !strings.Contains(got, "the secrets of this node") || strings.Contains(got, "sk-") {
			t.Errorf("%s of the credentials: %q", tool, got)
		}
	}
	if kept, _ := os.ReadFile(filepath.Join(s.Home, "credentials.json")); !strings.Contains(string(kept), "sk-") {
		t.Fatalf("the credentials were changed: %s", kept)
	}

	// The owner holds the own agent back.
	cfg := clitest.NodeConfig(s)
	cfg.Tools = &config.Tools{
		H2A:       []config.Entry{{Tool: "*"}, {Tool: "read", Settings: json.RawMessage(`{"deny": ["~/share"]}`)}},
		Protected: &[]string{},
	}
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	if got := chat("read", map[string]any{"path": "~/share/hello.txt"}); got != `error: reading ~/share/hello.txt is refused: ~/share is in "deny" of the tool read` {
		t.Errorf("a denied path: %q", got)
	}
	if got := chat("read", map[string]any{"path": "~/.ssh/id_ed25519"}); got != "key\n" {
		t.Errorf("with nothing protected: %q", got)
	}
}

// A peer reaches the files its owner shares, and nothing else.
func TestPeerWorksWithSharedFiles(t *testing.T) {
	home := user(t)
	beta := clitest.Settings(t)
	cfg := clitest.NodeConfig(beta)
	cfg.Name, cfg.Model = "beta", "gpt-4o-mini"
	cfg.Tools = &config.Tools{A2A: []config.Entry{
		{Tool: "read", Settings: json.RawMessage(`{"allow": ["~/share"]}`)},
		{Tool: "list", Settings: json.RawMessage(`{"allow": ["~/share"]}`)},
		{Tool: "write", Settings: json.RawMessage(`{"allow": ["~/share/inbox"], "deny": ["~/share/inbox/keep"]}`)},
		{Tool: "edit"},
	}}
	clitest.StartNode(t, beta, cfg)
	alpha := clitest.Settings(t)
	clitest.StartNode(t, alpha, clitest.NodeConfig(alpha))
	clitest.Finds(alpha, beta)
	clitest.Trust(t, alpha, beta, "beta")
	clitest.Trust(t, beta, alpha, "alpha")
	ask := func(tool string, args map[string]any) string {
		t.Helper()
		out, err := clitest.Run(t, alpha, "", "ask", "beta", call(tool, args))
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		return strings.TrimSuffix(strings.TrimPrefix(out, "[gpt-4o-mini] turns=3 last="), "\n")
	}

	if got := ask("read", map[string]any{"path": "~/share/hello.txt"}); got != "hello peer\n" {
		t.Errorf("read: %q", got)
	}
	if got := ask("list", map[string]any{"path": "~/share"}); got != "hello.txt (11 bytes)\ninbox/" {
		t.Errorf("list: %q", got)
	}
	if got := ask("write", map[string]any{"path": "~/share/inbox/from-alpha.txt", "content": "hi"}); got != "created ~/share/inbox/from-alpha.txt (2 bytes)" {
		t.Errorf("write: %q", got)
	}
	for tool, args := range map[string]map[string]any{
		"read":  {"path": "~/notes.md"},
		"list":  {"path": "~"},
		"write": {"path": "~/share/inbox/keep/x", "content": "gone"},
		"edit":  {"path": "~/share/hello.txt", "old": "hello", "new": "bye"}, // listed without "allow": nowhere
	} {
		if got := ask(tool, args); !strings.HasPrefix(got, "error: ") || !strings.Contains(got, " is refused: ") {
			t.Errorf("%s %v: %q", tool, args, got)
		}
	}
	// A tool that is not listed is no tool for a peer.
	if got := ask("delete", map[string]any{"path": "~/share/hello.txt"}); !strings.HasPrefix(got, "[gpt-4o-mini] turns=1 ") {
		t.Errorf("delete: %q", got)
	}
	for path, want := range map[string]string{"share/hello.txt": "hello peer\n", "share/inbox/keep/x": "kept\n", "share/inbox/from-alpha.txt": "hi"} {
		if b, _ := os.ReadFile(filepath.Join(home, path)); string(b) != want {
			t.Errorf("%s: %q", path, b)
		}
	}
}

// Settings a tool does not take keep the node from starting and the chat
// from answering.
func TestSettingsAreChecked(t *testing.T) {
	user(t)
	s := clitest.Settings(t)
	for settings, want := range map[string]string{
		`{"alow": ["~/share"]}`: `unknown field "alow"`,
		`{"allow": "~/share"}`:  "cannot unmarshal string",
		`{"allow": ["share"]}`:  `the path "share" has to be absolute or start with ~`,
	} {
		cfg := clitest.NodeConfig(s)
		cfg.Tools = &config.Tools{A2A: []config.Entry{{Tool: "read", Settings: json.RawMessage(settings)}}}
		config.Save(s.Home, cfg)
		if _, err := clitest.Run(t, s, "", "serve"); err == nil || !strings.Contains(err.Error(), "tools.a2a: settings of the tool read: ") || !strings.Contains(err.Error(), want) {
			t.Errorf("a2a %s: %v", settings, err)
		}
		cfg.Tools = &config.Tools{H2A: []config.Entry{{Tool: "read", Settings: json.RawMessage(settings)}}}
		config.Save(s.Home, cfg)
		if _, err := clitest.Run(t, s, "", "chat", "hi"); err == nil || !strings.Contains(err.Error(), "tools.h2a: settings of the tool read: ") || !strings.Contains(err.Error(), want) {
			t.Errorf("h2a %s: %v", settings, err)
		}
	}
	cfg := clitest.NodeConfig(s)
	cfg.Tools = &config.Tools{A2A: []config.Entry{{Tool: "list_peers", Settings: json.RawMessage(`{"allow": ["~"]}`)}}}
	config.Save(s.Home, cfg)
	if _, err := clitest.Run(t, s, "", "serve"); err == nil || !strings.Contains(err.Error(), "tools.a2a: the tool list_peers takes no settings") {
		t.Errorf("settings for a tool without: %v", err)
	}
}

// The owner is shown which file the agent is at.
func TestToolViews(t *testing.T) {
	s := clitest.Settings(t)
	var ctx *plugin.Context
	clitest.NewApp(t, s, "", plugin.New("reader", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	}))
	for tool, want := range map[string]string{
		"read": "reading ~/notes.md", "write": "writing ~/notes.md", "edit": "editing ~/notes.md",
		"list": "listing ~/notes.md", "delete": "deleting ~/notes.md", "search": "searching ~/notes.md",
	} {
		if got := plugin.ToolLine(ctx, plugin.Step{Tool: tool, Args: json.RawMessage(`{"path": "~/notes.md", "content": "x"}`)}); got != want {
			t.Errorf("%s: %q, want %q", tool, got, want)
		}
		for _, args := range []string{`{}`, `{"path": 1}`, `"x"`, `{"path": ""}`} {
			if got := plugin.ToolLine(ctx, plugin.Step{Tool: tool, Args: json.RawMessage(args)}); got != "" {
				t.Errorf("%s %s: %q", tool, args, got)
			}
		}
	}
}
