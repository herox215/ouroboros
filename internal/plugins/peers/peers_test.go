package peers_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
)

func TestAssistantAsksPeers(t *testing.T) {
	alpha, beta := clitest.TwoNodes(t)
	clitest.StartNode(t, alpha, clitest.MustLoad(t, alpha)) // beta's acceptance has to reach alpha
	b, _ := clitest.NewApp(t, beta, "")

	// "@" makes the fake LLM list the peers.
	out, err := clitest.Run(t, alpha, "", "chat", "@")
	if err != nil || !strings.Contains(out, "last=beta (id "+clitest.NodeID(t, beta)+", model gpt-4o-mini, not connected)") || strings.Contains(out, "alpha") {
		t.Fatalf("list_peers: %q, %v", out, err)
	}

	// Alpha is not connected to beta: its agent sends nothing and proposes
	// beta to the owner, who agrees. Only then beta is asked to connect.
	out, err = clitest.Run(t, alpha, "@beta hello\n", "chat")
	if err != nil || !strings.Contains(out, "last=beta (id "+clitest.NodeID(t, beta)+") is not connected") {
		t.Fatalf("first contact: %q, %v", out, err)
	}
	if requests, _ := home.Dir(beta.Home).Contacts().Snapshot(); len(requests.Incoming) != 0 {
		t.Fatalf("the agent of alpha asked beta to connect by itself: %+v", requests)
	}
	a, _ := clitest.NewApp(t, alpha, "")
	if err := clitest.Network(t, alpha, a).Accept(context.Background(), clitest.PublicKey(t, beta)); err != nil {
		t.Fatal(err)
	}
	if out, err = clitest.Run(t, alpha, "@beta hello\n", "chat"); err != nil || !strings.Contains(out, "last=beta was asked to connect, but nothing of your message was sent") {
		t.Fatalf("while beta has not decided: %q, %v", out, err)
	}
	if out, _ = clitest.Run(t, alpha, "", "chat", "@"); !strings.Contains(out, "connection requested") {
		t.Fatalf("the request should be remembered: %q", out)
	}

	if strings.Contains(out, "instance named") {
		t.Fatalf("a peer that is not connected should not get a word in: %q", out)
	}
	requests, _ := home.Dir(beta.Home).Contacts().Snapshot()
	if len(requests.Incoming) != 1 || requests.Incoming[0].Name != "alpha" {
		t.Fatalf("beta's requests: %+v", requests)
	}
	if err := clitest.Network(t, beta, b).Accept(context.Background(), requests.Incoming[0].PublicKey); err != nil {
		t.Fatal(err)
	}

	// Connected: beta's agent answers, and remembers the conversation.
	out, err = clitest.Run(t, alpha, "@beta one\n@beta two\n", "chat")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[gpt-4o] turns=3 last=[gpt-4o-mini] turns=1 last=one",
		"[gpt-4o] turns=7 last=[gpt-4o-mini] turns=3 last=two", // user, call, result, answer, user, call, result
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// What the agents say to each other is not printed, only the assistant's answers.
	if strings.Count(out, "last=one") != 1 || strings.Contains(out, "beta:") {
		t.Errorf("the talk between the agents should stay out of the chat:\n%s", out)
	}
	if out, _ = clitest.Run(t, alpha, "", "chat", "@"); !strings.Contains(out, "model gpt-4o-mini, connected): Ouroboros instance named beta\n") {
		t.Fatalf("beta should be listed as connected, with its card: %q", out)
	}

	// A one-shot chat prints the answer only.
	out, err = clitest.Run(t, alpha, "", "chat", "@beta", "ping")
	if err != nil || out != "[gpt-4o] turns=3 last=[gpt-4o-mini] turns=1 last=ping\n" {
		t.Fatalf("one-shot: %q, %v", out, err)
	}
}

func TestAssistantCannotReachPeer(t *testing.T) {
	alpha, _ := clitest.TwoNodes(t)
	clitest.StartNode(t, alpha, clitest.MustLoad(t, alpha))
	for peer, want := range map[string]string{"nobody": `no node "nobody" found`, "alpha": "is this node itself"} {
		out, err := clitest.Run(t, alpha, "@"+peer+" hi\n", "chat")
		if err != nil || !strings.Contains(out, "last=error: ") || !strings.Contains(out, want) {
			t.Errorf("%s: %q, %v", peer, out, err)
		}
	}
}

