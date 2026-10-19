package assistant

import (
	"context"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai/openaitest"
)

// Automatic permission questions do not require the user_question tool.
func TestUnansweredApprovalPrompts(t *testing.T) {
	for _, name := range []string{"browser", "edit", ToolUserQuestion} {
		t.Run(name, func(t *testing.T) {
			cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me"}
			tool := agent.NewTool(name, "", nil, func(context.Context, struct{}) (string, error) { return "", nil })
			b := Builder{LLM: overHTTP, Tools: func() []agent.Tool { return []agent.Tool{tool} }}
			prompt, err := New(func() (config.Config, error) { return cfg, nil }, b, discard).Ask(context.Background(), "chat", "?system", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				"If a tool reports that an approval request timed out or the owner did not answer, report that authorization is still pending and the operation did not run.",
				"Do not repeat the tool call or launch an additional question for that authorization; wait for the user's explicit decision before trying again.",
			} {
				if !strings.Contains(prompt, want) {
					t.Errorf("prompt lacks %q", want)
				}
			}
		})
	}
}
