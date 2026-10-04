package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/plugin"
)

// The owner starts the node, which is this test binary run as ouro.
func TestMain(m *testing.M) { clitest.Main(m) }

// owner is what the owner of the node of s does.
func owner(t *testing.T, s cli.Settings) *plugin.Owner {
	t.Helper()
	app, _ := clitest.NewApp(t, s, "")
	return plugin.OwnerOf(home.Dir(s.Home), clitest.Network(t, s, app))
}

// The owner starts and stops the node, and sees how it is.
func TestOwnerControlsTheNode(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	o := owner(t, s)
	t.Cleanup(func() { o.Stop() })

	if status, err := o.Status(); err != nil || status.Configured || status.Running || o.Running() {
		t.Fatalf("a fresh node: %+v, %v", status, err)
	}
	if err := o.Start(); err != plugin.ErrNotSetUp {
		t.Fatalf("a node that is not set up: %v", err)
	}
	self := func() (string, error) { return "/usr/bin/ouro", nil }
	cmd, err := o.WithBinary(self).SetupCommand()
	if err != nil || cmd.Path != "/usr/bin/ouro" || strings.Join(cmd.Args[1:], " ") != "start --setup" {
		t.Fatalf("setup: %+v, %v", cmd, err)
	}
	unknown := errors.New("no binary")
	if _, err := o.WithBinary(func() (string, error) { return "", unknown }).SetupCommand(); !errors.Is(err, unknown) {
		t.Fatalf("a binary that cannot be found: %v", err)
	}
	o.WithBinary(nil)

	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	if err := o.Start(); err != nil {
		t.Fatal(err)
	}
	if status, err := o.Status(); err != nil || !status.Running || !o.Running() || status.Name != "alpha" || status.Port == 0 {
		t.Fatalf("a started node: %+v, %v", status, err)
	}
	if err := o.Stop(); err != nil {
		t.Fatal(err)
	}
	if o.Running() {
		t.Fatal("the node should be stopped")
	}
}

// The owner sees the connection requests in both directions.
func TestOwnerRequests(t *testing.T) {
	s := clitest.Settings(t)
	o, store := owner(t, s), home.Dir(s.Home).Contacts()
	store.AddIncoming("pk-in", "in", "", "")
	store.AddOutgoing("pk-out", "out", "", "")
	incoming, outgoing, err := o.Requests()
	if err != nil || len(incoming) != 1 || incoming[0].Name != "in" || len(outgoing) != 1 || outgoing[0].Name != "out" {
		t.Fatalf("got %+v, %+v, %v", incoming, outgoing, err)
	}
}

// A decision on a connection request that cannot be stored reaches the front
// end as ErrNotSaved, without the cause, which is put down in the log instead.
func TestOwnerDecisionThatIsNotSaved(t *testing.T) {
	s := clitest.Settings(t)
	o, dir := owner(t, s), home.Dir(s.Home)
	ctx := context.Background()
	if err := dir.Contacts().AddIncoming("pk-gamma", "gamma", "", ""); err != nil {
		t.Fatal(err)
	}

	// A directory in the place of the lock: the contacts cannot be changed.
	lock := filepath.Join(s.Home, "contacts.json.lock")
	os.Remove(lock)
	os.Mkdir(lock, 0o700)
	for action, decide := range map[string]func(context.Context, string) error{"accept": o.Accept, "reject": o.Reject} {
		if err := decide(ctx, "pk-gamma"); err != plugin.ErrNotSaved {
			t.Fatalf("%s: %v", action, err)
		}
	}
	logged, _ := os.ReadFile(dir.Service().LogFile)
	for _, want := range []string{"accept the connection request of pk-gamma", "reject the connection request of pk-gamma", "contacts.json.lock"} {
		if !strings.Contains(string(logged), want) {
			t.Fatalf("log %q should contain %q", logged, want)
		}
	}

	// Nothing was decided: once the contacts can be written, it is.
	os.Remove(lock)
	if d, _ := dir.Contacts().Snapshot(); len(d.Incoming) != 1 || len(d.Trusted) != 0 || len(d.Blocked) != 0 {
		t.Fatalf("a decision that failed changed the contacts: %+v", d)
	}
	if err := o.Accept(ctx, "pk-gamma"); err != nil {
		t.Fatal(err)
	}
	if err := o.Reject(ctx, "pk-gamma"); !errors.Is(err, plugin.ErrNoRequest) {
		t.Fatalf("a request that is decided: %v", err)
	}
}

