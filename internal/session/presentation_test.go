package session

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
	"ouroboros/internal/presentation"
)

type presentationModel struct {
	calls int
	fail  bool
}

func (m *presentationModel) Complete(_ context.Context, _ string, _ []openai.Message, _ []openai.Tool) (openai.Message, error) {
	m.calls++
	if m.calls%2 == 1 {
		return openai.Message{Content: "same words", ToolCalls: []openai.ToolCall{call("tool-call", "noop", "{}")}}, nil
	}
	if m.fail {
		return openai.Message{}, errors.New("private failure")
	}
	return openai.Message{Content: "same words"}, nil
}

func TestLivePresentationIDsMatchArchiveReplay(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			arch := history.Open(t.TempDir())
			bot, err := agent.New(&presentationModel{fail: failed}, "model", "", agent.NewTool("noop", "no operation", nil, func(context.Context, struct{}) (string, error) { return "ok", nil })).WithExtensions("", nil, []agent.AnswerHook{{Name: "archive", After: func(ctx context.Context, o agent.AnswerOutcome) error {
				return arch.AppendWithPresentationID(history.Who{Session: "s1"}, o.Messages, o.Err, presentation.Terminal(ctx))
			}}})
			if err != nil {
				t.Fatal(err)
			}
			a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), say func(string)) (string, error) {
				return bot.Run(ctx, "s1", text, agent.Observer{Call: func(c agent.Call) {
					if c.Done && c.Err == nil && c.ToolCalls > 0 {
						say(c.Content)
					}
				}})
			}}
			s := service(t, a)
			// The live clock differs deliberately from archive timestamps.
			s.now = func() time.Time { return time.Unix(100, 0) }
			_, live, err := s.Watch(t.Context(), "s1")
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for range 2 {
				if err := s.Ask(t.Context(), "s1", "again"); err != nil {
					t.Fatal(err)
				}
				for _, e := range until(t, live, Answer, Failed) {
					if e.Kind == Say || e.Kind == Answer || e.Kind == Failed {
						if e.PresentationID == "" {
							t.Fatal("live output has no ID")
						}
						if slices.Contains(ids, e.PresentationID) {
							t.Fatal("identical text reused output ID")
						}
						ids = append(ids, e.PresentationID)
					}
				}
			}
			records, err := arch.Records(history.Owner("s1"))
			if err != nil {
				t.Fatal(err)
			}
			replayed, _ := replay(records)
			var restoredIDs []string
			for _, e := range replayed {
				if e.Kind == Say || e.Kind == Answer || e.Kind == Failed {
					restoredIDs = append(restoredIDs, e.PresentationID)
				}
			}
			if !slices.Equal(ids, restoredIDs) {
				t.Fatalf("live IDs %v; replay IDs %v", ids, restoredIDs)
			}
		})
	}
}

func TestLegacyReplayPresentationIDsAreStable(t *testing.T) {
	records := []history.Record{{Seq: 1, Role: "assistant", Content: "same"}, {Seq: 2, Role: "assistant", Content: "same"}, {Seq: 3, Role: "end", Outcome: history.Canceled, PresentationID: "canceled-id"}}
	first, _ := replay(records)
	records[0].Time = time.Now()
	second, _ := replay(records)
	if first[0].PresentationID == "" || first[0].PresentationID == first[1].PresentationID || first[0].PresentationID != second[0].PresentationID || first[2].PresentationID != "canceled-id" {
		t.Fatalf("replay IDs: %+v", first)
	}
}
