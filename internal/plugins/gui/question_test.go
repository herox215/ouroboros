package gui

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/chat"
	"ouroboros/internal/session"
	"ouroboros/plugin"
)

// asking is the agent of a node whose answer asks the owner a question and
// is what they answer.
type asking struct{}

func (asking) Run(ctx context.Context, _, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
	reply, err := agent.AskOwnerReply(ctx, json.RawMessage(`{"question":"Which database?","options":["SQLite","Postgres"]}`))
	return reply.Text, err
}

func (asking) Ready() error                                { return nil }
func (asking) Context(string) int                          { return 0 }
func (asking) Cache(string) assistant.Cache                { return assistant.Cache{} }
func (asking) Contents(string) (assistant.Contents, error) { return assistant.Contents{}, nil }
func (asking) Inspection(string) (agent.Inspection, bool)  { return agent.Inspection{}, false }
func (asking) Conversation(string) json.RawMessage         { return nil }
func (asking) Resume(string, json.RawMessage) error        { return nil }
func (asking) Forget(string)                               {}
func (asking) Effort() string                              { return "" }
func (asking) Note(context.Context, string, string) error  { return nil }
func (asking) Title(context.Context, []chat.Message) (string, error) {
	return "", nil
}
func (asking) CheckTopic(context.Context, assistant.TopicCheck) (assistant.TopicDecision, error) {
	return assistant.TopicDecision{}, nil
}

// What the owner sends while the thread asks them a question answers it, with
// the sessions of a node: it is not queued behind the answer that waits for
// it, where it would wait for itself.
func TestSendAnswersTheQuestionOfTheThread(t *testing.T) {
	sessions := session.New(asking{})
	t.Cleanup(sessions.Close)
	w := testWindow(t, sessions)
	batches := make(chan []Event, 64)
	w.onEmit(func(events []Event) { batches <- events })
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
	if err := w.Send("set it up"); err != nil {
		t.Fatal(err)
	}
	// until reads what the thread tells until one of it is what is waited
	// for; what a batch tells after it is left for the next wait.
	var told []Event
	until := func(what string, is func(Event) bool) {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			for len(told) > 0 {
				e := told[0]
				told = told[1:]
				if e.Kind == plugin.SessionQueued {
					t.Fatalf("waiting for %s, %q was queued", what, e.Text)
				}
				if is(e) {
					return
				}
			}
			select {
			case told = <-batches:
			case <-deadline:
				t.Fatalf("%s did not come", what)
			}
		}
	}
	until("the question", func(e Event) bool { return e.Kind == plugin.SessionAsked && !e.Done })
	if err := w.Send("Postgres"); err != nil {
		t.Fatal(err)
	}
	until("the answer to the question", func(e Event) bool {
		return e.Kind == plugin.SessionAsked && e.Done && e.Text == "Postgres"
	})
	until("the answer of the thread", func(e Event) bool { return e.Kind == plugin.SessionAnswer && e.Text == "Postgres" })
}
