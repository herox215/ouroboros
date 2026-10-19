package session

import (
	"context"
	"strings"
	"testing"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
)

// A blocked topic must neither hold the thread's next answer nor bypass its
// current conversation when it eventually returns.
func TestThreadBackgroundResultAfterTopicChange(t *testing.T) {
	gate := make(chan struct{})
	reported := make(chan string, 1)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if thread := assistant.ThreadOf(ctx); thread != nil {
			switch {
			case text == "XYZ":
				if _, err := thread.Start(ctx, "inspect XYZ", ""); err != nil {
					return "", err
				}
				return "Ich kümmere mich darum.", nil
			case strings.HasPrefix(text, backgroundHead):
				reported <- text
				return "Ach übrigens, wegen XYZ von vorhin: Die Ursache ist gefunden.", nil
			default:
				return "Jetzt sprechen wir über AAA.", nil
			}
		}
		select {
		case <-gate:
			return "complete XYZ finding", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	s := service(t, a)
	_, live, _ := s.Watch(context.Background(), ThreadID)
	ask(t, s, "XYZ")
	if got := until(t, live, Answer); got[len(got)-1].Text != "Ich kümmere mich darum." {
		t.Fatalf("missing immediate acknowledgement: %+v", got)
	}
	ask(t, s, "AAA")
	if got := until(t, live, Answer); got[len(got)-1].Text != "Jetzt sprechen wir über AAA." {
		t.Fatalf("thread blocked by background work: %+v", got)
	}
	close(gate)
	got := until(t, live, Answer)
	if got[len(got)-1].Text != "Ach übrigens, wegen XYZ von vorhin: Die Ursache ist gefunden." {
		t.Fatalf("result bypassed the conversational agent: %+v", got)
	}
	for _, e := range got {
		if e.Kind == Message {
			t.Fatalf("internal result appeared as owner message: %+v", e)
		}
	}
	if report := <-reported; !strings.Contains(report, "inspect XYZ") || !strings.HasSuffix(report, "complete XYZ finding") {
		t.Fatalf("missing topic or full result: %q", report)
	}
	// Reopening the archive must not turn internal results into user messages.
	events, _ := replay([]history.Record{{Role: "user", Content: backgroundHead + "XYZ]\n\nraw finding"}})
	if len(events) != 0 {
		t.Fatalf("replayed internal result visibly: %+v", events)
	}
}
