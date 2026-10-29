package assistant

import (
	"strings"
	"testing"
	"time"

	"ouroboros/internal/memory"
)

// wake returns at once when the archivist was stopped, even if a pass is due.
func TestWakeStopsWhenTheArchivistIsStopped(t *testing.T) {
	dir := t.TempDir()
	a, _ := archivistAssistant(t, archiveConfig("1h"), &archiveLLM{}, dir)
	a.unarchived = true
	a.wake(time.Now())
	if a.due != nil || !a.looked.IsZero() {
		t.Fatalf("a stopped archivist planned or ran a pass: due %v, looked %v", a.due, a.looked)
	}
}

// wake returns when a pass leaves no next moment to look: the profiles are
// gone after it.
func TestWakeReturnsWhenNoNextPassIsDue(t *testing.T) {
	dir := t.TempDir()
	a, _ := archivistAssistant(t, archiveConfig("1h"), &archiveLLM{}, dir)
	all := a.builder.memoriesOf
	calls := 0
	a.builder = a.builder.WithMemoryProfiles(func() []MemoryProfile {
		calls++
		if calls > 2 { // the look before the pass, and the pass itself
			return nil
		}
		return all()
	})
	a.wake(time.Now())
	if calls != 3 {
		t.Fatalf("memories listed %d times, want 3", calls)
	}
	if a.due != nil || a.looked.IsZero() {
		t.Fatalf("a pass without a next one: due %v, looked %v", a.due, a.looked)
	}
}

// nextArchive says that there is no next pass without profiles.
func TestNextArchiveWithoutProfiles(t *testing.T) {
	a, _ := recorded(t, archiveConfig("1h"), &archiveLLM{})
	a.builder = a.builder.WithMemoryProfiles(func() []MemoryProfile { return nil })
	if at, on := a.nextArchive(time.Now()); on || !at.IsZero() {
		t.Fatalf("next pass %v, %v", at, on)
	}
}

// nextArchive reports a store whose marker cannot be read, and says so in
// the log.
func TestNextArchiveLogsAMarkerFailure(t *testing.T) {
	db, store := rawStore(t)
	if _, err := db.Exec("DROP TABLE memory_meta"); err != nil {
		t.Fatal(err)
	}
	a, log := recorded(t, archiveConfig("1h"), &archiveLLM{})
	a.builder = a.builder.WithMemoryProfiles(func() []MemoryProfile {
		return []MemoryProfile{{Dir: "raw", Store: func() (*memory.Store, error) { return store, nil }}}
	})
	if at, on := a.nextArchive(time.Now()); on || !at.IsZero() {
		t.Fatalf("next pass %v, %v", at, on)
	}
	if lines := log.take(); len(lines) != 1 || !strings.HasPrefix(lines[0], "the memories cannot be archived: ") {
		t.Fatalf("log %q", lines)
	}
}

// archive does nothing at all without profiles.
func TestArchiveWithoutProfiles(t *testing.T) {
	llm := &archiveLLM{}
	a, _ := recorded(t, archiveConfig("1h"), llm)
	a.archive(time.Now())
	if n := llm.count(); n != 0 {
		t.Fatalf("the model was asked %d times without profiles", n)
	}
}

// Stop stops the look the archivist planned for later.
func TestStopStopsThePlannedLook(t *testing.T) {
	a, _ := recorded(t, archiveConfig("1h"), &archiveLLM{})
	a.waitUntil(time.Now().Add(time.Hour))
	if a.due == nil {
		t.Fatal("no look was planned")
	}
	a.Stop()
	if a.due.Stop() {
		t.Fatal("Stop left the planned look running")
	}
	if !a.stopped || !a.unarchived {
		t.Fatalf("stopped %v, unarchived %v", a.stopped, a.unarchived)
	}
}

// A look that comes due after the archivist was stopped plans nothing.
func TestWaitUntilAfterStop(t *testing.T) {
	a, _ := recorded(t, archiveConfig("1h"), &archiveLLM{})
	a.StopArchive()
	a.waitUntil(time.Now().Add(time.Hour))
	a.learning.Lock()
	due := a.due
	a.learning.Unlock()
	if due != nil {
		t.Fatal("planned a look although the archivist is stopped")
	}
}

// nextArchive reports a store that cannot be opened, and says so in the
// log.
func TestNextArchiveLogsAStoreFailure(t *testing.T) {
	a, log := recorded(t, archiveConfig("1h"), &archiveLLM{})
	a.builder = a.builder.WithMemoryProfiles(func() []MemoryProfile {
		return []MemoryProfile{{Dir: "gone", Store: func() (*memory.Store, error) { return nil, errTest }}}
	})
	if at, on := a.nextArchive(time.Now()); on || !at.IsZero() {
		t.Fatalf("next pass %v, %v", at, on)
	}
	if lines := log.take(); len(lines) != 1 || lines[0] != "the memories cannot be archived: "+errTest.Error() {
		t.Fatalf("log %q", lines)
	}
}

// A look planned again moves the one planned before rather than adding one.
func TestWaitUntilMovesThePlannedLook(t *testing.T) {
	a, _ := recorded(t, archiveConfig("1h"), &archiveLLM{})
	a.waitUntil(time.Now().Add(time.Hour))
	first := a.due
	a.waitUntil(time.Now().Add(2 * time.Hour))
	if a.due != first {
		t.Fatal("a second look was planned beside the first")
	}
	a.StopArchive()
}
