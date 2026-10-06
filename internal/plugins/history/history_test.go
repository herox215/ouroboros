package history_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/plugin"
)

// call is the message that makes the fake LLM call a tool.
func call(tool string, args map[string]any) string {
	b, _ := json.Marshal(args)
	return "!" + tool + " " + string(b)
}

// lines are the messages a user types, one per line.
func lines(msgs ...string) string { return strings.Join(msgs, "\n") + "\n" }

// logged is what the log of the node of home has.
func logged(t *testing.T, dir string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, "ouro.log"))
	return string(b)
}

// The agent of the owner finds what was said before in this conversation and
// in earlier ones, and the owner deletes what is archived.
func TestOwner(t *testing.T) {
	s := clitest.Settings(t)
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	if out, err := clitest.Run(t, s, "", "history"); err != nil || out != "no conversation is archived\n" {
		t.Fatalf("empty: %q, %v", out, err)
	}
	if _, err := clitest.Run(t, s, "", "chat", "the roof is red"); err != nil {
		t.Fatal(err)
	}

	out, err := clitest.Run(t, s, lines(
		"the garden is green",
		call("history_search", map[string]any{"query": "GARDEN"}),
		call("history_search", map[string]any{"query": "roof"}),
		call("history_search", map[string]any{"query": "roof", "scope": "all"}),
		call("history_search", map[string]any{"query": "roof", "scope": "everywhere"}),
		call("history_sessions", nil),
		call("history_read", map[string]any{"from": 1, "count": 1}),
	), "chat")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"#1 user: the garden is green\n",
		`last=no record of this conversation contains "roof"`,
		"#1 user: the roof is red\n", "#2 assistant: [gpt-4o] turns=1 last=the roof is red\n[6 hits in 2 conversations]", // the search before is archived too
		`error: scope is current or all, not "everywhere"`,
		`(this conversation) · started `, ` · "the roof is red"`, "[2 sessions]",
		"· user\nthe garden is green\n[records 1 to 1 of ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	log := logged(t, s.Home)
	for _, want := range []string{
		"history_search for the owner, scope current: 2 hits\n", "history_search for the owner, scope all: 6 hits\n",
		"history_sessions for the owner: 2 sessions\n", "history_read for the owner, scope current\n",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q", want)
		}
	}
	if strings.Contains(log, "is red\n") || strings.Contains(log, "hits\" ") {
		t.Errorf("the log has what was found:\n%s", log)
	}

	// Another session named, and one that is none.
	out, _ = clitest.Run(t, s, "", "history")
	entries := strings.Split(strings.TrimSpace(out), "\n")
	if len(entries) != 2 || !strings.HasPrefix(entries[1], "owner/") || !strings.HasSuffix(entries[1], `"the roof is red"`) {
		t.Fatalf("history: %q", out)
	}
	first := strings.Fields(entries[1])[0]
	session := strings.TrimPrefix(first, "owner/")
	out, _ = clitest.Run(t, s, "", "chat", call("history_read", map[string]any{"session": session}))
	if !strings.Contains(out, "the roof is red\n") {
		t.Errorf("read another session: %q", out)
	}
	if out, _ = clitest.Run(t, s, "", "chat", call("history_read", map[string]any{"session": "../x"})); !strings.Contains(out, "error: there is no session") {
		t.Errorf("read no session: %q", out)
	}
	if out, _ = clitest.Run(t, s, "", "chat", call("history_read", map[string]any{"from": "one"})); !strings.Contains(out, "error: invalid arguments") {
		t.Errorf("invalid arguments: %q", out)
	}
	if !strings.Contains(logged(t, s.Home), "history_read for the owner, scope a named session, failed: there is no session") {
		t.Error("a failed call is not logged")
	}

	if out, err := clitest.Run(t, s, "", "history", "delete", first); err != nil || out != "● deleted "+first+"\n" {
		t.Fatalf("delete: %q, %v", out, err)
	}
	if out, _ := clitest.Run(t, s, "", "history"); strings.Contains(out, first) {
		t.Errorf("deleted, but listed: %q", out)
	}
	if _, err := clitest.Run(t, s, "", "history", "delete", first); err == nil {
		t.Error("deleting it again should fail")
	}
	for _, args := range [][]string{{"history", "delete"}, {"history", "drop", first}, {"history", "a", "b", "c"}} {
		if _, err := clitest.Run(t, s, "", args...); err == nil || err.Error() != "usage: ouro history [delete SESSION]" {
			t.Errorf("%v: %v", args, err)
		}
	}
}

