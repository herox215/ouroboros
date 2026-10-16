package session

import (
	"context"
	"encoding/json"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/openai"
)

func TestQuestionReplyKeepsItsOwnSource(t *testing.T) {
	a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		reply, err := agent.AskOwnerReply(ctx, json.RawMessage(`{"question":"What should change?"}`))
		if reply.Text != "here" || reply.Source.Interface != "tui" {
			t.Errorf("reply: %+v", reply)
		}
		return reply.Text, err
	}}
	s := service(t, a)
	_, live, _ := s.Watch(t.Context(), "s1")
	if err := s.Ask(WithMessageSource(t.Context(), MessageSource{Interface: "gui"}), "s1", "go"); err != nil {
		t.Fatal(err)
	}
	until(t, live, Asked)
	if err := s.Steer(WithMessageSource(t.Context(), MessageSource{Interface: "tui"}), "s1", "here"); err != nil {
		t.Fatal(err)
	}
	events := until(t, live, Answer)
	if len(events) != 2 || events[0].Kind != Asked || !events[0].Done || events[0].Text != "here" || events[0].Source.Interface != "tui" {
		t.Fatalf("question reply: %+v", events)
	}
	if events[1].Text != "here" {
		t.Fatalf("model reply context: %+v", events[1])
	}
}

func TestQuestionRepliesKeepExactValuesWithSource(t *testing.T) {
	for _, source := range []string{"gui", "tui", "unknown"} {
		for _, option := range []string{assistant.AllowOnce, assistant.AllowAlways, assistant.AllowNot, "  here\nunchanged  "} {
			t.Run(source+"/"+option, func(t *testing.T) {
				a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
					q, _ := json.Marshal(assistant.Question{Question: "Allow bash?", Options: []string{assistant.AllowOnce, assistant.AllowAlways, assistant.AllowNot}})
					return agent.AskOwner(ctx, q)
				}}
				s := service(t, a)
				_, live, _ := s.Watch(t.Context(), "s1")
				if err := s.Ask(t.Context(), "s1", "go"); err != nil {
					t.Fatal(err)
				}
				until(t, live, Asked)
				if err := s.Steer(WithMessageSource(t.Context(), MessageSource{Interface: source}), "s1", option); err != nil {
					t.Fatal(err)
				}
				events := until(t, live, Answer)
				if len(events) != 2 || events[0].Source.Interface != source || events[0].Text != option || events[1].Text != option {
					t.Fatalf("option reply: %+v", events)
				}
			})
		}
	}
}

func TestGeneratedInputHasNoInterfaceSource(t *testing.T) {
	s := service(t, &fakeAgent{})
	se := &session{id: "s1", watchers: map[chan Event]struct{}{}}
	messages := s.taken(se, []queued{{text: goOnText, generated: true}, {text: "owner"}})
	if messages[0].Source != (MessageSource{}) || messages[1].Source.Interface != "unknown" {
		t.Fatalf("sources: %+v", messages)
	}
	model := openai.MessagesForModel(messages)
	if model[0].Content != goOnText {
		t.Fatalf("generated input mislabeled: %s", model[0].Content)
	}
}
