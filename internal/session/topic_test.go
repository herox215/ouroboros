package session

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// Keep the target waiting on a buffered question rather than launching an
// answer. Delivery is observable, and refusals cannot hide in background work.
func topicFixture(t *testing.T) (*Service, *fakeAgent, *session, chan queued) {
	t.Helper()
	a := &fakeAgent{}
	s := service(t, a)
	s.mu.Lock()
	defer s.mu.Unlock()
	se := s.get("target-concern")
	s.emit(se, Event{Kind: Message, Text: "Fix the login redirect loop"})
	s.emit(se, Event{Kind: Answer, Text: "The redirect cookie needs a regression test."})
	question := make(chan queued, 1)
	se.asked = question
	se.queue = []queued{{text: "already waiting"}}
	thread := s.get(ThreadID)
	s.emit(thread, Event{Kind: Message, Text: "Back to the login redirect: add the regression test."})
	thread.said = []string{"Back to the login redirect: add the regression test."}
	return s, a, se, question
}

type topicState struct {
	events []Event
	queue  []queued
	asked  chan queued
	routed bool
}

func topicSnapshot(s *Service, se *session) topicState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return topicState{slices.Clone(se.events), slices.Clone(se.queue), se.asked, se.routed}
}

func topicUnchanged(t *testing.T, s *Service, se *session, question chan queued, before topicState) {
	t.Helper()
	if after := topicSnapshot(s, se); !reflect.DeepEqual(before, after) {
		t.Fatalf("refused send changed target: before %+v; after %+v", before, after)
	}
	select {
	case got := <-question:
		t.Fatalf("refused send delivered %+v", got)
	default:
	}
}

func TestTopicSendRefusesBeforeDelivery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision assistant.TopicDecision
		err      error
	}{
		{"new", assistant.TopicDecision{Disposition: assistant.TopicNew, Reason: "a distinct concern"}, nil},
		{"unclear", assistant.TopicDecision{Disposition: assistant.TopicUnclear, Reason: "no identifiable referent"}, nil},
		{"error", assistant.TopicDecision{}, errors.New("CheckTopic unavailable")},
		{"empty", assistant.TopicDecision{}, nil},
		{"invalid", assistant.TopicDecision{Disposition: "maybe", Reason: "not a valid disposition"}, nil},
		{"missing reason", assistant.TopicDecision{Disposition: assistant.TopicContinue}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a, se, question := topicFixture(t)
			before := topicSnapshot(s, se)
			called := false
			a.checkTopic = func(_ context.Context, check assistant.TopicCheck) (assistant.TopicDecision, error) {
				called = true
				// The verifier must run before even answering a pending question,
				// and without holding the lock needed by other session work.
				if !s.mu.TryLock() {
					t.Error("CheckTopic runs under the service lock")
				} else {
					after := topicState{slices.Clone(se.events), slices.Clone(se.queue), se.asked, se.routed}
					s.mu.Unlock()
					if !reflect.DeepEqual(before, after) || len(question) != 0 {
						t.Error("target changed before verification")
					}
				}
				if check.Anchor != "Fix the login redirect loop" || check.Message != "Change the billing export" {
					t.Errorf("check evidence %+v", check)
				}
				return tc.decision, tc.err
			}
			if _, _, err := (threading{s}).Send(context.Background(), se.id, "Change the billing export"); err == nil {
				t.Fatal("refused decision accepted")
			}
			if !called {
				t.Fatal("CheckTopic was not called")
			}
			topicUnchanged(t, s, se, question, before)
		})
	}
}

func TestTopicSendMissingAnchor(t *testing.T) {
	s, a, se, question := topicFixture(t)
	s.mu.Lock()
	se.events = []Event{{Kind: Answer, Text: "No owner request survives", At: s.now()}}
	se.title = "A title is not an anchor"
	s.mu.Unlock()
	a.checkTopic = func(context.Context, assistant.TopicCheck) (assistant.TopicDecision, error) {
		t.Error("CheckTopic called without an owner anchor")
		return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "not evidence"}, nil
	}
	before := topicSnapshot(s, se)
	if _, _, err := (threading{s}).Send(context.Background(), se.id, "yes"); err == nil {
		t.Fatal("target without an anchor accepted")
	}
	topicUnchanged(t, s, se, question, before)
}

