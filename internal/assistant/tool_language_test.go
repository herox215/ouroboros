package assistant

import (
	"context"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai/openaitest"
)

func TestToolDescriptionLanguagePrompts(t *testing.T) {
	cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me"}
	edit := agent.NewTool("edit", "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	b := Builder{LLM: overHTTP, Tools: func() []agent.Tool { return []agent.Tool{edit} }}
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	owner, err := a.Ask(context.Background(), "chat", "?system", nil)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := b.helper(cfg, []agent.Tool{edit})
	if err != nil {
		t.Fatal(err)
	}
	helperPrompt, _ := helper.Prompt()
	// Use a literal expectation so changing the hint cannot change the test too.
	const want = "When writing or proposing tool descriptions, including parameter descriptions, write them in English regardless of the conversation language."
	for name, prompt := range map[string]string{"owner": owner, "helper": helperPrompt} {
		if !strings.Contains(prompt, want) {
			t.Errorf("%s prompt lacks %q", name, want)
		}
	}
	// The exception for tool descriptions must not replace the reply-language rule.
	if !strings.Contains(owner, "Answer in the language of the user.") {
		t.Error("owner prompt lacks the reply-language rule")
	}
}
