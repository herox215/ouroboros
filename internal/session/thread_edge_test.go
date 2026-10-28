package session

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// A message the thread hands on whose request is canceled while the target
// is checked is not handed on.
func TestSendCanceledWhileHanding(t *testing.T) {
	s, _, se, question := topicFixture(t)
	before := topicSnapshot(s, se)
	// Canceled at the fourth look: past the check of the concern and the
	// start of hand, at the last look before delivery.
	if _, _, err := (threading{s}).Send(&cancelsLate{Context: t.Context(), after: 3}, se.id, "add tests"); !errors.Is(err, context.Canceled) {
		t.Fatalf("sent: %v", err)
	}
	topicUnchanged(t, s, se, question, before)
}

// Only a background report names the session it reports on.
func TestReportedOnlyOfReports(t *testing.T) {
	if got := reported("a message of the owner"); got != "" {
		t.Fatalf("reported %q", got)
	}
}

// A message whose request is canceled is not handed on.
func TestHandCanceled(t *testing.T) {
	s := service(t, &fakeAgent{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := s.hand(ctx, "s1", "hi", nil, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("handed: %v", err)
	}
}

// A question the thread relays that was not asked yet is withdrawn quietly;
// once the asked one is withdrawn, the next is asked.
func TestWithdrawRelayedQuestions(t *testing.T) {
	s := service(t, &fakeAgent{})
	s.mu.Lock()
	defer s.mu.Unlock()
	thread := s.get(ThreadID)
	first, second, third := &session{id: "a"}, &session{id: "b"}, &session{id: "c"}
	question := json.RawMessage(`{"question":"Allow?"}`)
	thread.questions = []*relayed{{se: first, question: question, id: "asked"}, {se: second, question: question}, {se: third, question: question}}
	s.withdraw(second)
	if len(thread.questions) != 2 || len(thread.events) != 0 {
		t.Fatalf("questions %d, events %+v", len(thread.questions), thread.events)
	}
	s.withdraw(first)
	if len(thread.questions) != 1 || len(thread.events) != 2 || !thread.events[0].Done || thread.events[1].Kind != Asked || thread.events[1].Done {
		t.Fatalf("questions %d, events %+v", len(thread.questions), thread.events)
	}
}

// The original concern of a session that is not loaded is read from the
// archive once; without an archive or when it cannot be read there is none.
func TestConcernOfArchivedSession(t *testing.T) {
	s := service(t, &fakeAgent{})
	s.mu.Lock()
	got := s.concern("s1")
	s.mu.Unlock()
	if got != "" {
		t.Fatalf("concern without an archive: %q", got)
	}
	dir := t.TempDir()
	if err := history.Open(dir).Append(history.Who{Session: "s1"}, []openai.Message{{Role: "user", Content: "Fix the login"}}, nil); err != nil {
		t.Fatal(err)
	}
	s, _, _ = archived(t, &fakeAgent{}, dir, time.Now())
	for range 2 {
		s.mu.Lock()
		got := s.concern("s1")
		_, kept := s.concernAnchors["s1"]
		_, loaded := s.sessions["s1"]
		s.mu.Unlock()
		if got != "Fix the login" || !kept || loaded {
			t.Fatalf("concern %q, kept %v, loaded %v", got, kept, loaded)
		}
	}
	s, _, l := archived(t, &fakeAgent{}, fileArchive(t), time.Now())
	s.mu.Lock()
	got = s.concern("s1")
	s.mu.Unlock()
	if got != "" || !l.has("the original concern of session s1 cannot be read") {
		t.Fatalf("concern %q, log %q", got, l.lines)
	}
}

// relayedFixture is topicFixture with the question of the target relayed in
// the thread.
func relayedFixture(t *testing.T) (*Service, *fakeAgent, *session, chan queued) {
	t.Helper()
	s, a, se, question := topicFixture(t)
	s.mu.Lock()
	s.get(ThreadID).questions = []*relayed{{se: se, question: json.RawMessage(`{"question":"Add the redirect regression test?"}`)}}
	s.mu.Unlock()
	return s, a, se, question
}

// A reply to a relayed question is not taken for it when its request is
// canceled, before or after the check, or when the question changed while it
// was checked. Without a relayed question there is nothing to reply to.
func TestReplyToConcernRefusals(t *testing.T) {
	reply := queued{text: "yes"}
	s := service(t, &fakeAgent{})
	if answered, err := s.replyToConcern(t.Context(), reply); answered || err != nil {
		t.Fatalf("without a question: %v %v", answered, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if answered, err := s.replyToConcern(ctx, reply); answered || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v %v", answered, err)
	}

	s, a, _, _ := relayedFixture(t)
	ctx, cancel = context.WithCancel(t.Context())
	a.checkTopic = func(context.Context, assistant.TopicCheck) (assistant.TopicDecision, error) {
		cancel()
		return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "same"}, nil
	}
	if answered, err := s.replyToConcern(ctx, reply); answered || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled in the check: %v %v", answered, err)
	}

	s, a, _, _ = relayedFixture(t)
	a.checkTopic = func(context.Context, assistant.TopicCheck) (assistant.TopicDecision, error) {
		return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "same"}, nil
	}
	// Canceled at the fourth look, once the check is done.
	if answered, err := s.replyToConcern(&cancelsLate{Context: t.Context(), after: 3}, reply); answered || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled after the check: %v %v", answered, err)
	}

	s, a, _, _ = relayedFixture(t)
	a.checkTopic = func(context.Context, assistant.TopicCheck) (assistant.TopicDecision, error) {
		s.mu.Lock()
		s.get(ThreadID).questions = nil
		s.mu.Unlock()
		return assistant.TopicDecision{Disposition: assistant.TopicContinue, Reason: "same"}, nil
	}
	if answered, err := s.replyToConcern(t.Context(), reply); answered || err != nil {
		t.Fatalf("question gone: %v %v", answered, err)
	}
}

