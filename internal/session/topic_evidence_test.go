package session

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/openai"
)

func TestTopicSendRejectsConcurrentThreadChange(t *testing.T) {
	s, a, se, question := topicFixture(t)
	a.checkTopic = func(context.Context, assistant.TopicCheck) (assistant.TopicDecision, error) {
		s.mu.Lock()
		s.emit(s.get(ThreadID), Event{Kind: Message, Text: "No, I meant the other issue"})
		s.mu.Unlock()
		return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "stale thread context"}, nil
	}
	before := topicSnapshot(s, se)
	if _, _, err := (threading{s}).Send(context.Background(), se.id, "do it"); err == nil {
		t.Fatal("accepted stale thread evidence")
	}
	topicUnchanged(t, s, se, question, before)
}

func TestTopicPendingQuestionOutlivesRosterWindow(t *testing.T) {
	s, a, se, question := topicFixture(t)
	s.mu.Lock()
	for i := range se.events {
		se.events[i].At = s.now().Add(-48 * time.Hour)
	}
	thread := s.get(ThreadID)
	thread.questions = []*relayed{{se: se, question: json.RawMessage(`{"question":"Add the regression test?"}`)}}
	s.mu.Unlock()
	a.checkTopic = func(context.Context, assistant.TopicCheck) (assistant.TopicDecision, error) {
		return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "answer to the still pending question"}, nil
	}
	answered, err := s.replyToConcern(context.Background(), queued{text: "yes"})
	if err != nil || !answered {
		t.Fatalf("old pending question became unreachable: %t %v", answered, err)
	}
	if got := <-question; got.text != "yes" {
		t.Fatalf("answer lost: %+v", got)
	}
}

func TestTopicImageOnlyAnchorAndProposedImages(t *testing.T) {
	s, a, _, _ := topicFixture(t)
	original := []openai.Image{{Type: "image/png", Data: []byte("original fixture")}}
	proposed := []openai.Image{{Type: "image/png", Data: []byte("proposed fixture")}}
	s.mu.Lock()
	se := s.get("image-concern")
	s.taken(se, []queued{{images: original}})
	// Later input must not replace the original image anchor.
	s.taken(se, []queued{{text: "a later follow-up", images: proposed}})
	question := make(chan queued, 1)
	se.asked = question
	s.get(ThreadID).pictures = proposed
	s.mu.Unlock()
	a.checkTopic = func(_ context.Context, check assistant.TopicCheck) (assistant.TopicDecision, error) {
		if check.Anchor != "" || !reflect.DeepEqual(check.AnchorImages, original) || !reflect.DeepEqual(check.Images, proposed) {
			t.Errorf("wrong visual evidence: %+v", check)
		}
		return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "same pictured concern"}, nil
	}
	if _, _, err := (threading{s}).Send(context.Background(), se.id, "and this detail?"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-question:
		if !reflect.DeepEqual(got.images, proposed) {
			t.Fatalf("images not delivered: %+v", got)
		}
	default:
		t.Fatal("image-only anchor prevented continued concern")
	}
}

// A session that waits for an answer tells the check its question: a short
// answer to it belongs to the session. One that waits for nothing tells none.
func TestTopicSendTellsPendingQuestion(t *testing.T) {
	for _, waiting := range []bool{true, false} {
		s, a, se, _ := topicFixture(t)
		s.mu.Lock()
		s.emit(se, Event{Kind: Asked, Args: json.RawMessage(`{"question":"Run the tests and install?"}`)})
		if !waiting {
			se.asked = nil
		}
		s.mu.Unlock()
		var pending string
		a.checkTopic = func(_ context.Context, check assistant.TopicCheck) (assistant.TopicDecision, error) {
			pending = check.Pending
			return assistant.TopicDecision{Disposition: assistant.TopicUnclear, Reason: "recorded"}, nil
		}
		(threading{s}).Send(context.Background(), se.id, "Ja")
		if want := map[bool]string{true: "Run the tests and install?"}[waiting]; pending != want {
			t.Errorf("waiting %t: pending %q, not %q", waiting, pending, want)
		}
	}
}
