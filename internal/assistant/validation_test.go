package assistant

import (
	"context"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai/openaitest"
)

// Test the assembled prompts, including a browser-only agent that gets no
// file/bash workflow hint. Literal expectations do not disappear with the hint.
func TestFunctionValidationPrompts(t *testing.T) {
	cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me"}
	browser := agent.NewTool("browser", "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	b := Builder{LLM: overHTTP, Tools: func() []agent.Tool { return []agent.Tool{browser} }}
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	owner, err := a.Ask(context.Background(), "chat", "?system", nil)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := b.helper(cfg, []agent.Tool{browser})
	if err != nil {
		t.Fatal(err)
	}
	helperPrompt, _ := helper.Prompt()
	for name, prompt := range map[string]string{"owner": owner, "helper": helperPrompt} {
		for _, want := range []string{
			"distinguish reachability from verified functionality",
			"check its observable result before claiming it works",
			"perform an interaction (such as following a link or submitting a form) and read the resulting page or state",
			"merely opening a page proves only reachability",
			"do not perform consequential actions just to test",
			"Report what you actually tested, the observed result, and what remains unverified",
			"if interaction cannot be checked, say so rather than claiming full functionality",
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s prompt lacks %q", name, want)
			}
		}
	}
}
