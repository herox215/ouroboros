package matrix

import (
	"context"
	"ouroboros/internal/openai"
	"strings"
	"testing"
)

type avatarAttachmentModel struct{ called, verified bool }

func (m *avatarAttachmentModel) Complete(_ context.Context, _ string, messages []openai.Message, _ []openai.Tool) (openai.Message, error) {
	if !m.called {
		m.called = true
		return openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "avatar", Type: "function", Function: openai.FunctionCall{Name: setAvatar, Arguments: "{}"}}}}, nil
	}
	m.verified = strings.Contains(messages[len(messages)-1].Content, `"verified":true`)
	return openai.Message{Role: "assistant", Content: "done"}, nil
}

func TestAvatarMissingAttachment(t *testing.T) {
	for _, args := range []avatarArgs{{}, {Attachment: 2}, {Attachment: -1}} {
		if _, err := avatarData(context.Background(), args, nil); err == nil {
			t.Fatal("missing attachment accepted")
		}
	}
	tool := avatarTool(store{}, nil)
	if _, err := tool.Run(context.Background(), []byte(`{"path":"a.png","attachment":1}`)); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("ambiguous input: %v", err)
	}
}
