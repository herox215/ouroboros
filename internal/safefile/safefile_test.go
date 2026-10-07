package safefile

import (
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// leftovers are the files in the directory of path besides path itself.
func leftovers(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestWriteReplacesContentAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.json")
	os.WriteFile(path, []byte("a long old content"), 0o644)
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Fatalf("content = %q", b)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 600", info.Mode().Perm())
	}
	if left := leftovers(t, path); len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
	// Also a mode that a temporary file does not have anyway.
	if err := Write(path, []byte("shared"), 0o640); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Fatalf("perm = %o, want 640", info.Mode().Perm())
	}
}

// A file that is open keeps its old content: the new one is another file.
func TestWriteDoesNotTouchTheOldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.json")
	os.WriteFile(path, []byte("old"), 0o600)
	old, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 16)
	if n, _ := old.Read(b); string(b[:n]) != "old" {
		t.Fatalf("the old file was written to: %q", b[:n])
	}
}

// A write that fails leaves the target and its directory as they were.
func TestWriteFailureLeavesNothingBehind(t *testing.T) {
	if err := Write(filepath.Join(t.TempDir(), "missing", "file"), nil, 0o600); err == nil {
		t.Fatal("a missing directory should fail")
	}

	// A directory with something in it cannot be replaced by a file.
	path := filepath.Join(t.TempDir(), "file.json")
	os.MkdirAll(filepath.Join(path, "sub"), 0o700)
	if err := Write(path, []byte("new"), 0o600); err == nil {
		t.Fatal("a directory in the place of the file should fail")
	}
	if left := leftovers(t, path); len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

// A write the file system refuses part way fails, and the target keeps its
// old content. The limit on the size of files makes the write fail.
func TestWriteThatFailsKeepsTheOldContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.json")
	os.WriteFile(path, []byte("old"), 0o600)
	signal.Ignore(syscall.SIGXFSZ) // the write fails instead of ending the process
	defer signal.Reset(syscall.SIGXFSZ)
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	small := limit
	small.Cur = 16
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &small); err != nil {
		t.Skip("cannot limit the size of files:", err)
	}
	err := Write(path, make([]byte, 1024), 0o600)
	syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit)
	if err == nil {
		t.Fatal("a write beyond the limit should fail")
	}
	if b, _ := os.ReadFile(path); string(b) != "old" {
		t.Fatalf("content = %q", b)
	}
	if left := leftovers(t, path); len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

// The directory is synced once the file has its new name, and not after a
// write that failed: then there is no new name to keep.
func TestWriteSyncsTheDirectory(t *testing.T) {
	var synced []string
	defer func(sync func(string)) { syncDir = sync }(syncDir)
	syncDir = func(dir string) { synced = append(synced, dir) }
	dir := t.TempDir()
	if err := Write(filepath.Join(dir, "file.json"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(synced) != 1 || synced[0] != dir {
		t.Fatalf("synced %q", synced)
	}
	synced = nil
	path := filepath.Join(dir, "taken")
	os.MkdirAll(filepath.Join(path, "sub"), 0o700)
	if err := Write(path, []byte("new"), 0o600); err == nil || synced != nil {
		t.Fatalf("a write that failed: %v, synced %q", err, synced)
	}
}

func TestSyncDirIgnoresMissingDirectory(t *testing.T) {
	syncDir(filepath.Join(t.TempDir(), "missing"))
}

// Syncing a directory closes it again.
func TestSyncDirClosesTheDirectory(t *testing.T) {
	open := func() int {
		fds, err := os.ReadDir("/dev/fd")
		if err != nil {
			t.Skip("cannot list the open files:", err)
		}
		return len(fds)
	}
	dir := t.TempDir()
	before := open()
	for range 10 {
		syncDir(dir)
	}
	if after := open(); after != before {
		t.Fatalf("%d files open before, %d after", before, after)
	}
}

// Only one holder has the lock at a time, also within one process.
func TestLockExcludes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.lock")
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		again, err := Lock(path)
		if err != nil {
			t.Error(err)
		} else {
			again()
		}
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("the lock was given twice")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("the lock was not released")
	}
}

// Changes made under the lock do not overwrite each other.
func TestLockSerializesReadModifyWrite(t *testing.T) {
	dir := t.TempDir()
	path, lock := filepath.Join(dir, "count"), filepath.Join(dir, "count.lock")
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := Lock(lock)
			if err != nil {
				t.Error(err)
				return
			}
			defer unlock()
			b, _ := os.ReadFile(path)
			if err := Write(path, append(b, 'x'), 0o600); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if b, _ := os.ReadFile(path); len(b) != 20 {
		t.Fatalf("%d of 20 changes survived", len(b))
	}
}

func TestLockReportsErrors(t *testing.T) {
	if _, err := Lock(filepath.Join(t.TempDir(), "missing", "file.lock")); err == nil {
		t.Fatal("a missing directory should fail")
	}

	refused := errors.New("no lock")
	flock = func(int, int) error { return refused }
	defer func() { flock = syscall.Flock }()
	if _, err := Lock(filepath.Join(t.TempDir(), "file.lock")); !errors.Is(err, refused) {
		t.Fatalf("got %v", err)
	}
}
