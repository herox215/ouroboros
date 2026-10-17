package assistant

import (
	"context"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai/openaitest"
)

// Check assembled owner and helper prompts, not just shared hint constants.
func TestRecoveryAndResearchPrompts(t *testing.T) {
	for _, tool := range []string{"edit", ToolBash, "browser"} {
		t.Run(tool, func(t *testing.T) {
			cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me"}
			tools := []agent.Tool{agent.NewTool(tool, "", nil, func(context.Context, struct{}) (string, error) { return "", nil })}
			b := Builder{LLM: overHTTP, Tools: func() []agent.Tool { return tools }}
			owner, err := New(func() (config.Config, error) { return cfg, nil }, b, discard).Ask(context.Background(), "chat", "?system", nil)
			if err != nil {
				t.Fatal(err)
			}
			helper, err := b.helper(cfg, tools)
			if err != nil {
				t.Fatal(err)
			}
			helperPrompt, _ := helper.Prompt()
			for name, prompt := range map[string]string{"owner": owner, "helper": helperPrompt} {
				for _, want := range []string{
					"For static documentation, use an available HTTP mechanism first (for example curl through bash when available), with bounded output.",
					"Use a browser only when JavaScript or interaction is needed, not merely to read static sources.",
					"Inspect the available HTTP mechanisms before deciding that a new capability is needed; if none is available, report the limitation rather than assuming a tool exists.",
					"For short comparison questions, stop researching once a few reliable primary sources support the central claims and the requested comparison can be answered.",
					"Do not pursue further repository or implementation details unless they resolve a specific remaining uncertainty or the user asks for that depth.",
					"For feasibility checks, request a few search results and small, bounded webpage excerpts rather than collecting large raw responses.",
					"Use the tools' output budget parameters and browser offsets to read more only where needed.",
					"Start with about 2 KiB of text per source, leaving room for notes within the output budget, and expand only when necessary.",
					"Keep the combined output of tool calls, including notes and other results, safely below its output limit; individual tool limits do not bound a batch.",
				} {
					if !strings.Contains(prompt, want) {
						t.Errorf("%s prompt lacks %q", name, want)
					}
				}
				for _, want := range []string{
					"For commit-only requests, default to status, diff and secret checks plus appropriate short tests.",
					"Do not automatically start a comprehensive review or delegate review tasks; expand the scope only if the user asks or a specific finding requires it.",
					"When recovering a damaged repository file, first use git log and git show to find and recover the complete known-good version verbatim, rather than piecing it together from fragments.",
					"If Git has no complete version, check the conversation archive for the original; if neither source has it, state the gap before reconstructing.",
					"Preserve unrelated local changes, verify the recovered file is complete against the original source, and run the relevant tests; passing tests alone does not prove completeness.",
				} {
					if got, expected := strings.Contains(prompt, want), tool != "browser"; got != expected {
						t.Errorf("%s recovery rule %q present = %v, want %v", name, want, got, expected)
					}
				}
			}
		})
	}
}
