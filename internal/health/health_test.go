package health

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// What the owner reads of a record: never taken, green, or what failed.
func TestLine(t *testing.T) {
	var never *Record
	for _, tt := range []struct {
		name string
		r    *Record
		want string
	}{
		{"never taken", never, "not measured"},
		{"not run", &Record{}, "not measured"},
		{"green", &Record{Ran: true, OK: true}, "green"},
		{"failing without a name", &Record{Ran: true}, "failing"},
		{"failing", &Record{Ran: true, Fail: "./internal/tui/: TestFooterFitsTheWindow"}, "failing: ./internal/tui/: TestFooterFitsTheWindow"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.Line(); got != tt.want {
				t.Errorf("Line() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A record is kept where the node finds it again, with the packages that ran
// and the time of the run.
func TestWriteAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile", "health.json")
	want := Record{Ran: true, OK: true, Tests: []string{"./internal/tui/", "./internal/health/"},
		At: time.Date(2026, 10, 30, 12, 34, 56, 0, time.UTC)}
	if err := Write(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok := Read(path)
	if !ok {
		t.Fatal("a record that was written is not read")
	}
	if !got.Ran || !got.OK || !slices.Equal(got.Tests, want.Tests) || !got.At.Equal(want.At) {
		t.Errorf("read %+v, want %+v", got, want)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the record is not the owner's alone: %v, %v", info, err)
	}
}

// A record that is not there, cannot be read, or says that no run was taken
// is no reading at all, never a failure.
func TestReadWithoutARecord(t *testing.T) {
	dir := t.TempDir()
	if _, ok := Read(filepath.Join(dir, "health.json")); ok {
		t.Error("a record that is not there was read")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Read(bad); ok {
		t.Error("a record that cannot be read was read")
	}
	notRun := filepath.Join(dir, "not-run.json")
	if err := os.WriteFile(notRun, []byte(`{"ran":false,"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Read(notRun); ok {
		t.Error("a record that says no run was taken was read")
	}
}
