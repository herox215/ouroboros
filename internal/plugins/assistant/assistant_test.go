package assistant_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/history"
	assistantplugin "ouroboros/internal/plugins/assistant"
)

// A conversation of a peer is archived by its key: one without is not.
func TestArchivedPeerWithoutKey(t *testing.T) {
	hook := assistantplugin.Archived(history.Open(t.TempDir()))
	err := hook.After(context.Background(), agent.AnswerOutcome{Processing: agent.Processing{Conversation: agent.Conversation{ID: "c", Peer: true}}})
	if err == nil {
		t.Error("a peer without its key should not be archived")
	}
}

// The owner can take tools away from the own agent, too.
func TestOwnerToolsCanBeRestricted(t *testing.T) {
	s := clitest.Settings(t)
	cfg := clitest.NodeConfig(s)
	cfg.Tools = &config.Tools{H2A: config.Named()}
	config.Save(s.Home, cfg)
	out, err := clitest.Run(t, s, "", "chat", "@beta hello")
	if err != nil || out != "[gpt-4o] turns=1 last=@beta hello\n" {
		t.Fatalf("without tools the message is just a message: %q, %v", out, err)
	}

	cfg.Tools = &config.Tools{H2A: config.Named("ask_peers")}
	config.Save(s.Home, cfg)
	if _, err := clitest.Run(t, s, "", "chat", "hi"); err == nil || !strings.Contains(err.Error(), "tools.h2a") {
		t.Fatalf("a typo should be reported: %v", err)
	}
	cfg.Tools = &config.Tools{A2A: config.Named("ask_peers")}
	config.Save(s.Home, cfg)
	if _, err := clitest.Run(t, s, "", "serve"); err == nil || !strings.Contains(err.Error(), "tools.a2a") {
		t.Fatalf("a node with a typo should not start: %v", err)
	}
}

// run is the message that makes the fake LLM run a script.
func run(script string) string {
	args, _ := json.Marshal(map[string]string{"script": script})
	return "!run " + string(args)
}

// A script works with the tools of the node: for the owner with all of them,
// for a peer with those of a2a, each as far as its settings go.
func TestScriptsUseTheToolsOfTheNode(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	for name, content := range map[string]string{"share/a.txt": "alpha\n", "share/b.txt": "beta\n", "private.txt": "mine\n"} {
		os.MkdirAll(filepath.Dir(filepath.Join(home, name)), 0o755)
		if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	beta := clitest.Settings(t)
	cfg := clitest.NodeConfig(beta)
	cfg.Name, cfg.Model = "beta", "gpt-4o-mini"
	cfg.Tools = &config.Tools{A2A: []config.Entry{
		{Tool: "read", Settings: json.RawMessage(`{"allow": ["~/share"]}`)},
		{Tool: "list", Settings: json.RawMessage(`{"allow": ["~/share"]}`)},
	}}
	clitest.StartNode(t, beta, cfg)
	alpha := clitest.Settings(t)
	clitest.StartNode(t, alpha, clitest.NodeConfig(alpha))
	clitest.Finds(alpha, beta)
	clitest.Trust(t, alpha, beta, "beta")
	clitest.Trust(t, beta, alpha, "alpha")

	// The owner: every file of a directory in one call of the LLM, and a
	// tool whose name is a word of the language.
	everything := `return list({path: "~/share"}).split("\n").map(function(line) { return read({path: "~/share/" + line.split(" ")[0]}).trim() }).join("+")`
	out, err := clitest.Run(t, alpha, "", "chat", run(everything))
	if err != nil || out != "[gpt-4o] turns=3 last=alpha+beta\n" {
		t.Fatalf("the owner: %q, %v", out, err)
	}
	out, err = clitest.Run(t, alpha, "", "chat", run(`delete({path: "~/share/a.txt"})`))
	if err != nil || !strings.Contains(out, "last=error: delete is a word of JavaScript") {
		t.Fatalf("delete as a function: %q, %v", out, err)
	}
	out, err = clitest.Run(t, alpha, "", "chat", run(`return tools.delete({path: "~/share/b.txt"}) + " / " + list({path: "~/share"})`))
	if err != nil || out != "[gpt-4o] turns=3 last=deleted ~/share/b.txt / a.txt (6 bytes)\n" {
		t.Fatalf("tools.delete: %q, %v", out, err)
	}
	// A script of the owner asks a peer, whose agent runs a script in turn.
	asks := `return ask_peer({peer: "beta", message: ` + string(mustJSON(run(`return read({path: "~/share/a.txt"})`))) + `})`
	out, err = clitest.Run(t, alpha, "", "chat", run(asks))
	if err != nil || out != "[gpt-4o] turns=3 last=[gpt-4o-mini] turns=3 last=alpha\n\n" {
		t.Fatalf("a script that asks a peer: %q, %v", out, err)
	}

	// The peer: what a2a lists, as far as it lets the tool go, and nothing else.
	for script, want := range map[string]string{
		`return Object.keys(tools)`:                          `last=["read","list","explain_error"]`,
		`return read({path: "~/share/a.txt"})`:               "last=alpha\n",
		`return read({path: "~/private.txt"})`:               `is refused: it is not in "allow" of the tool read`,
		`return typeof write + typeof ask_peer + typeof run`: "last=undefinedundefinedundefined",
	} {
		out, err := clitest.Run(t, alpha, "", "ask", "beta", run(script))
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("a peer runs %s: %q, %v; want %q", script, out, err, want)
		}
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
