package history

import (
	"errors"
	"testing"

	"ouroboros/internal/openai"
)

func TestToolResult(t *testing.T) {
	a := Open(t.TempDir())
	msgs := []openai.Message{
		{Role: "assistant", ToolCalls: []openai.ToolCall{
			{ID: "other", Function: openai.FunctionCall{Name: "read", Arguments: "{}"}},
			{ID: "target", Function: openai.FunctionCall{Name: "run", Arguments: "not JSON"}},
			{ID: "pending", Function: openai.FunctionCall{Name: "read", Arguments: "{}"}},
		}},
		{Role: "tool", ToolCallID: "other", Content: "wrong"},
		{Role: "tool", ToolCallID: "target", Content: ""},
		{Role: "tool", ToolCallID: "target", Content: "must stop before this"},
	}
	if err := a.Append(Who{Session: "s"}, msgs, nil); err != nil {
		t.Fatal(err)
	}
	c, content, found, err := a.ToolResult("owner/s", "target")
	if err != nil || c.Arguments != "not JSON" || c.Name != "run" || content != "" || !found {
		t.Fatalf("result: %+v %q %v %v", c, content, found, err)
	}
	c, _, found, err = a.ToolResult("owner/s", "pending")
	if err != nil || c.ID != "pending" || found {
		t.Fatalf("pending: %+v %v %v", c, found, err)
	}
	for _, id := range []string{"", "nested", "absent"} {
		if _, _, _, err := a.ToolResult("owner/s", id); !errors.Is(err, ErrNoCall) {
			t.Fatalf("call %q: %v", id, err)
		}
	}
	if _, _, _, err := a.ToolResult("owner/missing", "target"); !errors.Is(err, ErrNotArchived) {
		t.Fatal(err)
	}
	for _, id := range []string{"../s", "owner/../s", "owner/s/other", "peer-invalid/s"} {
		if _, _, _, err := a.ToolResult(id, "target"); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
}
