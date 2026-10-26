package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

func TestRestartAcceptsPendingQuestionReplies(t *testing.T) {
	for _, path := range []string{"thread reply", "handoff", "local reply"} {
		t.Run(path, func(t *testing.T) {
			question := json.RawMessage(`{"question":"Allow?","options":["Yes","No"]}`)
			a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
				if text == "build" {
					AskRestart(ctx, "/new/ouro")
					return "built", nil
				}
				reply, err := agent.AskOwnerReply(ctx, question)
				if err == nil && (reply.Text != "Yes" || reply.Source.Gateway != "matrix") {
					t.Errorf("reply: %+v", reply)
				}
				return reply.Text, err
			}}
			s := service(t, a)
			replaced := make(chan string, 2)
			s.Attach(nil, func(exe string) { replaced <- exe })
			_, local, _ := s.Watch(t.Context(), "waiting")
			_, build, _ := s.Watch(t.Context(), "builder")
			if _, _, err := s.hand(t.Context(), "waiting", "work", nil, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			until(t, local, Asked)
			if err := s.Ask(t.Context(), "builder", "build"); err != nil {
				t.Fatal(err)
			}
			until(t, build, Restart)
			if err := s.Ask(t.Context(), ThreadID, "unrelated"); !errors.Is(err, ErrRestarting) {
				t.Fatalf("new message: %v", err)
			}
			ctx := WithMessageSource(t.Context(), MessageSource{Interface: "api", Gateway: "matrix"})
			var err error
			switch path {
			case "thread reply":
				err = s.Steer(ctx, ThreadID, "Yes")
			case "local reply":
				err = s.Steer(ctx, "waiting", "Yes")
			case "handoff":
				s.mu.Lock()
				s.emit(s.get(ThreadID), Event{Kind: Message, Text: "Yes", Source: MessageSource{Interface: "api", Gateway: "matrix"}})
				s.mu.Unlock()
				_, _, err = s.hand(ctx, "waiting", "Yes", nil, func() error { return nil })
			}
			if err != nil {
				t.Fatal(err)
			}
			until(t, local, Answer)
			select {
			case exe := <-replaced:
				if exe != "/new/ouro" {
					t.Fatal(exe)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("restart blocked after reply")
			}
			s.Releases()
			select {
			case <-replaced:
				t.Fatal("replacement scheduled twice")
			default:
			}
		})
	}
}

func TestRestartDeadlineParksQuestionAcrossProfiles(t *testing.T) {
	dir := t.TempDir()
	question := json.RawMessage(`{"question":"Allow?","options":["Yes","No"]}`)
	waitingAgent := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		reply, err := agent.AskOwner(ctx, question)
		return reply, err
	}}
	waiting := service(t, waitingAgent)
	waiting.Archive(history.Open(dir), t.Logf, func(string) string { return "" })
	builder := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		AskRestart(ctx, "/new/ouro")
		return "built", nil
	}})
	Share(waiting, builder)
	waiting.node.restartWait = 30 * time.Millisecond
	replaced := make(chan string, 2)
	builder.Attach(nil, func(exe string) { replaced <- exe })
	_, local, _ := waiting.Watch(t.Context(), "waiting")
	if _, _, err := waiting.hand(t.Context(), "waiting", "work", nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	until(t, local, Asked)
	if err := builder.Ask(t.Context(), "builder", "build"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-replaced:
	case <-time.After(5 * time.Second):
		t.Fatal("deadline did not release restart")
	}
	waiting.answers.Wait()
	if len(waitingAgent.notes) == 0 || !strings.Contains(waitingAgent.notes[len(waitingAgent.notes)-1], restartQuestionHead+string(question)) {
		t.Fatalf("missing pending question: %q", waitingAgent.notes)
	}
	// Model the replacement image consuming the durable resume mark and asking
	// the saved question anew. Its routed origin must reach the gateway thread.
	next := service(t, &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if !strings.Contains(text, goOnText) {
			return "resumed result", nil // the thread contextualizes the topic's result
		}
		return agent.AskOwner(ctx, question)
	}})
	next.Archive(history.Open(dir), t.Logf, func(string) string { return "" })
	// fakeAgent records Note calls but does not archive them. Persist the exact
	// note as the real agent does, then exercise normal archive replay.
	if err := next.archive.Append(history.Who{Session: "waiting"}, []openai.Message{{Role: "user", Content: strings.TrimPrefix(waitingAgent.notes[len(waitingAgent.notes)-1], "waiting: ")}}, nil); err != nil {
		t.Fatal(err)
	}
	_, thread, _ := next.Watch(t.Context(), ThreadID)
	next.GoOn()
	until(t, thread, Asked)
	if err := next.Steer(t.Context(), "waiting", "No"); err != nil {
		t.Fatal(err)
	}
	next.answers.Wait()
}
