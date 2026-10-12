package shell_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/sandbox"
	"ouroboros/plugin"
)

// call is the message that makes the fake LLM call a tool.
func call(tool string, args map[string]any) string {
	b, _ := json.Marshal(args)
	return "!" + tool + " " + string(b)
}

// The agent of the owner runs commands, in a sandbox unless the owner turned
// it off.
func TestOwnerRunsCommands(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	s := clitest.Settings(t)
	cfg := clitest.NodeConfig(s)
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	chat := func(args map[string]any) string {
		t.Helper()
		out, err := clitest.Run(t, s, "", "chat", call("bash", args))
		if err != nil {
			t.Fatalf("bash: %v", err)
		}
		return strings.TrimSuffix(strings.TrimPrefix(out, "[gpt-4o] turns=3 last="), "\n")
	}
	state := filepath.Join(s.Home, "state")
	if sandbox.Available() == nil {
		if got := chat(map[string]any{"command": "echo $" + sandbox.Env + "; pwd"}); got != "exit 0\n1\n"+home+"\n" {
			t.Errorf("in the sandbox: %q", got)
		}
		if got := chat(map[string]any{"command": "echo x > " + state}); !strings.HasPrefix(got, "exit 1\n") {
			t.Errorf("writing the state of the node: %q", got)
		}
		if _, err := os.Stat(state); err == nil {
			t.Error("the command wrote the state of the node")
		}
	}

	cfg.BashWithoutSandbox = true
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	if got := chat(map[string]any{"command": "echo \"[$" + sandbox.Env + "]\" > " + state + "; cat " + state}); got != "exit 0\n[]\n" {
		t.Errorf("without a sandbox: %q", got)
	}
}

// bash is no tool for all peers: only the list of one peer in tools.peers
// gives it.
func TestNoToolForPeers(t *testing.T) {
	s := clitest.Settings(t)
	cfg := clitest.NodeConfig(s)
	cfg.Tools = &config.Tools{A2A: config.Named("bash")}
	config.Save(s.Home, cfg)
	if _, err := clitest.Run(t, s, "", "serve"); err == nil || !strings.Contains(err.Error(), "the tool bash is no tool for all peers: list it in tools.peers") {
		t.Errorf("bash for peers: %v", err)
	}
}

// The owner is shown which command the agent runs.
func TestToolView(t *testing.T) {
	s := clitest.Settings(t)
	var ctx *plugin.Context
	clitest.NewApp(t, s, "", plugin.New("viewer", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	}))
	if got := plugin.ToolLine(ctx, plugin.Step{Tool: "bash", Args: json.RawMessage(`{"command": "go test ./..."}`)}); got != "running go test ./..." {
		t.Errorf("tool line: %q", got)
	}
}