// A peer's question may make the agent use the tools its owner allows for
// peers, and no others.
func TestPeerAgentUsesAllowedTools(t *testing.T) {
	// gamma keeps the default: its agent has no tools for peers.
	gamma := clitest.Settings(t)
	cfg := clitest.NodeConfig(gamma)
	cfg.Name, cfg.Model = "gamma", "o3-mini"
	clitest.StartNode(t, gamma, cfg)

	// beta lets peers use its tools.
	beta := clitest.Settings(t)
	cfg = clitest.NodeConfig(beta)
	cfg.Name, cfg.Model = "beta", "gpt-4o-mini"
	cfg.Tools = &config.Tools{A2A: config.Named("list_peers", "ask_peer")}
	clitest.StartNode(t, beta, cfg)

	alpha := clitest.Settings(t)
	clitest.StartNode(t, alpha, clitest.NodeConfig(alpha))

	clitest.Finds(alpha, beta)
	clitest.Finds(beta, alpha, gamma)
	clitest.Finds(gamma, beta)
	clitest.Trust(t, beta, alpha, "alpha")
	clitest.Trust(t, alpha, beta, "beta")
	clitest.Trust(t, gamma, beta, "beta")
	clitest.Trust(t, beta, gamma, "gamma")

	// alpha asks beta, whose agent asks gamma on the way.
	out, err := clitest.Run(t, alpha, "", "ask", "beta", "@gamma hello")
	if err != nil || out != "[gpt-4o-mini] turns=3 last=[o3-mini] turns=1 last=hello\n" {
		t.Fatalf("beta should ask gamma: %q, %v", out, err)
	}

	// The peer that asks is not asked in turn, and not offered either.
	out, err = clitest.Run(t, alpha, "", "ask", "beta", "@alpha hello")
	if err != nil || !strings.Contains(out, "last=error: alpha is the peer you are answering") {
		t.Fatalf("beta should not ask alpha back: %q, %v", out, err)
	}
	out, err = clitest.Run(t, alpha, "", "ask", "beta", "@")
	if err != nil || !strings.Contains(out, "gamma (id "+clitest.NodeID(t, gamma)) || strings.Contains(out, "alpha") {
		t.Fatalf("beta should list gamma only: %q, %v", out, err)
	}

	// gamma allows peers no tools: the same message is just a message.
	out, err = clitest.Run(t, beta, "", "ask", "gamma", "@beta hello")
	if err != nil || out != "[o3-mini] turns=1 last=@beta hello\n" {
		t.Fatalf("gamma should not use tools: %q, %v", out, err)
	}
}

// The agent of a peer asks back what it needs instead of sending a request
// of its own, which the two agents would wait on forever: the agent that
// asked gets the question as what its request is answered with. A note
// starts no agent at the peer: it is kept there, and that is all.
func TestPeerAsksBackAcrossNodes(t *testing.T) {
	beta := clitest.Settings(t)
	cfg := clitest.NodeConfig(beta)
	cfg.Name, cfg.Model = "beta", "gpt-4o-mini"
	cfg.Tools = &config.Tools{A2A: config.Named("ask_back")}
	clitest.StartNode(t, beta, cfg)
	alpha := clitest.Settings(t)
	clitest.StartNode(t, alpha, clitest.NodeConfig(alpha))
	clitest.Finds(alpha, beta)
	clitest.Finds(beta, alpha)
	clitest.Trust(t, beta, alpha, "alpha")
	clitest.Trust(t, alpha, beta, "beta")

	out, err := clitest.Run(t, alpha, "", "chat", `@beta !ask_back {"question":"which file do you mean?"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"last=beta has not answered yet. It asks back, and waits for your answer before it goes on (question ", "which file do you mean?", "Answer with answer_peer("} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	out, err = clitest.Run(t, alpha, "", "chat", `!tell_peer {"peer":"beta","message":"thanks for asking"}`)
	if err != nil || !strings.Contains(out, "last=beta was told. Nothing answers a note.") {
		t.Fatalf("a note: %q, %v", out, err)
	}
	// beta kept the note among its conversations with alpha, and no agent
	// answered it.
	var kept []string
	filepath.WalkDir(home.Dir(beta.Home).History(), func(path string, d fs.DirEntry, _ error) error {
		if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), "thanks for asking") {
			kept = append(kept, string(b))
		}
		return nil
	})
	if len(kept) != 1 || !strings.Contains(kept[0], `[A note from the peer, which asks for no answer:]\nthanks for asking`) || strings.Contains(kept[0], `"role":"assistant"`) {
		t.Fatalf("beta kept %q", kept)
	}
}
