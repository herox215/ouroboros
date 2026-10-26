package history

import (
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/openai"
)

// FirstUser is the first message of the user in a conversation: what a
// session that goes on by itself is reminded of, and "" without one.
func TestFirstUser(t *testing.T) {
	a := at(t)
	if _, err := a.FirstUser("owner/none"); !errors.Is(err, ErrNotArchived) {
		t.Errorf("a chat without an answer: %v", err)
	}
	if err := a.Append(owner, []openai.Message{
		{Role: "user", Content: "the first task"},
		{Role: "assistant", Content: "done"},
		{Role: "user", Content: "a later one"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := a.FirstUser("owner/chat-1"); err != nil || got != "the first task" {
		t.Errorf("first user: %q, %v", got, err)
	}
	// An answer without a message of the user has none.
	if err := a.Append(Who{Session: "tools"}, []openai.Message{{Role: "assistant", Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := a.FirstUser("owner/tools"); err != nil || got != "" {
		t.Errorf("without a user: %q, %v", got, err)
	}
	if _, err := a.FirstUser("nonsense"); err == nil || !strings.Contains(err.Error(), "names no session") {
		t.Errorf("bad id: %v", err)
	}
}
