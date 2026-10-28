package assistant_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/history"
	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	"ouroboros/internal/openai"
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

// Statistics are archived with a conversation of the owner, where there are
// any to ask for, and never with one of a peer.
func TestArchivedStatistics(t *testing.T) {
	archive := history.Open(t.TempDir())
	owner := agent.AnswerOutcome{Processing: agent.Processing{Conversation: agent.Conversation{ID: "mine"}}}
	peer := agent.AnswerOutcome{Processing: agent.Processing{Conversation: agent.Conversation{ID: "theirs", Peer: true, Origin: a2a.Origin{PublicKey: "key"}}}}
	ended := func(id string) history.Record {
		t.Helper()
		records, err := archive.Records(id)
		if err != nil || len(records) == 0 {
			t.Fatalf("%s: %+v, %v", id, records, err)
		}
		return records[len(records)-1]
	}
	plain := assistantplugin.Archived(archive)
	if err := plain.After(context.Background(), owner); err != nil || ended(history.Owner("mine")).Statistics != nil {
		t.Errorf("without statistics: %v, %+v", err, ended(history.Owner("mine")))
	}
	var asked []string
	counted := assistantplugin.Archived(archive, func(id string) history.Statistics {
		asked = append(asked, id)
		return history.Statistics{Context: 42}
	})
	if err := counted.After(context.Background(), owner); err != nil || ended(history.Owner("mine")).Statistics == nil || ended(history.Owner("mine")).Statistics.Context != 42 {
		t.Errorf("with statistics: %v, %+v", err, ended(history.Owner("mine")))
	}
	if err := counted.After(context.Background(), peer); err != nil || ended(history.Party("key")+"/theirs").Statistics != nil {
		t.Errorf("a peer: %v, %+v", err, ended(history.Party("key")+"/theirs"))
	}
	if strings.Join(asked, ",") != "mine" {
		t.Errorf("statistics were asked for %v", asked)
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

// Direct tools use the owner and peer permissions of the node.
func TestDirectToolsUseTheToolsOfTheNode(t *testing.T) {
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
	owner := clitest.NodeConfig(alpha)
	owner.Tools = &config.Tools{Ask: &[]config.Rule{}} // nobody is there to allow a delete
	clitest.StartNode(t, alpha, owner)
	clitest.Finds(alpha, beta)
	clitest.Trust(t, alpha, beta, "beta")
	clitest.Trust(t, beta, alpha, "alpha")

	for message, want := range map[string]string{
		`!read {"path":"~/private.txt"}`: "mine\n",
		`!list {"path":"~/share"}`:       "a.txt (6 bytes)\nb.txt (5 bytes)",
	} {
		out, err := clitest.Run(t, alpha, "", "chat", message)
		if err != nil || out != "[gpt-4o] turns=3 last="+want+"\n" {
			t.Fatalf("owner %s: %q, %v", message, out, err)
		}
	}

	// Each request is also nested in an owner ask_peer call: forwarding must
	// preserve the peer allowlist and file settings, not the owner permissions.
	for _, tt := range []struct{ message, want string }{
		{`!read {"path":"~/share/a.txt"}`, "[gpt-4o-mini] turns=3 last=alpha\n"},
		{`!list {"path":"~/share"}`, "[gpt-4o-mini] turns=3 last=a.txt (6 bytes)\nb.txt (5 bytes)"},
		{`!read {"path":"~/private.txt"}`, `is refused: it is not in "allow" of the tool read`},
		{`!list {"path":"~"}`, `is refused: it is not in "allow" of the tool list`},
		{`!delete {"path":"~/share/b.txt"}`, `[gpt-4o-mini] turns=1 last=!delete {"path":"~/share/b.txt"}`},
		{`!write {"path":"~/share/a.txt","content":"changed"}`, `[gpt-4o-mini] turns=1 last=!write`},
		{`!ask_peer {"peer":"alpha","message":"hello"}`, `[gpt-4o-mini] turns=1 last=!ask_peer`},
		{`!tools.delete {"path":"~/share/b.txt"}`, `[gpt-4o-mini] turns=1 last=!tools.delete`},
	} {
		for _, nested := range []bool{false, true} {
			args := []string{"ask", "beta", tt.message}
			if nested {
				args = []string{"chat", "!ask_peer " + string(mustJSON(map[string]string{"peer": "beta", "message": tt.message}))}
			}
			out, err := clitest.Run(t, alpha, "", args...)
			if err != nil || !strings.Contains(out, tt.want) {
				t.Errorf("peer %s (nested=%v): %q, %v; want %q", tt.message, nested, out, err, tt.want)
			}
		}
	}
	for name, want := range map[string]string{"a.txt": "alpha\n", "b.txt": "beta\n"} {
		got, err := os.ReadFile(filepath.Join(home, "share", name))
		if err != nil || string(got) != want {
			t.Fatalf("peer changed %s: %q, %v", name, got, err)
		}
	}

	// Only the direct advertised name deletes; there is no tools namespace.
	for _, message := range []string{`!tools.delete {"path":"~/share/b.txt"}`, `!delete {"path":"~/share/b.txt"}`} {
		out, err := clitest.Run(t, alpha, "", "chat", message)
		want := "[gpt-4o] turns=1 last=" + message + "\n"
		if strings.HasPrefix(message, "!delete ") {
			want = "[gpt-4o] turns=3 last=deleted ~/share/b.txt\n"
		}
		if err != nil || out != want {
			t.Fatalf("owner delete %s: %q, %v", message, out, err)
		}
		_, statErr := os.Stat(filepath.Join(home, "share/b.txt"))
		if strings.HasPrefix(message, "!delete ") {
			if !os.IsNotExist(statErr) {
				t.Fatalf("direct delete left file: %v", statErr)
			}
		} else if statErr != nil {
			t.Fatalf("unadvertised name changed file: %v", statErr)
		}
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// Stopping the node closes the archive the answers went to: no file of it
// stays open. The open files are counted in /proc, so only on Linux; elsewhere
// the node is still started, answered through and stopped.
func TestStopClosesTheArchive(t *testing.T) {
	counted := runtime.GOOS == "linux"
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := home.Dir(root)
	k := kernel.New()
	if err := k.Start(assistantplugin.Plugin(dir)); err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	builder, ok := kernel.Service[assistant.Builder](k)
	if !ok {
		t.Fatal("no blueprint")
	}
	owner := agent.AnswerOutcome{Processing: agent.Processing{Conversation: agent.Conversation{ID: "mine"}}, Messages: []openai.Message{{Role: "user", Content: "hi"}}}
	if err := builder.AnswerHooks()[0].After(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	if counted && openBelow(t, dir.History()) == 0 {
		t.Fatal("the archive was never opened")
	}
	k.Stop()
	if !counted {
		return
	}
	if n := openBelow(t, dir.History()); n != 0 {
		t.Fatalf("%d files of the archive open after the node stopped", n)
	}
}

// openBelow counts the files below dir this process has open.
func openBelow(t *testing.T, dir string) int {
	t.Helper()
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, fd := range fds {
		if target, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name())); err == nil && strings.HasPrefix(target, dir+string(filepath.Separator)) {
			n++
		}
	}
	return n
}
