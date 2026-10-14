package shell_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/plugin"
)

// call is the message that makes the fake LLM call a tool.
func call(tool string, args map[string]any) string {
	b, _ := json.Marshal(args)
	return "!" + tool + " " + string(b)
}

// The agent of the owner runs commands as the owner, in no sandbox.
func TestOwnerRunsCommands(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	s := clitest.Settings(t)
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(s.Home, "state")
	out, err := clitest.Run(t, s, "", "chat", call("bash", map[string]any{"command": "pwd; echo x > " + state + "; cat " + state}))
	if got := strings.TrimSuffix(strings.TrimPrefix(out, "[gpt-4o] turns=3 last="), "\n"); err != nil || got != "exit 0\n"+home+"\nx\n" {
		t.Errorf("bash: %q, %v", got, err)
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