func topicHas(messages []openai.Message, role, text string) bool {
	return slices.ContainsFunc(messages, func(m openai.Message) bool { return m.Role == role && strings.Contains(m.Content, text) })
}

func TestTopicSendContinueUsesOriginalWordsAndEvidence(t *testing.T) {
	s, a, se, question := topicFixture(t)
	const original = "Back to the login redirect: add the regression test."
	called := false
	a.checkTopic = func(_ context.Context, check assistant.TopicCheck) (assistant.TopicDecision, error) {
		called = true
		if !s.mu.TryLock() {
			t.Error("CheckTopic runs under the service lock")
		} else {
			s.mu.Unlock()
		}
		if check.Anchor != "Fix the login redirect loop" || check.Message != original ||
			!topicHas(check.Recent, "assistant", "redirect cookie") ||
			!topicHas(check.Context, "user", original) {
			t.Errorf("missing contextual evidence: %+v", check)
		}
		select {
		case got := <-question:
			t.Errorf("delivered before verification: %+v", got)
		default:
		}
		return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "same regression"}, nil
	}
	if _, _, err := (threading{s}).Send(context.Background(), handle(se.id), ""); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("CheckTopic was not called")
	}
	select {
	case got := <-question:
		if got.text != original || !got.routed {
			t.Fatalf("delivery changed original message: %+v", got)
		}
	default:
		t.Fatal("continue did not deliver")
	}
}

func TestTopicSendReturnsToStableOriginalConcern(t *testing.T) {
	s, a, se, question := topicFixture(t)
	s.mu.Lock()
	se.title = "Billing export (a later misleading title)"
	s.emit(se, Event{Kind: Message, Text: "Unrelated billing export request"})
	s.emit(se, Event{Kind: Answer, Text: "Billing export finished"})
	other := s.get("billing-concern")
	s.emit(other, Event{Kind: Message, Text: "Unrelated billing export request"})
	thread := s.get(ThreadID)
	s.emit(thread, Event{Kind: Message, Text: "Now discuss billing exports"})
	s.emit(thread, Event{Kind: Answer, Text: "The billing export is finished"})
	const back = "Back to the login redirect loop: and tests?"
	s.emit(thread, Event{Kind: Message, Text: back})
	s.mu.Unlock()
	called := false
	a.checkTopic = func(_ context.Context, check assistant.TopicCheck) (assistant.TopicDecision, error) {
		called = true
		if check.Anchor != "Fix the login redirect loop" || check.Message != back ||
			!topicHas(check.Recent, "user", "Unrelated billing") ||
			!topicHas(check.Context, "assistant", "billing export is finished") ||
			!topicHas(check.Context, "user", back) {
			t.Errorf("return to earlier concern got wrong evidence: %+v", check)
		}
		return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "explicit return"}, nil
	}
	if _, _, err := (threading{s}).Send(context.Background(), se.id, back); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("CheckTopic was not called")
	}
	select {
	case got := <-question:
		if got.text != back {
			t.Fatalf("delivered %+v", got)
		}
	default:
		t.Fatal("return to earlier concern not delivered")
	}
}

func TestTopicSendCancelledDoesNotDeliver(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(strconv.FormatBool(during), func(t *testing.T) {
			s, a, se, question := topicFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !during {
				cancel()
			}
			a.checkTopic = func(context.Context, assistant.TopicCheck) (assistant.TopicDecision, error) {
				cancel()
				// Even a verifier that ignores cancellation cannot authorize delivery.
				return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "same concern"}, nil
			}
			before := topicSnapshot(s, se)
			if _, _, err := (threading{s}).Send(ctx, se.id, "add tests"); err == nil {
				t.Fatal("cancelled send accepted")
			}
			topicUnchanged(t, s, se, question, before)
		})
	}
}

