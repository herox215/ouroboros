package session

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	if _, _, err := (threading{s}).Send(&cancelsLate{Context: t.Context(), after: 3}, se.id, "add tests", "", false); !errors.Is(err, context.Canceled) {
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
