package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/openai"
)

// joinModel would finish immediately after delegation if the session let it.
type joinModel struct {
	calls int
	seen  chan []openai.Message
}

func (m *joinModel) Complete(_ context.Context, _ string, messages []openai.Message, _ []openai.Tool) (openai.Message, error) {
	m.calls++
	if m.calls == 1 {
		return openai.Message{Content: "I am splitting the work into parallel checks.", ToolCalls: []openai.ToolCall{{ID: "split", Type: "function", Function: openai.FunctionCall{Name: "split", Arguments: "{}"}}}}, nil
	}
	m.seen <- messages
	return openai.Message{Content: "Combined conclusion"}, nil
}

func joiningService(t *testing.T, tasks ...assistant.Task) (*Service, <-chan Event, <-chan []openai.Message) {
	t.Helper()
	m := &joinModel{seen: make(chan []openai.Message, 4)}
	tool := agent.NewTool("split", "Split independent checks", nil, func(ctx context.Context, _ struct{}) (string, error) {
		_, err := assistant.TasksOf(ctx).Start(tasks)
		return "started", err
	})
	bot := agent.New(m, "test", "system", tool)
	s := service(t, &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), say func(string)) (string, error) {
		return bot.Run(ctx, "s1", text, agent.Observer{Call: func(c agent.Call) {
			if c.Done && c.Err == nil && c.ToolCalls > 0 {
				say(c.Content)
			}
		}})
	}})
	_, live, err := s.Watch(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ask(context.Background(), "s1", "check"); err != nil {
		t.Fatal(err)
	}
	return s, live, m.seen
}

func noJoinedConclusion(t *testing.T, seen <-chan []openai.Message) {
	t.Helper()
	select {
	case messages := <-seen:
		t.Fatalf("model resumed before all helpers ended: %+v", messages)
	case <-time.After(30 * time.Millisecond):
	}
}

// Reports are collected before the next model request (and hence before any
// final streaming), including failures, rather than triggering later answers.
func TestTasksJoinBeforeNextModelRound(t *testing.T) {
	first, last := make(chan struct{}), make(chan struct{})
	_, live, seen := joiningService(t, waiting("first", first, "found it", nil), waiting("last", last, "", errTest))
	initial := until(t, live, Tasks)
	if initial[1].Kind != Say {
		t.Fatalf("no announcement before tasks: %+v", initial)
	}
	noJoinedConclusion(t, seen)
	close(first)
	until(t, live, Tasks)
	noJoinedConclusion(t, seen)
	close(last)
	events := until(t, live, Answer)
	reports := 0
	for _, e := range events {
		if e.Kind == Task {
			reports++
		}
	}
	if reports != 2 || events[len(events)-1].Text != "Combined conclusion" {
		t.Fatalf("events: %+v", events)
	}
	select {
	case messages := <-seen:
		found := 0
		for _, m := range messages {
			if m.Content == assistant.TaskReport(1, "first", "found it", nil) || m.Content == assistant.TaskReport(2, "last", "", errTest) {
				found++
			}
		}
		if found != 2 {
			t.Fatalf("missing joined results: %+v", messages)
		}
	case <-time.After(time.Second):
		t.Fatal("no combined model request")
	}
	select {
	case extra := <-seen:
		t.Fatalf("extra conclusion: %+v", extra)
	default:
	}
}

func TestTasksJoinCanBeCanceled(t *testing.T) {
	for _, stop := range []string{"cancel", "close"} {
		t.Run(stop, func(t *testing.T) {
			gate := make(chan struct{})
			s, live, seen := joiningService(t, waiting("blocked", gate, "not used", nil))
			until(t, live, Tasks)
			noJoinedConclusion(t, seen)
			if stop == "cancel" {
				if err := s.Cancel(context.Background(), "s1"); err != nil {
					t.Fatal(err)
				}
				events := until(t, live, Canceled)
				for _, e := range events {
					if e.Kind == Answer {
						t.Fatalf("final answer on cancellation: %+v", events)
					}
				}
			} else {
				done := make(chan struct{})
				go func() { s.Close(); close(done) }()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("close blocked joining helpers")
				}
			}
			select {
			case messages := <-seen:
				t.Fatalf("model called after %s: %s", stop, strings.TrimSpace(messages[len(messages)-1].Content))
			default:
			}
		})
	}
}