func TestTopicSendRejectsConcurrentTargetChange(t *testing.T) {
	s, a, se, question := topicFixture(t)
	checking, release := make(chan struct{}), make(chan struct{})
	a.checkTopic = func(context.Context, assistant.TopicCheck) (assistant.TopicDecision, error) {
		close(checking)
		<-release
		return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "old snapshot"}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := (threading{s}).Send(context.Background(), se.id, "add tests")
		done <- err
	}()
	select {
	case <-checking:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("verifier was not reached")
	}
	s.mu.Lock()
	s.emit(se, Event{Kind: Message, Text: "A concurrent direct request changed the target"})
	s.mu.Unlock()
	before := topicSnapshot(s, se)
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stale verifier result delivered blindly")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("send did not finish")
	}
	topicUnchanged(t, s, se, question, before)
}

func TestTopicRosterUsesArchivedOriginalConcern(t *testing.T) {
	dir := t.TempDir()
	arch := history.Open(dir)
	const anchor = "Fix the original login redirect"
	if err := arch.Append(history.Who{Session: "kept"}, []openai.Message{
		{Role: "user", Content: anchor},
		{Role: "assistant", Content: "Initial fix"},
		{Role: "user", Content: "Later unrelated billing request"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	arch.SetTitle(history.Owner("kept"), "A changed title")
	arch.Close()
	a := &fakeAgent{}
	s, _, _ := archived(t, a, dir, time.Now())
	s.mu.Lock()
	got := roster(3*time.Hour, s.routable(), "")
	s.mu.Unlock()
	if !strings.Contains(got, " · concern: "+strconv.Quote(anchor)) {
		t.Fatalf("archive roster lost original concern: %s", got)
	}
	// Loading the archived target must preserve that same anchor for the
	// independent check, not substitute its current title or last request.
	s.mu.Lock()
	se := s.get("kept")
	question := make(chan queued, 1)
	se.asked = question
	s.mu.Unlock()
	called := false
	a.checkTopic = func(_ context.Context, check assistant.TopicCheck) (assistant.TopicDecision, error) {
		called = true
		if check.Anchor != anchor {
			t.Errorf("archived target anchor = %q, want %q", check.Anchor, anchor)
		}
		return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "return to original concern"}, nil
	}
	const followup = "Back to the original login redirect: add tests"
	if _, _, err := (threading{s}).Send(context.Background(), "kept", followup); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("archived target bypassed CheckTopic")
	}
	select {
	case message := <-question:
		if message.text != followup {
			t.Fatalf("archived target received %+v", message)
		}
	default:
		t.Fatal("archived target did not receive continued message")
	}
}

func TestTopicRosterQuotesAndBoundsOriginalConcern(t *testing.T) {
	s, _, se, _ := topicFixture(t)
	const anchor = "Fix the \"login\" redirect\nwithout changing billing"
	s.mu.Lock()
	se.events[0].Text = anchor
	se.title = "An unrelated later title"
	got := roster(3*time.Hour, s.routable(), "")
	s.mu.Unlock()
	if !strings.Contains(got, " · concern: "+strconv.Quote(oneLine(anchor))) {
		t.Fatalf("roster did not quote the original concern: %s", got)
	}

	s.mu.Lock()
	se.events[0].Text = strings.Repeat("x", 800)
	got = roster(3*time.Hour, s.routable(), "")
	s.mu.Unlock()
	_, tail, ok := strings.Cut(got, " · concern: ")
	if !ok {
		t.Fatalf("roster has no concern: %s", got)
	}
	// Decode the quoted head rather than counting quoting or roster metadata.
	line, _, _ := strings.Cut(tail, "\n")
	quoted, err := strconv.QuotedPrefix(line)
	if err != nil {
		t.Fatalf("concern is not quoted: %q (%v)", line, err)
	}
	head, err := strconv.Unquote(quoted)
	if err != nil || len(head) > 500 || !strings.HasPrefix(head, strings.Repeat("x", 100)) {
		t.Fatalf("concern head is not bounded to 500 bytes: %q (%v)", head, err)
	}
}