// propose makes the agent of the owner of s propose a change, and returns
// what the chat printed.
func propose(t *testing.T, s cli.Settings, path string, value any, reason string) string {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"path": path, "value": value, "reason": reason})
	out, err := clitest.Run(t, s, "", "chat", "!propose_config "+string(args))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// What the agent proposes for the configuration changes nothing until the
// owner accepts it, and holds at once then: the node that runs answers peers
// with the new configuration, without a restart.
func TestOwnerDecidesOnProposals(t *testing.T) {
	userHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", userHome)
	os.Mkdir(filepath.Join(userHome, "share"), 0o755)
	os.WriteFile(filepath.Join(userHome, "share", "hello.txt"), []byte("hello peer"), 0o644)

	alpha, beta := clitest.Settings(t), clitest.Settings(t)
	clitest.StartNode(t, alpha, clitest.NodeConfig(alpha))
	cfg := clitest.NodeConfig(beta)
	cfg.Name = "beta"
	clitest.StartNode(t, beta, cfg)
	clitest.Finds(alpha, beta)
	clitest.Finds(beta, alpha)
	clitest.Trust(t, alpha, beta, "beta")
	clitest.Trust(t, beta, alpha, "alpha")
	o := owner(t, alpha)
	reads := func() string {
		t.Helper()
		out, err := clitest.Run(t, beta, "", "ask", clitest.NodeID(t, alpha), `!read {"path": "~/share/hello.txt"}`)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	if list, err := o.Proposals(); err != nil || len(list) != 0 {
		t.Fatalf("nothing proposed: %+v, %v", list, err)
	}
	out := propose(t, alpha, "tools.a2a", []any{map[string]any{"tool": "read", "allow": []string{"~/share"}}}, "beta wants to read the share")
	if !strings.Contains(out, "last=Proposed, and nothing is changed yet: tools.a2a is to become") || !strings.Contains(out, "R") {
		t.Fatalf("the agent was told: %q", out)
	}
	list, err := o.Proposals()
	if err != nil || len(list) != 1 || list[0].Field != "tools.a2a" || list[0].Now != `[]` || list[0].Then != `[{"allow":["~/share"],"tool":"read"}]` ||
		list[0].Reason != "beta wants to read the share" || list[0].Stale || list[0].ID == "" || list[0].At.IsZero() {
		t.Fatalf("waiting: %+v, %v", list, err)
	}
	if clitest.MustLoad(t, alpha).Tools != nil {
		t.Fatal("the proposal changed the configuration")
	}
	if got := reads(); !strings.Contains(got, "turns=1 ") {
		t.Fatalf("before the owner decided, beta got: %q", got)
	}

	if err := o.AcceptProposal(list[0].ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := clitest.MustLoad(t, alpha).Field("tools.a2a"); string(got) != list[0].Then {
		t.Fatalf("the configuration after accepting: %s", got)
	}
	// The node was not restarted.
	if got := reads(); got != "[gpt-4o] turns=3 last=hello peer\n" {
		t.Fatalf("after the owner accepted, beta got: %q", got)
	}
	if list, err := o.Proposals(); err != nil || len(list) != 0 {
		t.Fatalf("still waiting: %+v, %v", list, err)
	}
	logged, _ := os.ReadFile(home.Dir(alpha.Home).Service().LogFile)
	if !strings.Contains(string(logged), `the owner accepted a proposal: tools.a2a is now [{"allow":["~/share"],"tool":"read"}]`) {
		t.Fatalf("the log: %s", logged)
	}
	if err := o.AcceptProposal("gone"); err != plugin.ErrNoProposal {
		t.Fatalf("accepting what is not there: %v", err)
	}

	// A new name holds at once as well: beta finds the node under it.
	propose(t, alpha, "name", "omega", "a better name")
	list, _ = o.Proposals()
	if len(list) != 1 || list[0].Now != `"alpha"` || list[0].Then != `"omega"` {
		t.Fatalf("waiting: %+v", list)
	}
	if err := o.AcceptProposal(list[0].ID); err != nil {
		t.Fatal(err)
	}
	if out, err := clitest.Run(t, beta, "", "list"); err != nil || !strings.Contains(out, "omega") || strings.Contains(out, "alpha") {
		t.Fatalf("beta finds: %q, %v", out, err)
	}

	// A refused proposal is gone and changed nothing.
	propose(t, alpha, "tools.a2a", []string{"*"}, "everything for everybody")
	list, _ = o.Proposals()
	if err := o.RefuseProposal(list[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := o.RefuseProposal(list[0].ID); err != plugin.ErrNoProposal {
		t.Fatalf("refusing twice: %v", err)
	}
	if got, _ := clitest.MustLoad(t, alpha).Field("tools.a2a"); string(got) != `[{"allow":["~/share"],"tool":"read"}]` {
		t.Fatalf("a refused proposal changed the configuration: %s", got)
	}

	// The configuration changed since: the owner sees that, and accepting
	// applies nothing.
	propose(t, alpha, "model", "gpt-4o-mini", "cheaper")
	if err := config.Update(alpha.Home, func(c *config.Config) error {
		c.Model = "o3-mini"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	list, _ = o.Proposals()
	if len(list) != 1 || !list[0].Stale || list[0].Now != `"o3-mini"` || list[0].Then != `"gpt-4o-mini"` {
		t.Fatalf("a stale proposal: %+v", list)
	}
	if err := o.AcceptProposal(list[0].ID); err != plugin.ErrStale {
		t.Fatalf("accepting a stale proposal: %v", err)
	}
	if clitest.MustLoad(t, alpha).Model != "o3-mini" {
		t.Fatal("a stale proposal was applied")
	}
	if list, _ := o.Proposals(); len(list) != 0 {
		t.Fatalf("a stale proposal still waits: %+v", list)
	}

	// What cannot be read is an error, not an empty list.
	propose(t, alpha, "model", "gpt-4o-mini", "cheaper")
	saved, _ := os.ReadFile(filepath.Join(alpha.Home, "config.json"))
	os.WriteFile(filepath.Join(alpha.Home, "config.json"), []byte("{"), 0o600)
	if list, err := o.Proposals(); err == nil || list != nil {
		t.Fatalf("a configuration that cannot be read: %+v, %v", list, err)
	}
	os.WriteFile(filepath.Join(alpha.Home, "config.json"), saved, 0o600)
	os.WriteFile(filepath.Join(alpha.Home, "proposals.json"), []byte("nope"), 0o600)
	if list, err := o.Proposals(); err == nil || list != nil {
		t.Fatalf("proposals that cannot be read: %+v, %v", list, err)
	}
}

// The tools for the configuration are no tools for peers: a node that lists
// them for peers does not start, so that nobody believes to have allowed
// them.
func TestConfigToolsAreNotForPeers(t *testing.T) {
	for _, name := range []string{"get_config", "propose_config"} {
		s := clitest.Settings(t)
		cfg := clitest.NodeConfig(s)
		cfg.Tools = &config.Tools{A2A: config.Named("list_peers", name)}
		config.Save(s.Home, cfg)
		if _, err := clitest.Run(t, s, "", "serve"); err == nil || !strings.Contains(err.Error(), "tools.a2a: the tool "+name+" is for the owner of the node only: it is no tool for peers") {
			t.Errorf("%s in a2a: %v", name, err)
		}
	}
	// Every tool for peers leaves them out, and a peer that asks for them gets none.
	alpha, beta := clitest.Settings(t), clitest.Settings(t)
	cfg := clitest.NodeConfig(alpha)
	cfg.Tools = &config.Tools{A2A: config.Named("*")}
	clitest.StartNode(t, alpha, cfg)
	if err := config.Save(beta.Home, clitest.NodeConfig(beta)); err != nil {
		t.Fatal(err)
	}
	clitest.StartNode(t, beta, clitest.NodeConfig(beta))
	clitest.Finds(beta, alpha)
	clitest.Trust(t, alpha, beta, "beta")
	clitest.Trust(t, beta, alpha, "alpha")
	for _, message := range []string{`!get_config`, `!propose_config {"path": "tools.a2a", "value": ["*"], "reason": "let me in"}`} {
		out, err := clitest.Run(t, beta, "", "ask", clitest.NodeID(t, alpha), message)
		if err != nil || !strings.Contains(out, "turns=1 ") {
			t.Errorf("%s: %q, %v", message, out, err)
		}
	}
	if list, err := owner(t, alpha).Proposals(); err != nil || len(list) != 0 {
		t.Fatalf("a peer proposed: %+v, %v", list, err)
	}
	// The owner sees the configuration, without its secrets.
	out, err := clitest.Run(t, alpha, "", "chat", "!get_config")
	if err != nil || !strings.Contains(out, `"name": "alpha"`) || strings.Contains(out, "sk-good") || strings.Contains(out, clitest.MustLoad(t, alpha).PrivateKey) {
		t.Fatalf("get_config: %q, %v", out, err)
	}
}
