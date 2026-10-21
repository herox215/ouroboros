package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ouroboros/internal/assistant"
)

func TestTopicReplyDoesNotConsumeUnrelatedQuestion(t *testing.T) {
	for _, disposition := range []string{assistant.TopicContinue, assistant.TopicNew, assistant.TopicUnclear} {
		t.Run(disposition, func(t *testing.T) {
			s, a, se, question := topicFixture(t)
			s.mu.Lock()
			thread := s.get(ThreadID)
			thread.questions = []*relayed{{se: se, question: json.RawMessage(`{"question":"Add the redirect regression test?"}`)}}
			s.mu.Unlock()
			a.checkTopic = func(_ context.Context, check assistant.TopicCheck) (assistant.TopicDecision, error) {
				if check.Message != "Change the billing export" {
					t.Errorf("wrong reply: %q", check.Message)
				}
				return assistant.TopicDecision{Disposition: disposition, Reason: "fixture"}, nil
			}
			before := topicSnapshot(s, se)
			answered, err := s.replyToConcern(context.Background(), queued{text: "Change the billing export"})
			if err != nil || answered != (disposition == assistant.TopicContinue) {
				t.Fatalf("answered=%t err=%v", answered, err)
			}
			if disposition != assistant.TopicContinue {
				topicUnchanged(t, s, se, question, before)
			} else if got := <-question; got.text != "Change the billing export" {
				t.Fatalf("reply lost: %+v", got)
			}
		})
	}
}

func TestTopicReplyTopicChangeReachesThread(t *testing.T) {
	s, a, se, question := topicFixture(t)
	s.mu.Lock()
	thread := s.get(ThreadID)
	thread.questions = []*relayed{{se: se, question: json.RawMessage(`{"question":"Add the redirect regression test?"}`)}}
	// Keep the thread busy so the new input can be observed in its queue
	// without starting a fake answer or consuming the pending target question.
	thread.cancel = func() {}
	s.mu.Unlock()
	a.checkTopic = func(context.Context, assistant.TopicCheck) (assistant.TopicDecision, error) {
		return assistant.TopicDecision{Disposition: assistant.TopicNew, Reason: "billing is separate"}, nil
	}
	before := topicSnapshot(s, se)
	if err := s.Steer(context.Background(), ThreadID, "Change the billing export"); err != nil {
		t.Fatal(err)
	}
	topicUnchanged(t, s, se, question, before)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(thread.queue) != 1 || thread.queue[0].text != "Change the billing export" {
		t.Fatalf("new concern did not reach router: %+v", thread.queue)
	}
	thread.cancel = nil
}

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
	_, result, err := (threading{s}).Send(context.Background(), se.id, "add tests")
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
