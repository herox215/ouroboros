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
	if got := s.archive.resumeNote("owner/whole"); !strings.HasSuffix(got, whole) || strings.HasSuffix(got, "…") {
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
	if want := task + strings.Repeat("a", maxTaskNote-5) + "…"; got != want {
		t.Errorf("long task = %q, want %q", got, want)
	}
	if len(got) > len(task)+maxTaskNote {
		t.Errorf("long task is not bounded: %d bytes, %q", len(got), got)
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
