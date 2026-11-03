package session

import (
	"context"
	"strings"
	"testing"

	"ouroboros/internal/assistant"
)

func TestTopicSendIgnoresStreamingProgress(t *testing.T) {
	s, a, se, question := topicFixture(t)
	a.checkTopic = func(context.Context, assistant.TopicCheck) (assistant.TopicDecision, error) {
		s.mu.Lock()
		s.emit(se, Event{Kind: Thought, Text: "still thinking"})
		s.emit(se, Event{Kind: Step, Tool: "read"})
		s.emit(se, Event{Kind: Answering, Text: "streaming"})
		s.mu.Unlock()
		return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "same concern"}, nil
	}
	_, result, err := (threading{s}).Send(context.Background(), se.id, "add tests", "", false)
	if err != nil || !strings.Contains(result, "CheckTopic: continue (same concern)") {
		t.Fatalf("progress invalidated continuation or missing audit reason: %q %v", result, err)
	}
	select {
	case got := <-question:
		if got.text != "add tests" {
			t.Fatalf("wrong message: %+v", got)
		}
	default:
		t.Fatal("no delivery after streaming progress")
	}
}
