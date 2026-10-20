package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
)

// told is what events tell, each with its kind: of an Asked the question
// it opens with, or what it was answered with.
func told(events []Event) []string {
	var got []string
	for _, e := range events {
		switch {
		case e.Kind == Asked && !e.Done:
			got = append(got, "asked "+e.AsQuestion().Question)
		case e.Kind == Asked:
			got = append(got, "answered "+e.Text)
		default:
			got = append(got, string(e.Kind)+" "+e.Text)
		}
	}
	return got
}

func same(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The agent asks the owner on its way to an answer: those who watch are
// told the question, what the owner says next is its answer and not a
// message that is queued, and one question waits at a time.
func TestAskTheOwner(t *testing.T) {
	question := json.RawMessage(`{"question":"Which?","options":["a","b"],"recommended":"a"}`)
	second, refused := make(chan struct{}), make(chan error, 1)
	a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		first := make(chan string)
		go func() {
			answer, _ := agent.AskOwner(ctx, question)
			first <- answer
		}()
		<-second
		_, err := agent.AskOwner(ctx, question)
		refused <- err
		return "chose " + <-first, nil
	}}
	s := service(t, a)
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	if err := s.Ask(ctx, "s1", "go"); err != nil {
		t.Fatal(err)
	}
	asked := until(t, live, Asked)
	if q := asked[len(asked)-1].AsQuestion(); q.Question != "Which?" || len(q.Options) != 2 || q.Recommended != "a" {
		t.Fatalf("the question: %+v", q)
	}
	close(second)
	if err := <-refused; !errors.Is(err, errAsked) {
		t.Fatalf("a second question while the first waits: %v", err)
	}
	if err := s.Steer(ctx, "s1", "b"); err != nil {
		t.Fatal(err)
	}
	if got := told(append(asked, until(t, live, Answer)...)); !same(got, "message go", "asked Which?", "answered b", "answer chose b") {
		t.Fatalf("got %q", got)
	}
}

// A question ends without an answer when the answer is stopped, or when
// the time of the tool that asked is over: what the owner says after that
// is a message again.
func TestAskTheOwnerInVain(t *testing.T) {
	question := json.RawMessage(`{"question":"Which?","options":["a","b"]}`)
	late := make(chan struct{})
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if text == "stop" {
			_, err := agent.AskOwner(ctx, question)
			return "", err
		}
		tool, cancel := context.WithTimeout(ctx, time.Millisecond)
		defer cancel()
		if _, err := agent.AskOwner(tool, question); !errors.Is(err, context.DeadlineExceeded) {
			return "", errors.New("the question outlived its tool")
		}
		<-late
		return "took " + agent.Steered(ctx)[0], nil
	}}
	s := service(t, a)
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	if err := s.Ask(ctx, "s1", "stop"); err != nil {
		t.Fatal(err)
	}
	asked := until(t, live, Asked)
	if err := s.Cancel(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if got := told(append(asked, until(t, live, Canceled)...)); !same(got, "message stop", "asked Which?", "answered ", "canceled ") {
		t.Fatalf("stopped: %q", got)
	}

	if err := s.Ask(ctx, "s1", "wait"); err != nil {
		t.Fatal(err)
	}
	until(t, live, Asked)
	over := until(t, live, Asked)
	if err := s.Steer(ctx, "s1", "too late"); err != nil {
		t.Fatal(err)
	}
	queued := until(t, live, Queued)
	close(late)
	if got := told(append(append(over, queued...), until(t, live, Answer)...)); !same(got, "answered ", "queued too late", "message too late", "answer took too late") {
		t.Fatalf("out of time: %q", got)
	}
}

// Who awaits an answer shows no question: the agent is told so instead of
// waiting for an answer that cannot come.
func TestAwaitAnswersAQuestion(t *testing.T) {
	a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		return agent.AskOwner(ctx, json.RawMessage(`{"question":"Which?","options":["a","b"]}`))
	}}
	if got, err := Await(context.Background(), service(t, a), "s1", "go"); got != Unattended || err != nil {
		t.Fatalf("got %q, %v", got, err)
	}
}

// What a tool says to the owner on its way, like that it goes on with
// their answer, is told at once as a Say.
func TestToolSays(t *testing.T) {
	a := &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		if !agent.Say(ctx, "Alles klar, ich lösche sie.") {
			return "", errors.New("nobody hears it")
		}
		return "done", nil
	}}
	s := service(t, a)
	ctx := context.Background()
	_, live, _ := s.Watch(ctx, "s1")
	if err := s.Ask(ctx, "s1", "go"); err != nil {
		t.Fatal(err)
	}
	if got := told(until(t, live, Answer)); !same(got, "message go", "say Alles klar, ich lösche sie.", "answer done") {
		t.Fatalf("got %q", got)
	}
}