// A reply that continues the relayed question is logged with the archive.
func TestReplyToConcernLogged(t *testing.T) {
	s, _, se, question := relayedFixture(t)
	l := &logs{}
	s.Archive(history.Open(t.TempDir()), l.log, func(string) string { return "" })
	if answered, err := s.replyToConcern(t.Context(), queued{text: "yes"}); !answered || err != nil {
		t.Fatalf("answered %v, %v", answered, err)
	}
	if got := <-question; got.text != "yes" || !l.has("CheckTopic: reply continues session "+se.id) {
		t.Fatalf("reply %+v, log %q", got, l.lines)
	}
}

// Input without files is asked as it is. Files that cannot be stored fail
// the input, and leave nothing behind; so does a canceled request.
func TestAttachmentsNotStored(t *testing.T) {
	s := service(t, &fakeAgent{})
	_, live, _ := s.Watch(t.Context(), "s1")
	if err := s.AskAttachments(t.Context(), "s1", "hi", nil); err != nil {
		t.Fatal(err)
	}
	until(t, live, Answer)
	files := []Attachment{{Name: "a", Data: []byte("x")}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.AskAttachments(ctx, "s1", "hi", files); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}

	tmp := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(tmp, "missing"))
	if err := s.AskAttachments(t.Context(), "s1", "hi", files); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("without a temporary directory: %v", err)
	}
	t.Setenv("TMPDIR", tmp)
	broken := errors.New("disk full")
	old := writeAttachment
	t.Cleanup(func() { writeAttachment = old })
	writeAttachment = func(string, []byte, os.FileMode) error { return broken }
	if err := s.AskAttachments(t.Context(), "s1", "hi", files); !errors.Is(err, broken) {
		t.Fatalf("not written: %v", err)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// A relative directory needs the working directory to resolve against.
func TestDirectoryWithoutWorkingDirectory(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root looks into every directory")
	}
	closed := t.TempDir()
	t.Chdir(closed)
	// Not even "." can be looked up in a directory without search permission.
	if err := os.Chmod(closed, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(closed, 0o700) })
	if got, err := resolveDirectory("project", ""); err == nil {
		t.Fatalf("resolved %q", got)
	}
}