// An archive that cannot be written leaves the answer as it is and is
// logged as the diagnostic of the plugin.
func TestArchiveFails(t *testing.T) {
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	os.WriteFile(home.Dir(s.Home).History(), nil, 0o600)
	out, err := clitest.Run(t, s, "", "chat", "hello")
	if err != nil || !strings.Contains(out, "last=hello") {
		t.Fatalf("answer: %q, %v", out, err)
	}
	if !strings.Contains(logged(t, s.Home), " plugin history after failed: ") {
		t.Errorf("not logged:\n%s", logged(t, s.Home))
	}
	if _, err := clitest.Run(t, s, "", "history"); err == nil {
		t.Error("listing an archive that is a file should fail")
	}
	if out, _ := clitest.Run(t, s, "", "chat", call("history_sessions", nil)); !strings.Contains(out, "error: ") {
		t.Errorf("sessions: %q", out)
	}
	if !strings.Contains(logged(t, s.Home), "history_sessions for the owner: 0 sessions, failed: ") {
		t.Error("a failed listing is not logged")
	}
}

// A peer reaches the conversation it is in, and nothing of the owner or of
// another conversation.
func TestPeer(t *testing.T) {
	beta := clitest.Settings(t)
	cfg := clitest.NodeConfig(beta)
	cfg.Name, cfg.Model = "beta", "gpt-4o-mini"
	cfg.Tools = &config.Tools{A2A: []config.Entry{{Tool: "history_search"}, {Tool: "history_read"}, {Tool: "history_sessions"}}}
	clitest.StartNode(t, beta, cfg)
	alpha := clitest.Settings(t)
	clitest.StartNode(t, alpha, clitest.NodeConfig(alpha))
	clitest.Finds(alpha, beta)
	clitest.Trust(t, alpha, beta, "beta")
	clitest.Trust(t, beta, alpha, "alpha")

	// What the owner of beta said.
	if _, err := clitest.Run(t, beta, "", "chat", "the secret of beta"); err != nil {
		t.Fatal(err)
	}
	// An earlier conversation of alpha.
	if _, err := clitest.Run(t, alpha, "", "ask", "beta", "the secret of an earlier talk"); err != nil {
		t.Fatal(err)
	}
	out, err := clitest.Run(t, alpha, lines(
		"my own words",
		call("history_search", map[string]any{"query": "words"}),
		call("history_search", map[string]any{"query": "secret"}),
		call("history_search", map[string]any{"query": "secret", "scope": "all"}),
		call("history_sessions", nil),
		call("history_read", map[string]any{"count": 1}),
	), "ask", "beta")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"(this conversation) · ", " · #1 user: my own words\n", "[2 hits in this conversation]",
		`last=no record of this conversation contains "secret"`,
		"error: a peer can only reach the archive of the conversation it is in",
		"(this conversation) · started ", `"my own words"`, "[1 session]",
		"· user\nmy own words\n[records 1 to 1 of ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	// The session of the owner of beta is no session of a peer.
	owner, _ := clitest.Run(t, beta, "", "history")
	session := strings.TrimPrefix(strings.Fields(owner)[0], "owner/")
	for _, s := range []string{session, "../owner/" + session} {
		if out, _ := clitest.Run(t, alpha, "", "ask", "beta", call("history_read", map[string]any{"session": s})); !strings.Contains(out, "error: a peer can only reach") {
			t.Errorf("%q: %q", s, out)
		}
	}
	if !strings.Contains(logged(t, beta.Home), "history_search for a peer, scope current: 2 hits\n") {
		t.Error("the call of a peer is not logged")
	}
	// The conversations of the peer are archived apart from those of the owner.
	if !strings.Contains(owner, "peer-") || !strings.Contains(owner, `"the secret of an earlier talk"`) {
		t.Errorf("the archive of beta: %q", owner)
	}
}

// What the owner is shown of the calls.
func TestToolViews(t *testing.T) {
	var ctx *plugin.Context
	spy := plugin.New("spy", func(c *plugin.Context, _ struct{}) error { ctx = c; return nil })
	clitest.NewApp(t, clitest.Settings(t), "", spy)
	for tool, want := range map[string]string{"history_search": "searching the history", "history_read": "reading the history", "history_sessions": "listing the history"} {
		if got := plugin.ToolLine(ctx, plugin.Step{Tool: tool}); got != want {
			t.Errorf("%s: %q", tool, got)
		}
	}
}
