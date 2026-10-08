package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
)

// titleLLM answers as reply says, and keeps what it was sent.
type titleLLM struct {
	reply string
	err   error
	model string
	sent  []openai.Message
	tools []openai.Tool
	calls int
}

func (l *titleLLM) Complete(_ context.Context, model string, msgs []openai.Message, tools []openai.Tool) (openai.Message, error) {
	l.calls++
	l.model, l.sent, l.tools = model, msgs, tools
	return openai.Message{Role: "assistant", Content: l.reply}, l.err
}

// titled is an assistant on llm.
func titled(cfg config.Config, llm agent.LLM) *Assistant {
	return New(func() (config.Config, error) { return cfg, nil }, Builder{
		LLM:   func(string) (agent.LLM, error) { return llm, nil },
		Tools: func() []agent.Tool { return nil },
	}, discard)
}

// The LLM of the agent names a conversation from what the owner and the agent
// said in it: not the calls of tools, and of a long conversation its start.
func TestTitle(t *testing.T) {
	cfg := config.Config{Access: "http://x", Model: "m", Name: "n"}
	llm := &titleLLM{reply: "  „Den Garten planen.“\nand more"}
	long := strings.Repeat("ä", titleMessage)
	a := titled(cfg, llm)
	conversation := []openai.Message{
		{Role: "user", Content: "Wie plane ich den Garten?"},
		{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "1", Function: openai.FunctionCall{Name: "run"}}}},
		{Role: "tool", ToolCallID: "1", Content: "secret of a tool"},
		{Role: "assistant", Content: long},
		{Role: "user", Content: long},
		{Role: "user", Content: long},
		// The conversation sent ends with this one at exactly titleTalk.
		{Role: "user", Content: strings.Repeat("x", 1921)},
		{Role: "user", Content: "too late"},
	}
	got, err := a.Title(context.Background(), conversation)
	if err != nil || got != "Den Garten planen" {
		t.Fatalf("title %q, %v", got, err)
	}
	if llm.model != "m" || llm.tools != nil || len(llm.sent) != 2 || llm.sent[0].Role != "system" || llm.sent[0].Content != titlePrompt || llm.sent[1].Role != "user" {
		t.Fatalf("sent %+v to %q with %v", llm.sent, llm.model, llm.tools)
	}
	cut := strings.Repeat("ä", titleMessage/2) + "…"
	if want := "user: Wie plane ich den Garten?\n\nassistant: " + cut + strings.Repeat("\n\nuser: "+cut, 2) + "\n\nuser: " + strings.Repeat("x", 1921) + "\n\n"; llm.sent[1].Content != want || len(want) != titleTalk {
		t.Fatalf("talk %q", llm.sent[1].Content)
	}
	// A message of titleMessage bytes is whole.
	whole := strings.Repeat("x", titleMessage)
	a.Title(context.Background(), []openai.Message{{Role: "user", Content: whole}})
	if llm.sent[1].Content != "user: "+whole+"\n\n" {
		t.Fatalf("whole message %q", llm.sent[1].Content)
	}

	// While no topic can be told, there is no title.
	llm.reply = "none."
	if got, err := a.Title(context.Background(), conversation); err != nil || got != "" {
		t.Errorf("no topic: %q, %v", got, err)
	}
	// Without anything said, the LLM is not asked.
	llm.calls = 0
	if got, err := a.Title(context.Background(), conversation[1:3]); err != nil || got != "" || llm.calls != 0 {
		t.Errorf("nothing said: %q, %v, %d calls", got, err, llm.calls)
	}
	// What fails is told.
	llm.err = errors.New("down")
	if _, err := a.Title(context.Background(), conversation); err == nil || err.Error() != "down" {
		t.Errorf("failed call: %v", err)
	}
	hi := conversation[:1]
	// The agent was built, but its LLM is no more.
	built := 0
	gone := New(func() (config.Config, error) { return cfg, nil }, Builder{
		LLM: func(string) (agent.LLM, error) {
			if built++; built > 1 {
				return nil, errors.New("no access")
			}
			return llm, nil
		},
		Tools: func() []agent.Tool { return nil },
	}, discard)
	if _, err := gone.Title(context.Background(), hi); err == nil || err.Error() != "no access" {
		t.Errorf("no LLM: %v", err)
	}
	if _, err := titled(config.Config{}, llm).Title(context.Background(), hi); !errors.Is(err, config.ErrIncomplete) {
		t.Errorf("not set up: %v", err)
	}
}

// The fake API of the tests knows a request to name a conversation, and
// names none.
func TestTitleOfTheFakeAPI(t *testing.T) {
	if !strings.HasPrefix(titlePrompt, openaitest.TitlePrompt) || untold != openaitest.Untitled {
		t.Fatal("the fake API does not know the request to name a conversation")
	}
	cfg := config.Config{Access: openaitest.NewServer(t), Model: "m", Name: "n"}
	llm, _ := overHTTP(cfg.Access)
	if got, err := titled(cfg, llm).Title(context.Background(), []openai.Message{{Role: "user", Content: "plan my garden"}}); err != nil || got != "" {
		t.Fatalf("title %q, %v", got, err)
	}
}

// The title is the first line of the reply, without what goes around it.
func TestTopic(t *testing.T) {
	for reply, want := range map[string]string{
		"Garden plans":           "Garden plans",
		"\"Garden plans.\"":      "Garden plans",
		"**Garden plans**\nmore": "Garden plans",
		"  `NONE`  ":             "",
		"":                       "",
		"« Plan »":               "Plan",
	} {
		if got := topic(reply); got != want {
			t.Errorf("topic(%q) = %q, want %q", reply, got, want)
		}
	}
}
