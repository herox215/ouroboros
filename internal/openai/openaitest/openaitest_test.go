package openaitest

import (
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
