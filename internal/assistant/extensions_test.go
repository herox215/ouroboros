package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
	"ouroboros/internal/workdir"
)

func TestExtensionsSharedOwnerPeerFlow(t *testing.T) {
	cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "m", Name: "node", ContextProvider: "p"}
	var conversations []agent.Conversation
	var logs []string
	log := func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) }
	provider := agent.ContextProvider{Name: "p", Prepare: func(_ context.Context, p agent.Preparation) ([]openai.Message, error) {
		conversations = append(conversations, p.Conversation)
		return append(append([]openai.Message{p.Messages[0]}, p.Context...), p.Messages[1:]...), nil
	}}
	b := Builder{LLM: overHTTP, Tools: func() []agent.Tool { return nil }, ContextProviders: func() []agent.ContextProvider { return []agent.ContextProvider{provider} }, AnswerHooks: func() []agent.AnswerHook {
		return []agent.AnswerHook{{Name: "post", Before: func(context.Context, agent.Preparation) ([]openai.Message, error) {
			return []openai.Message{{Role: "system", Content: "hook context"}}, nil
		}, After: func(context.Context, agent.AnswerOutcome) error { return errors.New("post failed") }}}
	}}
	owner := New(func() (config.Config, error) { return cfg, nil }, b, log)
	if _, ok := owner.Inspection("id"); ok {
		t.Fatal("unexpected snapshot")
	}
	contents, err := owner.Contents("id")
	if err != nil || len(contents.Messages) != 0 {
		t.Fatal(contents, err)
	}
	reply, err := owner.Ask(context.Background(), "id", "owner", nil)
	if err != nil || !strings.Contains(reply, "last=owner") {
		t.Fatal(reply, err)
	}
	snapshot, ok := owner.Inspection("id")
	if !ok || len(snapshot.Messages) != 3 || len(snapshot.Diagnostics) != 1 {
		t.Fatal(snapshot)
	}
	contents, err = owner.Contents("id")
	if err != nil || contents.System != "" || contents.Messages[1].Content != "hook context" {
		t.Fatal(contents, err)
	}
	// The system prompt is the one of the project the conversation is in now.
	contents, err = owner.ContentsContext(workdir.With(context.Background(), "/project"), "id")
	if err != nil || contents.Messages[0].Role != "system" || !strings.Contains(contents.Messages[0].Content, `"/project"`) {
		t.Fatal(contents, err)
	}
	if owner.Context("id") == 0 {
		t.Fatal("missing actual size")
	}
	if !strings.Contains(strings.Join(logs, "\n"), "plugin post after failed") {
		t.Fatal("owner diagnostic not logged", logs)
	}
	logs = nil
	peers := b.Peers(func() (config.Config, error) { return cfg, nil }, log)
	reply, err = peers.Respond(a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"}), "id", "peer")
	if err != nil || !strings.Contains(reply, "turns=2 last=peer") {
		t.Fatal(reply, err)
	}
	if len(conversations) != 2 || conversations[0].Peer || !conversations[1].Peer || conversations[1].Origin.PublicKey != "peer" {
		t.Fatal(conversations)
	}
	if len(logs) == 0 || !strings.Contains(strings.Join(logs, "\n"), "plugin post after failed") {
		t.Fatal(logs)
	}
	// Changing selection must rebuild both agents, and invalid selections cannot
	// silently keep peers using their previous provider.
	cfg.ContextProvider = "missing"
	if _, err = owner.Ask(context.Background(), "id", "next", nil); !errors.Is(err, agent.ErrContextProvider) {
		t.Fatal(err)
	}
	if _, err = peers.Respond(context.Background(), "id", "next"); !errors.Is(err, agent.ErrContextProvider) {
		t.Fatal(err)
	}
	cfg.ContextProvider = ""
	if _, err = owner.Ask(context.Background(), "id", "next", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = peers.Respond(context.Background(), "id", "next"); err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 2 {
		t.Fatal("inactive provider invoked")
	}
}

func TestContentsPreparationFailure(t *testing.T) {
	cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "m", Name: "node", ContextProvider: "p"}
	boom := errors.New("prepare failed")
	b := Builder{LLM: overHTTP, Tools: func() []agent.Tool { return nil }, ContextProviders: func() []agent.ContextProvider {
		return []agent.ContextProvider{{Name: "p", Prepare: func(context.Context, agent.Preparation) ([]openai.Message, error) { return nil, boom }}}
	}}
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	if _, err := a.Ask(context.Background(), "id", "hi", nil); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	c, err := a.Contents("id")
	if !errors.Is(err, boom) || len(c.Messages) != 0 || a.Context("id") != 0 {
		t.Fatal(c, err)
	}
}
