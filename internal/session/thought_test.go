package session

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/assistant"
	"ouroboros/internal/openai"
)

// What the model thinks is told in pieces of thoughtPiece as the stream of
// the LLM brings it, and what is left of a piece before the answer ends.
func TestThoughtsComeBeforeTheAnswer(t *testing.T) {
	thinking := strings.Repeat("Let me see. ", 45) // more than a piece
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < len(thinking); i += 50 {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":%q}}]}\n\n", thinking[i:min(i+50, len(thinking))])
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1}}\n\n")
	}))
	defer llm.Close()
	client := openai.New(llm.URL, "key")
	s := service(t, &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		msg, err := client.Complete(ctx, "model", []openai.Message{{Role: "user", Content: text}}, nil)
		return msg.Content, err
	}})
	ctx := context.Background()
	_, live, err := s.Watch(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ask(ctx, "s1", "hi"); err != nil {
		t.Fatal(err)
	}
	var thoughts []string
	for _, e := range until(t, live, Answer, Failed) {
		if e.Kind == Thought {
			thoughts = append(thoughts, e.Text)
		}
		if e.Kind == Failed {
			t.Fatalf("failed: %s", e.Text)
		}
	}
	if want := []string{thinking[:thoughtPiece], thinking[thoughtPiece:]}; !slices.Equal(thoughts, want) {
		t.Fatalf("thoughts %q, want %q", thoughts, want)
	}
}
