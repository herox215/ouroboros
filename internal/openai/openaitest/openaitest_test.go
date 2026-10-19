package openaitest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"ouroboros/internal/openai"
)

func TestWithoutOriginContext(t *testing.T) {
	for _, source := range []string{"cli", "tui", "gui", "api", "unknown"} {
		for _, text := range []string{"hello", "?system", "@peer hello", "@", "#error-id", "!get_config", `!propose_config {"path":"model"}`} {
			t.Run(source+"/"+text, func(t *testing.T) {
				plain := openai.Message{Role: "user", Content: text}
				attributed := plain
				attributed.Source = openai.MessageSource{Interface: source}
				wire := openai.MessagesForModel([]openai.Message{attributed})[0]
				original := wire
				if got := withoutOriginContext(wire); got.Content != text {
					t.Fatalf("user text: %q, want %q", got.Content, text)
				}
				if wire.Content != original.Content {
					t.Fatal("changed the attributed request")
				}
				if got := withoutOriginContext(plain); got.Content != text {
					t.Fatalf("plain text: %q, want %q", got.Content, text)
				}
				wire.Role = "tool"
				if got := withoutOriginContext(wire); got.Content != wire.Content {
					t.Fatal("stripped origin-like text from a tool result")
				}
			})
		}
	}
}

// Commands call only directly advertised tools, not names in descriptions.
func TestCommandsRequireDirectTools(t *testing.T) {
	url := NewServerWithKey(t, "")
	for _, tt := range []struct {
		name, command, offered, description, want string
	}{
		{"direct", `!read {"path":"a.txt"}`, "read", "", "read"},
		{"default arguments", "!list", "list", "", "list"},
		{"peer", "@beta hello", "ask_peer", "", "ask_peer"},
		{"peers", "@", "list_peers", "", "list_peers"},
		{"error", "#id", "explain_error", "", "explain_error"},
		{"unlisted", "!read", "list", "", ""},
		{"description is not an offer", "!read", "run", "\n- read(path)\n- tools.read(path)", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"model":    "gpt-4o",
				"messages": []openai.Message{{Role: "system", Content: "test"}, {Role: "user", Content: tt.command}},
				"tools":    []openai.Tool{{Type: "function", Function: openai.Function{Name: tt.offered, Description: tt.description}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.Post(url+"/chat/completions", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var result struct {
				Choices []struct{ Message openai.Message } `json:"choices"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK || len(result.Choices) != 1 {
				t.Fatalf("response: %d, %+v", resp.StatusCode, result)
			}
			msg := result.Choices[0].Message
			if tt.want == "" {
				if len(msg.ToolCalls) != 0 || msg.Content != "[gpt-4o] turns=1 last="+tt.command {
					t.Fatalf("unoffered tool was not echoed: %+v", msg)
				}
				return
			}
			wantCall, _ := toolCall(openai.Message{Role: "user", Content: tt.command})
			if len(msg.ToolCalls) != 1 || msg.ToolCalls[0] != wantCall || msg.ToolCalls[0].Function.Name != tt.want {
				t.Fatalf("tool call: %+v, want %+v", msg, wantCall)
			}
		})
	}
}
