package session

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"ouroboros/internal/assistant"
	"ouroboros/internal/openai"
)

func TestTopicSendRejectsConcurrentThreadChange(t *testing.T) {
	s, a, se, question := topicFixture(t)
	checks := 0
	a.checkTopic = func(context.Context, assistant.TopicCheck) (assistant.TopicDecision, error) {
		checks++
		s.mu.Lock()
		s.emit(s.get(ThreadID), Event{Kind: Message, Text: "No, I meant the other issue"})
		s.mu.Unlock()
		return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "stale thread context"}, nil
	}
	before := topicSnapshot(s, se)
	if _, _, err := (threading{s}).Send(context.Background(), se.id, "do it", "", false); err == nil {
		t.Fatal("accepted stale thread evidence")
	}
	if checks != 3 {
		t.Fatalf("unbounded or missing fresh verification: %d checks", checks)
	}
	topicUnchanged(t, s, se, question, before)
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
	if _, _, err := (threading{s}).Send(context.Background(), se.id, "and this detail?", "", false); err != nil {
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
		(threading{s}).Send(context.Background(), se.id, "Ja", "", false)
		if want := map[bool]string{true: "Run the tests and install?"}[waiting]; pending != want {
			t.Errorf("waiting %t: pending %q, not %q", waiting, pending, want)
		}
	}
}
