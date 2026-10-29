package session

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// resumeNote names the chat and repeats the first task as far as it is there:
// a missing chat or task leaves the name alone, and what it cuts stays UTF-8.
func TestResumeNote(t *testing.T) {
	s, arch, l := archived(t, &fakeAgent{}, filepath.Join(t.TempDir(), "history"), time.Now())
	const name = "\n\nThis chat is \"s1\" in the history archive."
	const tail = "\n\n" + readLast
	if got := s.archive.resumeNote("owner/s1"); got != name {
		t.Errorf("without an archived answer: %q, want %q", got, name)
	}
	if l.has("cannot be read") {
		t.Error("a chat without an answer is logged")
	}

	appendTask := func(session, task string) {
		t.Helper()
		if err := arch.Append(history.Who{Session: session}, []openai.Message{{Role: "user", Content: task}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	appendTask("empty", " \n")
	if got, want := s.archive.resumeNote("owner/empty"), "\n\nThis chat is \"empty\" in the history archive."; got != want {
		t.Errorf("a task of spaces: %q, want %q", got, want)
	}

	whole := strings.Repeat("b", maxTaskNote)
	appendTask("whole", whole)
	if got := s.archive.resumeNote("owner/whole"); !strings.HasSuffix(got, whole+tail) || strings.Contains(got, "…") {
		t.Errorf("a task of %d bytes is cut: %q", maxTaskNote, got)
	}

	// The cut at maxTaskNote falls into this character; the note stays UTF-8.
	long := strings.Repeat("a", maxTaskNote-5) + "😀" + "tail"
	appendTask("long", long)
	got := s.archive.resumeNote("owner/long")
	if !utf8.ValidString(got) || strings.Contains(got, "\uFFFD") {
		t.Errorf("a cut task is not whole UTF-8: %q", got)
	}
	const task = "\n\nThis chat is \"long\" in the history archive. Its first task, as the owner put it:\n"
	if want := task + strings.Repeat("a", maxTaskNote-5) + "…" + tail; got != want {
		t.Errorf("long task = %q, want %q", got, want)
	}
	if len(got) > len(task)+maxTaskNote+len(tail) {
		t.Errorf("long task is not bounded: %d bytes, %q", len(got), got)
	}

	// The last message of the owner follows the first, but not a note of
	// ouro or the report of a task.
	if err := arch.Append(history.Who{Session: "long"}, []openai.Message{{Role: "user", Content: "now fix the tests"}, {Role: "user", Content: "[Task 1, which you delegated, ended.]"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.archive.resumeNote("owner/long"); !strings.HasSuffix(got, "\n\nWhat the owner wrote last:\nnow fix the tests"+tail) {
		t.Errorf("without the last message of the owner: %q", got)
	}

	// Both restart notes stay recognizable with what resumeNote adds.
	note := s.archive.resumeNote("owner/whole")
	for _, head := range []string{goOnText, interruptedText} {
		if !restartNote(head) || !restartNote(head+note) {
			t.Errorf("restart note %q is not recognized with %q", head, note)
		}
	}
	if restartNote("a message of the owner") {
		t.Error("a message of the owner is a restart note")
	}
}

// An answer that stopped in the middle is told where the archive of its chat
// ends and how long before now, or that nothing of it is archived.
func TestArchiveEnd(t *testing.T) {
	s, arch, _ := archived(t, &fakeAgent{}, filepath.Join(t.TempDir(), "history"), time.Now())
	if got, want := s.archive.archiveEnd("owner/none", time.Now()), "\n\nNothing of this chat is archived: what the answer was asked and did is not in history_read."; got != want {
		t.Errorf("without an archived answer: %q, want %q", got, want)
	}
	if err := arch.Append(history.Who{Session: "s"}, []openai.Message{{Role: "user", Content: "the task"}}, nil); err != nil {
		t.Fatal(err)
	}
	records, err := arch.Records("owner/s")
	if err != nil {
		t.Fatal(err)
	}
	last := records[len(records)-1].Time
	want := "\n\nThe archive of this chat ends at " + last.Local().Format("2006-01-02 15:04") + ", 22 minutes before ouro started anew: what the answer was asked and did, and whatever came after that, is not in history_read."
	if got := s.archive.archiveEnd("owner/s", last.Add(22*time.Minute+time.Second)); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
