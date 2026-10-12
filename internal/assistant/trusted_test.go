package assistant

import (
	"context"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/identity"
	"ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
)

// A trusted tool is for the owner, and for a peer only in the list of that
// peer, by its name: never with every tool, and never for all peers.
func TestTrustedToolsForOnePeerAlone(t *testing.T) {
	run := func(context.Context, struct{}) (string, error) { return "", nil }
	plain := agent.NewTool("plain", "", nil, run)
	build := agent.NewTool("build", "", nil, run)
	build.Trusted = true
	secret := agent.NewTool("secret", "", nil, run)
	secret.OwnerOnly = true
	all := []agent.Tool{plain, build, secret}
	for _, tt := range []struct {
		allowed []config.Entry
		of      audience
		want    string
	}{
		{config.Named("*"), forOwner, "plain,build,secret"},
		{config.Named("build"), forOwner, "build"},
		{config.Named("*"), forPeers, "plain"},
		{config.Named("*"), forOnePeer, "plain"},
		{config.Named("build"), forOnePeer, "build"},
		{config.Named("*", "build"), forOnePeer, "plain,build"},
	} {
		got, err := pick(all, tt.allowed, tt.of)
		if err != nil || names(got) != tt.want {
			t.Errorf("pick(%s, %d) = %q, %v; want %q", tt.allowed, tt.of, names(got), err, tt.want)
		}
	}
	for _, tt := range []struct {
		allowed []config.Entry
		of      audience
		want    string
	}{
		{config.Named("build"), forPeers, "the tool build is no tool for all peers: list it in tools.peers, for a peer you trust that far"},
		{config.Named("secret"), forOnePeer, "the tool secret is for the owner of the node only: it is no tool for peers"},
		{config.Named("nope"), forPeers, `unknown tool "nope" (there are: plain)`},
		{config.Named("nope"), forOnePeer, `unknown tool "nope" (there are: plain, build)`},
	} {
		if got, err := pick(all, tt.allowed, tt.of); err == nil || err.Error() != tt.want || got != nil {
			t.Errorf("pick(%s, %d) = %q, %v; want %q", tt.allowed, tt.of, names(got), err, tt.want)
		}
	}
}

// A peer with a list of its own in tools.peers is answered by an agent with
// those tools, which knows it may build ouro when rebuild is among them; the
// other peers are answered as before.
func TestPeerWithAListOfItsOwn(t *testing.T) {
	luna := identity.Generate()
	cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me", PrivateKey: identity.Generate().Private()}
	cfg.Tools = &config.Tools{A2A: config.Named(toolListPeers), Peers: map[string][]config.Entry{luna.ID(): config.Named(toolListPeers, ToolRebuild)}}
	rebuild := agent.NewTool(ToolRebuild, "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	rebuild.Trusted = true
	b := Builder{LLM: overHTTP, Source: "/src", Tools: func() []agent.Tool { return append(PeerTools(nil, func() string { return "" }), rebuild) }}
	peers := b.Peers(func() (config.Config, error) { return cfg, nil }, discard)
	if err := peers.Ready(); err != nil {
		t.Fatal(err)
	}
	source := " Your source, the checkout " + ToolRebuild + " builds, is /src."
	if got, err := peers.Respond(from(luna.Public()), "c", "?system"); err != nil || !strings.Contains(got, buildHint+source) {
		t.Fatalf("the peer with a list of its own: %q, %v", got, err)
	}
	if got, err := peers.Respond(from(identity.Generate().Public()), "c", "?system"); err != nil || strings.Contains(got, buildHint) {
		t.Fatalf("another peer: %q, %v", got, err)
	}
	b.Source = ""
	bot, err := b.ForPeer(cfg, luna.ID())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := bot.Respond(context.Background(), "c", "?system"); err != nil || !strings.Contains(got, buildHint) || strings.Contains(got, "Your source") {
		t.Fatalf("without a known source: %q, %v", got, err)
	}

	// A list that is no list of tools, or of no node, is an error of the
	// configuration, like one of tools.a2a.
	for want, peers := range map[string]map[string][]config.Entry{
		`tools.peers: "Luna" is no ID of a node`:             {"Luna": config.Named(toolListPeers)},
		"tools.peers." + luna.ID() + `: unknown tool "nope"`: {luna.ID(): config.Named("nope")},
	} {
		cfg.Tools.Peers = peers
		if _, err := b.ForPeers(cfg); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ForPeers: %v, want %q", err, want)
		}
		if err := b.Check(cfg); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Check: %v, want %q", err, want)
		}
	}
}

// A conversation with a peer the agent does not remember, as after a new
// build, goes on with what the archive kept of it.
func TestPeersRecallAConversation(t *testing.T) {
	cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me", PrivateKey: identity.Generate().Private()}
	b := Builder{LLM: overHTTP, Tools: func() []agent.Tool { return nil }}
	peers := b.Peers(func() (config.Config, error) { return cfg, nil }, discard)
	recalled := 0
	peers.Recall(func(_ context.Context, contextID string) []openai.Message {
		recalled++
		if contextID != "old" {
			return nil
		}
		return []openai.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "ho"}}
	})
	for _, want := range []string{"turns=3", "turns=5"} {
		if got, err := peers.Respond(from("luna"), "old", "hello"); err != nil || !strings.HasSuffix(got, want+" last=hello") {
			t.Fatalf("got %q, %v; want %s", got, err, want)
		}
	}
	if got, err := peers.Respond(from("luna"), "new", "hello"); err != nil || !strings.HasSuffix(got, "turns=1 last=hello") || recalled != 2 {
		t.Fatalf("a new conversation: %q, %v, recalled %d times", got, err, recalled)
	}
}
