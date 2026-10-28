package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/llm"
	"ouroboros/internal/llm/llmtest"
	"ouroboros/internal/memory"
	"ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
	openaiplugin "ouroboros/internal/plugins/openai"
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

// llms are the providers of the node of s: the one for OpenAI-compatible
// APIs, with what the settings offer, and one that is none.
func llms(s cli.Settings) []plugin.Provider {
	return []plugin.Provider{openaiplugin.Provider(openaiplugin.Offer{BaseURL: s.BaseURL, APIKey: s.APIKey}), llmtest.Provider("two")}
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

// The owner sets the node up in a dialog of a front end, with the providers
// of this ouro and what the settings offer: the setup saves what the node is
// started with.
func TestOwnerSetsUpTheNode(t *testing.T) {
	s := clitest.Settings(t)
	s.APIKey = openaitest.Key
	o := owner(t, s)
	set, err := o.Setup(llms(s))
	if err != nil || len(set.Providers()) != 2 {
		t.Fatalf("got %+v, %v", set, err)
	}
	// The key that is offered is known for the endpoint offered.
	d := &llmtest.Dialog{Answers: map[string][]string{"LLM endpoint": {""}, "API key": {""}}}
	models, err := set.Login(context.Background(), "openai", d)
	if err != nil || !d.Asked()[1].Known {
		t.Fatalf("signing in: %+v, %v", d.Asked(), err)
	}
	if err := set.Save(models.Default, "alpha"); err != nil {
		t.Fatal(err)
	}
	if status, err := o.Status(); err != nil || !status.Configured || status.Name != "alpha" || status.Provider != "localhost" {
		t.Fatalf("a node that was set up: %+v, %v", status, err)
	}
	if endpoint, key := clitest.Reached(t, s); endpoint != s.BaseURL || key != openaitest.Key {
		t.Fatalf("the node reaches %q with %q", endpoint, key)
	}
	bad := &llmtest.Dialog{Answers: map[string][]string{"LLM endpoint": {""}, "API key": {"sk-bad"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		for len(bad.Rejected()) == 0 && ctx.Err() == nil {
			time.Sleep(time.Millisecond)
		}
		cancel() // the owner gives up at the second question for the key
	}()
	if _, err := set.Login(ctx, "openai", bad); !errors.Is(err, context.Canceled) || !slices.Equal(bad.Rejected(), []string{"API key rejected, try again"}) {
		t.Fatalf("a bad key: %v, rejected %v", err, bad.Rejected())
	}

	broken := clitest.Settings(t)
	if err := os.WriteFile(filepath.Join(broken.Home, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := owner(t, broken).Setup(llms(broken)); err == nil {
		t.Fatal("a configuration that cannot be read: want error")
	}
	if _, err := owner(t, broken).Models(context.Background(), llms(broken)); err == nil {
		t.Fatal("models of a configuration that cannot be read: want error")
	}
	if _, err := owner(t, broken).SetModel("a", "m"); err == nil {
		t.Fatal("a model for a configuration that cannot be read: want error")
	}
	if _, err := owner(t, broken).ToggleFallback("a", "m"); err == nil {
		t.Fatal("a fallback for a configuration that cannot be read: want error")
	}
	if err := owner(t, broken).SetEffort("low"); err == nil {
		t.Fatal("an effort for a configuration that cannot be read: want error")
	}
	if _, known := owner(t, broken).Efforts(llms(broken)); known {
		t.Fatal("the efforts of a configuration that cannot be read are known")
	}
	if _, err := owner(t, broken).Fallbacks(); err == nil {
		t.Fatal("the fallbacks of a configuration that cannot be read: want error")
	}
	if err := owner(t, broken).SetPeerModel("a", "m"); err == nil {
		t.Fatal("a model for peers for a configuration that cannot be read: want error")
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

// The owner changes the model of the node in the model screen of a front
// end: Models lists what every access offers, and SetModel makes a model of
// one of them the model of the node. Picking one of another access switches
// the provider, without a setup and without a restart.
func TestOwnerChangesTheModel(t *testing.T) {
	s := clitest.Settings(t)
	o := owner(t, s)

	// A node that is not set up has no models, and no model to save.
	if _, err := o.Models(context.Background(), llms(s)); !errors.Is(err, plugin.ErrNotSetUp) {
		t.Fatalf("a node that is not set up: %v", err)
	}
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}

	listed, err := o.Models(context.Background(), llms(s))
	if err != nil || len(listed) != 1 {
		t.Fatalf("got %+v, %v", listed, err)
	}
	// The models that cannot chat are not listed.
	first := listed[0]
	if !slices.Equal(first.Models, []string{"gpt-4o", "gpt-4o-mini", "o3-mini"}) || first.Err != "" ||
		!first.Access.Active || first.Access.Provider != "openai" || first.Access.Label != "localhost" || first.Access.About != s.BaseURL {
		t.Fatalf("got %+v", first)
	}
	if switched, err := o.SetModel(first.Access.ID, "  gpt-4o-mini  "); err != nil || switched {
		t.Fatalf("SetModel = %v, %v", switched, err)
	}
	cfg := clitest.MustLoad(t, s)
	if endpoint, key := clitest.Reached(t, s); cfg.Model != "gpt-4o-mini" || cfg.Name != "alpha" || endpoint != s.BaseURL || key != openaitest.Key {
		t.Fatalf("the configuration after saving: %+v, at %q with %q", cfg, endpoint, key)
	}
	// An empty model changes nothing.
	if _, err := o.SetModel(first.Access.ID, "   "); err == nil {
		t.Fatal("an empty model: want error")
	}
	if cfg := clitest.MustLoad(t, s); cfg.Model != "gpt-4o-mini" {
		t.Fatalf("an empty model changed the configuration: %+v", cfg)
	}

	// A second access, of another provider: its models are listed next to
	// those of the first, and one of them is picked.
	set, _ := o.Setup(llms(s))
	if _, err := set.Login(context.Background(), "two", &llmtest.Dialog{Answers: map[string][]string{"Token": {llmtest.Token}}}); err != nil {
		t.Fatal(err)
	}
	if err := set.Save("two-small", "alpha"); err != nil {
		t.Fatal(err)
	}
	if switched, err := o.SetModel(first.Access.ID, "gpt-4o"); err != nil || !switched {
		t.Fatalf("back to the first access: %v, %v", switched, err)
	}
	listed, err = o.Models(context.Background(), llms(s))
	if err != nil || len(listed) != 2 || !listed[0].Access.Active || listed[1].Access.Active ||
		!slices.Equal(listed[1].Models, []string{"two-small", "two-large"}) || listed[1].Access.Label != "Test two" {
		t.Fatalf("got %+v, %v", listed, err)
	}
	if switched, err := o.SetModel(listed[1].Access.ID, "two-large"); err != nil || !switched {
		t.Fatalf("SetModel of another access = %v, %v", switched, err)
	}
	if cfg := clitest.MustLoad(t, s); cfg.Access != listed[1].Access.ID || cfg.Model != "two-large" {
		t.Fatalf("picked %+v", cfg)
	}
	if status, _ := o.Status(); status.Provider != "Test two" || status.Model != "two-large" {
		t.Fatalf("status %+v", status)
	}
	// The other access is what the node falls back on, until it is not.
	if list, err := o.Fallbacks(); err != nil || len(list) != 0 {
		t.Fatalf("no fallbacks yet: %+v, %v", list, err)
	}
	fallback := plugin.Fallback{Access: first.Access.ID, Model: "gpt-4o"}
	if list, err := o.ToggleFallback(fallback.Access, fallback.Model); err != nil || len(list) != 1 || list[0] != fallback {
		t.Fatalf("ToggleFallback = %+v, %v", list, err)
	}
	if list, err := o.Fallbacks(); err != nil || len(list) != 1 || list[0] != fallback {
		t.Fatalf("Fallbacks = %+v, %v", list, err)
	}
	if _, err := o.ToggleFallback(listed[1].Access.ID, "two-large"); err == nil {
		t.Fatal("the model in use as its own fallback: want error")
	}
	if list, err := o.ToggleFallback(fallback.Access, fallback.Model); err != nil || len(list) != 0 {
		t.Fatalf("ToggleFallback again = %+v, %v", list, err)
	}
	if log, _ := os.ReadFile(filepath.Join(s.Home, "ouro.log")); strings.Count(string(log), "the owner changed the fallbacks to ") != 2 ||
		!strings.Contains(string(log), "the owner changed the fallbacks to [{Access:"+fallback.Access+" Model:gpt-4o}]") {
		t.Fatalf("log: %s", log)
	}
	// Peers get a model of their own, of the other access, and lose it again.
	if err := o.SetPeerModel(first.Access.ID, "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	if status, _ := o.Status(); status.PeerAccess != first.Access.ID || status.PeerModel != "gpt-4o-mini" || status.Model != "two-large" {
		t.Fatalf("status %+v", status)
	}
	if err := o.SetPeerModel(first.Access.ID, " "); err == nil {
		t.Fatal("no model for peers: want error")
	}
	if err := o.SetPeerModel("", ""); err != nil {
		t.Fatal(err)
	}
	if status, _ := o.Status(); status.PeerAccess != "" || status.PeerModel != "" {
		t.Fatalf("status %+v", status)
	}
	log, _ := os.ReadFile(filepath.Join(s.Home, "ouro.log"))
	if n := strings.Count(string(log), "for peers"); n != 2 || !strings.Contains(string(log), `the owner picked the model "gpt-4o-mini" of the access "`+first.Access.ID+`" for peers`) ||
		!strings.Contains(string(log), `the owner picked the model "" of the access "" for peers`) {
		t.Fatalf("log (%d): %s", n, log)
	}
	if !strings.Contains(string(log), "the owner picked the model two-large of the access "+listed[1].Access.ID) || strings.Contains(string(log), llmtest.Token+`"`) {
		t.Fatalf("log: %s", log)
	}

	// The models are listed with the key of the node: one the provider
	// refuses shows with the access, and keeps the others.
	bad := clitest.Settings(t)
	refused := clitest.NodeConfig(bad)
	refused.APIKey = "sk-bad"
	if err := config.Save(bad.Home, refused); err != nil {
		t.Fatal(err)
	}
	if listed, err := owner(t, bad).Models(context.Background(), llms(bad)); err != nil || len(listed) != 1 || listed[0].Err != plugin.ErrKeyRejected.Error() || listed[0].Models != nil {
		t.Fatalf("a key the provider refuses: %+v, %v", listed, err)
	}
}

// A provider opens the page it signs in on through the front end.
func TestBrowse(t *testing.T) {
	old := llm.Browse
	t.Cleanup(func() { llm.Browse = old })
	var opened string
	llm.Browse = func(url string) error {
		opened = url
		return errors.New("no browser")
	}
	if err := plugin.Browse("https://example.com"); err == nil || opened != "https://example.com" {
		t.Fatalf("Browse = %v, opened %q", err, opened)
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

// seedProposal records a legacy pending proposal, as persisted by older builds.
// New propose_config calls ask directly and no longer populate this store.
func seedProposal(t *testing.T, s cli.Settings, path string, value any, reason string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := home.Dir(s.Home).Proposals().Add(clitest.MustLoad(t, s), path, raw, reason); err != nil {
		t.Fatal(err)
	}
}

// Legacy pending proposals still require an explicit owner decision. Accepting
// one updates the running node without a restart.
func TestOwnerDecidesOnLegacyProposals(t *testing.T) {
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
	seedProposal(t, alpha, "tools.a2a", []any{map[string]any{"tool": "read", "allow": []string{"~/share"}}}, "beta wants to read the share")
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
	seedProposal(t, alpha, "name", "omega", "a better name")
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
	// A name another profile has, whatever its case, is not taken.
	if _, err := home.Dir(alpha.Home).AddProfile("zweite"); err != nil {
		t.Fatal(err)
	}
	seedProposal(t, alpha, "name", "ZWEITE", "another name")
	list, _ = o.Proposals()
	if err := o.AcceptProposal(list[0].ID); err == nil || err.Error() != `there is a profile called "ZWEITE" already` {
		t.Fatalf("the name of another profile: %v", err)
	}
	// Only the name of the proposal that is accepted is checked: another one
	// is accepted while that name waits.
	seedProposal(t, alpha, "description", "knows the way", "say what it does")
	list, _ = o.Proposals()
	if len(list) != 2 || list[1].Field != "description" {
		t.Fatalf("waiting: %+v", list)
	}
	if err := o.AcceptProposal(list[1].ID); err != nil || clitest.MustLoad(t, alpha).Description != "knows the way" {
		t.Fatalf("a description while a name waits: %v", err)
	}
	os.WriteFile(filepath.Join(alpha.Home, "proposals.json"), []byte("{"), 0o600)
	if err := o.AcceptProposal(list[0].ID); err == nil {
		t.Fatal("proposals that cannot be read")
	}
	os.Remove(filepath.Join(alpha.Home, "proposals.json"))

	// A refused proposal is gone and changed nothing.
	seedProposal(t, alpha, "tools.a2a", []string{"*"}, "everything for everybody")
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
	seedProposal(t, alpha, "model", "gpt-4o-mini", "cheaper")
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
	seedProposal(t, alpha, "model", "gpt-4o-mini", "cheaper")
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

// The owner sets the effort, the status says it, and the log says so; an
// effort that is none is refused.
func TestOwnerSetsTheEffort(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	o := owner(t, s)
	if err := o.SetEffort("high"); !errors.Is(err, config.ErrIncomplete) {
		t.Fatalf("a node that is not set up: %v", err)
	}
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	if err := o.SetEffort("high"); err != nil {
		t.Fatal(err)
	}
	if status, err := o.Status(); err != nil || status.Effort != "high" {
		t.Fatalf("status %+v, %v", status, err)
	}
	if err := o.SetEffort("huge"); err == nil {
		t.Fatal("no such effort: want error")
	}
	log, _ := os.ReadFile(home.Dir(s.Home).Service().LogFile)
	if !strings.Contains(string(log), `the owner set the effort to "high"`) || strings.Contains(string(log), "huge") {
		t.Fatalf("the log:\n%s", log)
	}
	if !slices.Contains(plugin.Efforts, "max") || plugin.Efforts[0] != "" || !slices.Contains(plugin.AllEfforts, "xhigh") {
		t.Fatalf("efforts %q", plugin.Efforts)
	}
}

// The efforts the model of the node takes are what models.dev lists of it,
// as kept with the node: its access is known there by its endpoint.
func TestOwnerEfforts(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	listed := fmt.Sprintf(`{"at": %q, "providers": {%q: {"gpt-4o": {"efforts": ["low", "max"]}}}}`, time.Now().Format(time.RFC3339), s.BaseURL)
	if err := os.WriteFile(home.Dir(s.Home).Catalog(), []byte(listed), 0o600); err != nil {
		t.Fatal(err)
	}
	efforts, known := owner(t, s).Efforts(llms(s))
	if !known || !slices.Equal(efforts, []string{"low", "max"}) {
		t.Fatalf("efforts %q, %v", efforts, known)
	}
}

// The owner turns the debug mode of the node on and off, and the log says so.
func TestOwnerSetsDebug(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	o := owner(t, s)
	dir := home.Dir(s.Home)
	for _, on := range []bool{true, false} {
		if err := o.SetDebug(on); err != nil || dir.Debug() != on {
			t.Fatalf("set %v: %v, debug %v", on, err, dir.Debug())
		}
	}
	log, _ := os.ReadFile(dir.Service().LogFile)
	if !strings.Contains(string(log), "debug mode on") || !strings.Contains(string(log), "debug mode off") {
		t.Fatalf("the log:\n%s", log)
	}
	if err := os.MkdirAll(filepath.Join(s.Home, "debug", "in"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := o.SetDebug(false); err == nil {
		t.Fatal("a debug that cannot be removed")
	}
}

// The owner keeps how the terminal UI looks, for all profiles: system until
// another is chosen, and only one of the appearances.
func TestOwnerKeepsTheAppearance(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	o := owner(t, s)
	if got := o.Appearance(); got != "system" {
		t.Fatalf("before a choice: %q", got)
	}
	if err := o.SetAppearance("light"); err != nil || o.Appearance() != "light" {
		t.Fatalf("light: %v, %q", err, o.Appearance())
	}
	if err := o.SetAppearance("pink"); err == nil || o.Appearance() != "light" {
		t.Fatalf("pink: %v, %q", err, o.Appearance())
	}
	if err := os.WriteFile(home.Dir(s.Home).Appearance(), []byte("pink\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := o.Appearance(); got != "system" {
		t.Fatalf("a file of another: %q", got)
	}
}

// A provider that is no part of ouro tells the debug log of the node what
// its calls send and get, with what the package hands out and nothing else.
func TestProviderTrace(t *testing.T) {
	if plugin.Trace(context.Background()) != nil {
		t.Fatal("a node that is not in debug mode has a trace")
	}
	var lines []string
	ctx := openai.WithTrace(context.Background(), func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	trace := plugin.Trace(ctx)
	if trace == nil {
		t.Fatal("no trace in debug mode")
	}
	var requests plugin.Requests
	first := [][]byte{[]byte(`{"role":"user","content":"hi"}`)}
	trace("llm prefix: %s", requests.Compare("conv", "tools", first))
	trace("llm prefix: %s", requests.Compare("conv", "tools", append(first, []byte(`{"role":"assistant","content":"ho"}`))))
	plugin.TraceResponse(trace, &http.Response{StatusCode: 200, Header: http.Header{"X-Cache": {"hit"}}}, 2*time.Second)
	want := []string{
		"llm prefix: first request of the session in the trace",
		"llm prefix: all 1 messages of the last request are kept, 1 are new; 46.2% of the bytes are the same",
		"llm response: HTTP 200 after 2s, headers {X-Cache: hit}",
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("told %q", lines)
	}
}

// A picture the owner pasted into the chat is kept as a file of its own in
// the workspace of the profile, which only the owner reads.
func TestOwnerSavesAPicture(t *testing.T) {
	if _, err := plugin.OwnerOf(home.Dir(t.TempDir()), nil).SavePicture([]byte("PNG"), "png"); !errors.Is(err, plugin.ErrNotSetUp) {
		t.Fatalf("a node that is not set up has no workspace: %v", err)
	}
	s := clitest.Settings(t)
	o := owner(t, s)
	cfg := clitest.NodeConfig(s)
	cfg.PrivateKey = identity.Generate().Private()
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	workspace := home.Dir(s.Home).WorkingDir()
	first, err := o.SavePicture([]byte("PNG"), "png")
	if err != nil || filepath.Dir(first) != filepath.Join(workspace, "pasted") || filepath.Ext(first) != ".png" {
		t.Fatalf("kept as %q, %v", first, err)
	}
	second, err := o.SavePicture([]byte("JPEG"), "jpg")
	if err != nil || second == first || filepath.Ext(second) != ".jpg" {
		t.Fatalf("kept as %q, %v", second, err)
	}
	data, _ := os.ReadFile(first)
	info, _ := os.Stat(first)
	dir, _ := os.Stat(filepath.Dir(first))
	if string(data) != "PNG" || info.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
		t.Fatalf("%q, %v in %v", data, info.Mode(), dir.Mode())
	}
	if _, err := o.SavePicture([]byte("PNG"), "p/ng"); err == nil {
		t.Fatal("a kind that is no extension")
	}
	if err := os.RemoveAll(filepath.Join(workspace, "pasted")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "pasted"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SavePicture([]byte("PNG"), "png"); err == nil {
		t.Fatal("a workspace that cannot keep it")
	}
}

// A Model reads the effort of a call from its context.
func TestEffortOfACall(t *testing.T) {
	if got := plugin.Effort(openai.WithEffort(context.Background(), "low")); got != "low" {
		t.Fatalf("effort %q", got)
	}
}

// What the reviews suggested waits until the owner takes or drops it, and
// either is in the log.
func TestOwnerDecidesOnSuggestions(t *testing.T) {
	s := clitest.Settings(t)
	o := owner(t, s)
	if list, err := o.Suggestions(); err != nil || len(list) != 0 {
		t.Fatalf("nothing suggested: %+v, %v", list, err)
	}
	store := home.Dir(s.Home).Suggestions()
	a, _, _ := store.Add("let bash run long commands as jobs", []string{"bash took 42s"}, "s1")
	b, _, _ := store.Add("say which files are near", nil, "s2")
	if list, err := o.Suggestions(); err != nil || len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("waiting: %+v, %v", list, err)
	}
	got, err := o.TakeSuggestion(a.ID)
	if err != nil || got.Text != a.Text || got.Signals[0] != "bash took 42s" {
		t.Fatalf("taken %+v, %v", got, err)
	}
	// The agent is asked to make what the owner took, with what was measured.
	if task := plugin.SuggestedTask(got); task != assistant.SuggestedTask(a.Text, a.Signals) ||
		!strings.Contains(task, "\n\nlet bash run long commands as jobs\n\nWhat was measured of that answer: bash took 42s.") {
		t.Fatalf("task %q", task)
	}
	if err := o.DropSuggestion(b.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := o.Suggestions(); err != nil || len(list) != 0 {
		t.Fatalf("still waiting: %+v, %v", list, err)
	}
	if _, err := o.TakeSuggestion(a.ID); !errors.Is(err, plugin.ErrNoSuggestion) {
		t.Fatalf("taken twice: %v", err)
	}
	if err := o.DropSuggestion("gone"); !errors.Is(err, plugin.ErrNoSuggestion) {
		t.Fatalf("dropped what is none: %v", err)
	}
	logged, _ := os.ReadFile(home.Dir(s.Home).Service().LogFile)
	for _, want := range []string{`the owner took a suggestion: "let bash run long commands as jobs"`, `the owner dropped a suggestion: "say which files are near"`} {
		if !strings.Contains(string(logged), want) {
			t.Fatalf("the log lacks %q: %s", want, logged)
		}
	}
}

// What the review of an answer remembered the owner can take back: a new
// memory is forgotten, one it replaced is as it was. What changed since, or
// is no memory, is not taken back, and what is taken back is in the log.
func TestOwnerUndoesWhatWasRemembered(t *testing.T) {
	s := clitest.Settings(t)
	o := owner(t, s)
	store, err := memory.Open(home.Dir(s.Home).History())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	args := func(e, was memory.Entry) json.RawMessage {
		raw, _ := json.Marshal(plugin.Remembered{Memory: e, Was: was})
		return raw
	}
	e, was, _ := store.Remember(ctx, "", "style", "short answers", "c", false, time.Time{})
	again, before, _ := store.Remember(ctx, "", "style", "very short answers", "c", false, time.Time{})
	if err := o.UndoRemembered(args(again, before)); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Read(ctx, "", e.ID); got.Content != "short answers" {
		t.Fatalf("not as it was: %+v", got)
	}
	if err := o.UndoRemembered(args(again, before)); !errors.Is(err, plugin.ErrMemoryChanged) {
		t.Fatalf("taken back twice: %v", err)
	}
	now, _ := store.Read(ctx, "", e.ID)
	if err := o.UndoRemembered(args(now, was)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ctx, "", e.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("a new memory is not forgotten: %v", err)
	}
	if err := o.UndoRemembered(json.RawMessage(`"nope"`)); err == nil {
		t.Fatal("took back what is no memory")
	}
	logged, _ := os.ReadFile(home.Dir(s.Home).Service().LogFile)
	if strings.Count(string(logged), `the owner took back a memory: "style"`) != 2 {
		t.Fatalf("the log: %s", logged)
	}
	bad := clitest.Settings(t)
	if err := os.WriteFile(home.Dir(bad.Home).History(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := owner(t, bad).UndoRemembered(args(e, was)); err == nil {
		t.Fatal("took back without a store")
	}
}

// A front end edits a memory in the memory dialog: what the owner typed
// replaces it if it did not change since they read it, and an edit of a
// memory that changed since fails as one taken back does. What was edited
// is in the log.
func TestOwnerEditsAMemory(t *testing.T) {
	s := clitest.Settings(t)
	o := owner(t, s)
	store, err := memory.Open(home.Dir(s.Home).History())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	e, _, err := store.Remember(ctx, "", "style", "short answers", "c", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	edited, err := o.SetMemory(ctx, "", e.ID, e.Version, "style", "very short answers")
	if err != nil {
		t.Fatalf("the owner's edit: %v", err)
	}
	if edited.Content != "very short answers" || edited.Version != e.Version+1 {
		t.Fatalf("the edit did not land: %+v", edited)
	}
	if _, err := o.SetMemory(ctx, "", e.ID, e.Version, "style", "terse"); !errors.Is(err, plugin.ErrMemoryChanged) {
		t.Fatalf("an edit of what changed since should fail as a conflict: %v", err)
	}
	if _, err := o.SetMemory(ctx, "", "gone", 1, "style", "terse"); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("an edit of no memory: %v", err)
	}
	if _, err := o.SetMemory(ctx, "", e.ID, edited.Version, "", "terse"); err == nil {
		t.Fatal("a memory without a key was taken")
	}
	logged, _ := os.ReadFile(home.Dir(s.Home).Service().LogFile)
	if !strings.Contains(string(logged), `the owner edited a memory: "style"`) {
		t.Fatalf("the log lacks the edit: %s", logged)
	}
}

// The memory dialog lists the memories the agent of the owner collected, the
// latest updated first. Without a store that can be opened there is nothing
// to list and nothing to edit.
func TestOwnerListsTheMemories(t *testing.T) {
	s := clitest.Settings(t)
	o := owner(t, s)
	store, err := memory.Open(home.Dir(s.Home).History())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := store.Remember(ctx, "", "style", "short answers", "c", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Remember(ctx, "", "name", "Lu", "c", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	listed, err := o.Memories(ctx, "", false)
	if err != nil || len(listed.Memories) != 2 || listed.Total != 2 || listed.Memories[0].Key == listed.Memories[1].Key {
		t.Fatalf("the memories: %+v, %v", listed, err)
	}

	bad := clitest.Settings(t)
	if err := os.WriteFile(home.Dir(bad.Home).History(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := owner(t, bad).Memories(ctx, "", false); err == nil {
		t.Fatal("listed without a store")
	}
	if _, err := owner(t, bad).SetMemory(ctx, "", "id", 1, "style", "terse"); err == nil {
		t.Fatal("edited without a store")
	}
}

// The owner turns the yolo mode of the node on and off, and the log says so.
// A mode that cannot be turned off fails.
func TestOwnerSetsYolo(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	o := owner(t, s)
	dir := home.Dir(s.Home)
	for _, on := range []bool{true, false} {
		if err := o.SetYolo(on); err != nil || dir.Yolo() != on {
			t.Fatalf("set %v: %v, yolo %v", on, err, dir.Yolo())
		}
	}
	log, _ := os.ReadFile(dir.Service().LogFile)
	if !strings.Contains(string(log), "yolo mode on") || !strings.Contains(string(log), "yolo mode off") {
		t.Fatalf("the log:\n%s", log)
	}
	if err := os.MkdirAll(filepath.Join(s.Home, "yolo", "in"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := o.SetYolo(false); err == nil {
		t.Fatal("a yolo mode that cannot be removed")
	}
}

// The owner picks a model for the sessions and one for the thread, and
// gives each back to what the owner is answered with. The log says so, and
// a configuration that cannot be read takes no pick.
func TestOwnerPicksModelsForSessionsAndTheThread(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	o := owner(t, s)
	listed, err := o.Models(context.Background(), llms(s))
	if err != nil || len(listed) != 1 {
		t.Fatalf("models %+v, %v", listed, err)
	}
	access := listed[0].Access.ID
	if err := o.SetSessionModel(access, "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	if err := o.SetThreadModel(access, "o3-mini"); err != nil {
		t.Fatal(err)
	}
	if cfg := clitest.MustLoad(t, s); cfg.SessionAccess != access || cfg.SessionModel != "gpt-4o-mini" || cfg.ThreadAccess != access || cfg.ThreadModel != "o3-mini" {
		t.Fatalf("the configuration: %+v", cfg)
	}
	if err := o.SetSessionModel(access, " "); err == nil {
		t.Fatal("no model for the sessions: want error")
	}
	if err := o.SetThreadModel(access, " "); err == nil {
		t.Fatal("no model for the thread: want error")
	}
	if err := o.SetSessionModel("", ""); err != nil {
		t.Fatal(err)
	}
	if err := o.SetThreadModel("", ""); err != nil {
		t.Fatal(err)
	}
	if cfg := clitest.MustLoad(t, s); cfg.SessionAccess != "" || cfg.SessionModel != "" || cfg.ThreadAccess != "" || cfg.ThreadModel != "" {
		t.Fatalf("the configuration: %+v", cfg)
	}
	log, _ := os.ReadFile(filepath.Join(s.Home, "ouro.log"))
	if !strings.Contains(string(log), `the owner picked the model "gpt-4o-mini" of the access "`+access+`" for sessions`) ||
		!strings.Contains(string(log), `the owner picked the model "o3-mini" of the access "`+access+`" for the thread`) ||
		strings.Count(string(log), "for sessions") != 2 || strings.Count(string(log), "for the thread") != 2 {
		t.Fatalf("log: %s", log)
	}

	broken := clitest.Settings(t)
	if err := os.WriteFile(filepath.Join(broken.Home, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := owner(t, broken).SetSessionModel("a", "m"); err == nil {
		t.Fatal("a model for the sessions for a configuration that cannot be read: want error")
	}
	if err := owner(t, broken).SetThreadModel("a", "m"); err == nil {
		t.Fatal("a model for the thread for a configuration that cannot be read: want error")
	}
}
