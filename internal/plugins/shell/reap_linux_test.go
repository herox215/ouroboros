package shell

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The table of the processes is read from what the kernel shows of each:
// the fields after the name, which may hold spaces and parentheses itself.
// What cannot be read, or is cut short, is left out.
func TestProcsIn(t *testing.T) {
	root := t.TempDir()
	// The state is the first field after the name, the start the 20th.
	tail := func(state, ppid, pgid, start string) string {
		return " " + state + " " + ppid + " " + pgid + strings.Repeat(" 0", 16) + " " + start + " 0 0\n"
	}
	for pid, stat := range map[string]string{
		"100": "100 (a) b) (c)" + tail("S", "1", "100", "4711"),
		"101": "101 (dead)" + tail("Z", "100", "100", "4712"),
		"102": "102 no name" + tail("S", "1", "102", "4713"),
		"103": "103 (short) S 1 103",
		"104": "",
	} {
		if err := os.MkdirAll(filepath.Join(root, pid), 0o755); err != nil {
			t.Fatal(err)
		}
		if stat != "" {
			os.WriteFile(filepath.Join(root, pid, "stat"), []byte(stat), 0o644)
		}
	}
	os.Mkdir(filepath.Join(root, "self"), 0o755)
	got := procsIn(root)
	slices.SortFunc(got, func(a, b proc) int { return a.pid - b.pid })
	want := []proc{{100, 1, 100, 4711, false}, {101, 100, 100, 4712, true}}
	if !slices.Equal(got, want) {
		t.Errorf("procs:\n%+v\nwant\n%+v", got, want)
	}
	if got := procsIn(filepath.Join(root, "missing")); len(got) != 0 {
		t.Errorf("without a table: %+v", got)
	}
}

// The owner of a file is known from what the system tells of it, and is
// nobody where it tells nothing.
func TestOwnerOf(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := ownerOf(info); got != os.Getuid() {
		t.Errorf("owner %d, want %d", got, os.Getuid())
	}
	if got := ownerOf(bareInfo{}); got != -1 {
		t.Errorf("owner of a file the system tells nothing of: %d", got)
	}
}

// bareInfo is a file the system tells nothing of.
type bareInfo struct{}

func (bareInfo) Name() string       { return "bare" }
func (bareInfo) Size() int64        { return 0 }
func (bareInfo) Mode() os.FileMode  { return 0 }
func (bareInfo) ModTime() time.Time { return time.Time{} }
func (bareInfo) IsDir() bool        { return false }
func (bareInfo) Sys() any           { return nil }
