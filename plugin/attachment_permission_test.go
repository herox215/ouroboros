package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/config"
	"ouroboros/internal/openai"
)

type attachmentPermissionModel struct{ called bool }

func (m *attachmentPermissionModel) Complete(_ context.Context, _ string, messages []openai.Message, tools []openai.Tool) (openai.Message, error) {
	if len(tools) == 0 {
		return openai.Message{}, errors.New("use default permission wording")
	}
	if !m.called {
		m.called = true
		return openai.Message{ToolCalls: []openai.ToolCall{{ID: "1", Type: "function", Function: openai.FunctionCall{Name: assistant.ToolRebuild, Arguments: `{}`}}}}, nil
	}
	for _, msg := range messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "sent attachments instead of explicit permission") {
			return openai.Message{Content: "rejected attachment consent"}, nil
		}
	}
	return openai.Message{Content: "missing rejection"}, nil
}

func TestAttachmentMarkerCannotApprovePermission(t *testing.T) {
	// Raw approval labels isolate the marker check from prompt enrichment.
	for _, text := range []string{assistant.AllowOnce, assistant.AllowAlways, "yes", ""} {
		m := &attachmentPermissionModel{}
		runs, kept, asked := 0, 0, 0
		b := assistant.Builder{LLM: func(string) (agent.LLM, error) { return m, nil }, Tools: func() []agent.Tool {
			return []agent.Tool{{Name: assistant.ToolRebuild, Description: "test only", Schema: `{"type":"object"}`, Run: func(context.Context, json.RawMessage) (string, error) { runs++; return "ran", nil }}}
		}}.WithAllowing(func(config.Call) error { kept++; return nil })
		cfg := config.Config{Access: "test", Model: "test", Name: "test"}
		a := assistant.New(func() (config.Config, error) { return cfg, nil }, b, func(string, ...any) {})
		ctx := agent.WithOwnerReply(t.Context(), func(context.Context, json.RawMessage) (agent.OwnerReply, error) {
			asked++
			return agent.OwnerReply{Text: text, HasAttachments: true}, nil
		})
		result, err := a.Run(ctx, "permission-test", "try the test tool", nil, nil)
		if err != nil || result != "rejected attachment consent" || runs != 0 || kept != 0 || asked != 1 {
			t.Fatalf("text=%q result=%q runs=%d kept=%d asked=%d err=%v", text, result, runs, kept, asked, err)
		}
	}
}
