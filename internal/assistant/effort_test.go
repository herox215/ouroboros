package assistant

import (
	"context"
	"errors"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/identity"
	"ouroboros/internal/openai"
)

// effortLLM keeps the effort of every call.
type effortLLM struct{ efforts []string }

func (l *effortLLM) Complete(ctx context.Context, _ string, _ []openai.Message, _ []openai.Tool) (openai.Message, error) {
	l.efforts = append(l.efforts, openai.EffortOf(ctx))
	return openai.Message{Content: "ok"}, nil
}

// The model answers the owner with the effort of the configuration, as it
// is when the message comes, and peers with its own.
func TestEffortIsTheOwners(t *testing.T) {
	llm := &effortLLM{}
	cfg := config.Config{Access: "a", Model: "m", Name: "me", PrivateKey: identity.Generate().Private(), Effort: "high"}
	read := func() (config.Config, error) { return cfg, nil }
	b := Builder{LLM: func(string) (agent.LLM, error) { return llm, nil }, Tools: func() []agent.Tool { return nil }}
	a := New(read, b, discard)
	ctx := context.Background()
	a.Ask(ctx, "chat", "hi", nil)
	cfg.Effort = ""
	a.Ask(ctx, "chat", "again", nil)
	cfg.Effort = "max"
	b.Peers(read, nil).Respond(ctx, "peer", "hi")
	if len(llm.efforts) != 3 || llm.efforts[0] != "high" || llm.efforts[1] != "" || llm.efforts[2] != "" {
		t.Fatalf("efforts %q", llm.efforts)
	}
}

// A session brings its own effort, which holds from the next call on, also
// when it is "": the configuration holds only where none is brought. Effort
// is that of the configuration.
func TestEffortOfASession(t *testing.T) {
	llm := &effortLLM{}
	cfg := config.Config{Access: "a", Model: "m", Name: "me", PrivateKey: identity.Generate().Private(), Effort: "high"}
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{LLM: func(string) (agent.LLM, error) { return llm, nil }, Tools: func() []agent.Tool { return nil }}, discard)
	effort := "low"
	ctx := openai.WithEffortOf(context.Background(), func() string { return effort })
	a.Ask(ctx, "chat", "hi", nil)
	effort = ""
	a.Ask(ctx, "chat", "again", nil)
	if len(llm.efforts) != 2 || llm.efforts[0] != "low" || llm.efforts[1] != "" {
		t.Fatalf("efforts %q", llm.efforts)
	}
	if a.Effort() != "high" {
		t.Fatalf("default %q", a.Effort())
	}
	broken := New(func() (config.Config, error) { return config.Config{Effort: "max"}, errors.New("unreadable") }, Builder{}, discard)
	if broken.Effort() != "" {
		t.Fatalf("an unreadable configuration: %q", broken.Effort())
	}
}

// Model is the model of the configuration as it is now, and "" if the
// configuration cannot be read.
func TestModel(t *testing.T) {
	cfg := config.Config{Access: "a", Model: "m", Name: "me", PrivateKey: identity.Generate().Private()}
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{}, discard)
	if got := a.Model(false); got != "m" {
		t.Fatalf("model %q", got)
	}
	cfg.Model = "other"
	if got := a.Model(false); got != "other" {
		t.Fatalf("the model as configured anew %q", got)
	}
	broken := New(func() (config.Config, error) { return config.Config{Model: "max"}, errors.New("unreadable") }, Builder{}, discard)
	if got := broken.Model(false); got != "" {
		t.Fatalf("an unreadable configuration: %q", got)
	}
}
