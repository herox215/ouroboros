package history

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

// The tools run within a conversation only, and a peer is known by its key.
func TestWho(t *testing.T) {
	var logs []string
	h := &archive{history.Open(filepath.Join(t.TempDir(), "history")), func(format string, args ...any) { logs = append(logs, format) }}
	for _, tool := range h.tools() {
		if _, err := tool.Run(context.Background(), json.RawMessage(`{"query":"x"}`)); err == nil || !strings.Contains(err.Error(), "only reachable within a conversation") {
			t.Errorf("%s outside a conversation: %v", tool.Name, err)
		}
	}
	if len(logs) != 0 {
		t.Errorf("logged: %v", logs)
	}
}

// llm asks for the tool "who" once, and then answers with its result.
type llm struct{}

func (llm) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	if last := msgs[len(msgs)-1]; last.Role == "tool" {
		return openai.Message{Content: last.Content}, nil
	}
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: "1", Function: openai.FunctionCall{Name: "who", Arguments: "{}"}}}}, nil
}

// who names the conversation a tool runs for, and refuses a peer without a
// key: it would be taken for the owner.
func TestWhoWithinAConversation(t *testing.T) {
	tool := plugin.NewTool("who", "", nil, func(ctx context.Context, _ struct{}) (string, error) {
		w, err := who(ctx)
		return w.Session + "/" + w.Peer, err
	})
	bot := agent.New(llm{}, "m", "s", tool)
	if got, _ := bot.Respond(context.Background(), "c1", "hi"); got != "c1/" {
		t.Errorf("owner: %q", got)
	}
	if got, _ := bot.Respond(a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "pk"}), "c2", "hi"); got != "c2/pk" {
		t.Errorf("peer: %q", got)
	}
	if got, _ := bot.Respond(a2a.WithOrigin(context.Background(), a2a.Origin{}), "c3", "hi"); got != "error: "+history.ErrScope.Error() {
		t.Errorf("peer without key: %q", got)
	}
}
