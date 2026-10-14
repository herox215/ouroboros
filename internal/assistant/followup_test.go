package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A short message soon after an answer has that answer reviewed, with what
// the owner wrote, also when the answer had no signals of its own.
func TestFollowUpIsReviewed(t *testing.T) {
	llm := &reviewLLM{reply: "CHANGE: do what you say."}
	a, log := recorded(t, ownerConfig(), llm)
	got := make(chan string, 2)
	ctx := WithReview(context.Background(), func(text string) { got <- text })
	if _, err := a.Ask(ctx, "c", "build the page", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Ask(ctx, "c", "  did   anything happen? ", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-got:
		if text != "do what you say." {
			t.Fatalf("suggested %q", text)
		}
	case <-time.After(time.Second):
		t.Fatalf("not reviewed:\n%s", strings.Join(log.take(), "\n"))
	}
	asked := llm.reviewed[1].Content
	for _, want := range []string{`message from the owner: "build the page"`, `- follow-up: the owner wrote next, 0s later: "did anything happen?"`} {
		if !strings.Contains(asked, want) {
			t.Errorf("the review is not sent %q:\n%s", want, asked)
		}
	}
	if strings.Contains(asked, `message from the owner: "  did`) {
		t.Errorf("the review is sent the follow-up as the answer:\n%s", asked)
	}
	lines := log.take()
	if !slicesContain(lines, `chat c: signals of the answer before: the owner wrote next, 0s later: "did anything happen?"`) {
		t.Errorf("the log:\n%s", strings.Join(lines, "\n"))
	}
}

func slicesContain(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// Only the next message follows up on an answer, and only a short one that
// comes soon; one after an answer to a suggestion taken does not, and a
// conversation forgotten has no answer to follow up on.
func TestWhatFollowsUp(t *testing.T) {
	a := &Assistant{}
	now := time.Now()
	last := answered{at: now, trail: []string{"x"}}
	a.keep("c", last)
	if _, _, ok := a.followed("c", strings.Repeat("a", followUpRunes+1), now); ok {
		t.Error("a long message follows up")
	}
	if _, _, ok := a.followed("c", "?", now); ok {
		t.Error("the message after the next follows up")
	}
	a.keep("c", last)
	if _, s, ok := a.followed("c", strings.Repeat("ä", followUpRunes), now.Add(followUpTime)); !ok || s.Kind != SignalFollowUp {
		t.Errorf("a short message soon after: %v, %v", s, ok)
	}
	a.keep("c", last)
	if _, _, ok := a.followed("c", "?", now.Add(followUpTime+time.Second)); ok {
		t.Error("a message long after follows up")
	}
	a.keep("c", last)
	if _, _, ok := a.followed("c", " \n", now); ok {
		t.Error("an empty message follows up")
	}
	a.keep("c", answered{at: now, task: true})
	if _, _, ok := a.followed("c", "?", now); ok {
		t.Error("a message after a suggestion taken follows up")
	}
	a.keep("c", last)
	a.forget("c")
	if _, _, ok := a.followed("c", "?", now); ok {
		t.Error("a forgotten conversation follows up")
	}
}

// Without a review nothing is kept for a follow-up.
func TestNothingKeptWithoutReview(t *testing.T) {
	a, _ := recorded(t, ownerConfig(), &reviewLLM{})
	if _, err := a.Ask(context.Background(), "c", "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(a.last) != 0 {
		t.Fatalf("kept %v", a.last)
	}
}
