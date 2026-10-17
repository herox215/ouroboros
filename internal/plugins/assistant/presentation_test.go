package assistant_test

import (
	"context"
	"errors"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
	assistantplugin "ouroboros/internal/plugins/assistant"
	"ouroboros/internal/presentation"
)

func TestArchivedPresentationID(t *testing.T) {
	for _, test := range []struct {
		name       string
		peer       bool
		statistics bool
		err        error
		outcome    string
	}{
		{"failed without statistics", false, false, errors.New("private failure"), history.Failed},
		{"canceled with statistics", false, true, context.Canceled, history.Canceled},
		{"peer without statistics", true, true, context.Canceled, history.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := history.Open(t.TempDir())
			defer archive.Close()
			ctx := presentation.New(context.Background())
			terminalID := presentation.Terminal(ctx)
			if terminalID == "" {
				t.Fatal("missing trace terminal ID")
			}
			messageID := presentation.Next(ctx)
			o := agent.AnswerOutcome{
				Processing: agent.Processing{Conversation: agent.Conversation{ID: "chat", Peer: test.peer, Origin: a2a.Origin{PublicKey: "key"}}},
				Messages:   []openai.Message{{Role: "assistant", Content: "partial", PresentationID: messageID}},
				Err:        test.err,
			}
			asked := false
			hook := assistantplugin.Archived(archive)
			if test.statistics {
				hook = assistantplugin.Archived(archive, func(string) history.Statistics {
					asked = true
					return history.Statistics{PresentationID: "must-be-overridden"}
				})
			}
			// Cancellation must not lose the terminal identity or prevent archival.
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			if err := hook.After(ctx, o); err != nil {
				t.Fatal(err)
			}
			id := history.Owner("chat")
			if test.peer {
				id = history.Party("key") + "/chat"
			}
			got, err := archive.Records(id)
			if err != nil || len(got) != 2 {
				t.Fatalf("records: %+v, %v", got, err)
			}
			end := got[1]
			if got[0].PresentationID != messageID || end.PresentationID != terminalID || end.Outcome != test.outcome {
				t.Fatalf("presentation/outcome: %+v", got)
			}
			wantStatistics := test.statistics && !test.peer
			if asked != wantStatistics || (end.Statistics != nil) != wantStatistics {
				t.Fatalf("statistics: asked=%v, end=%+v", asked, end)
			}
		})
	}
}
