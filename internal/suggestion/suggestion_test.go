package suggestion

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What is added waits until it is removed, also for another Store of the
// same file, and the same text is one suggestion.
func TestAddListRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suggestions.json")
	s := Open(path)
	if list, err := s.List(); err != nil || len(list) != 0 {
		t.Fatalf("empty: %v, %v", list, err)
	}
	a, err := s.Add("let bash run long commands as jobs", []string{"bash took 42s"}, "s1")
	if err != nil || a.ID == "" || a.Session != "s1" || a.Signals[0] != "bash took 42s" {
		t.Fatalf("added %+v, %v", a, err)
	}
	if again, err := s.Add("let bash run long commands as jobs", nil, "s2"); err != nil || again.ID != a.ID {
		t.Fatalf("the same again: %+v, %v", again, err)
	}
	b, _ := s.Add("say which files are near", nil, "s2")
	list, err := Open(path).List()
	if err != nil || len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("list %+v, %v", list, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode())
	}
	if got, err := s.Remove(a.ID); err != nil || got.Text != a.Text {
		t.Fatalf("removed %+v, %v", got, err)
	}
	if _, err := s.Remove(a.ID); !errors.Is(err, ErrGone) {
		t.Fatalf("removed twice: %v", err)
	}
	if list, _ := s.List(); len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("left %+v", list)
	}
}

// No more than Max wait, and a text is cut to MaxText.
func TestBounds(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "suggestions.json"))
	for i := range Max {
		if _, err := s.Add(fmt.Sprint(i), nil, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Add("one more", nil, ""); !errors.Is(err, ErrFull) {
		t.Fatalf("one more: %v", err)
	}
	s = Open(filepath.Join(t.TempDir(), "suggestions.json"))
	if n, _ := s.Add(strings.Repeat("a", MaxText+1), nil, ""); len(n.Text) != MaxText {
		t.Fatalf("kept %d bytes", len(n.Text))
	}
}

// A list that cannot be read is no empty one, and is not written over; an
// empty file is an empty list; without a lock nothing is read.
func TestErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "suggestions.json")
	os.WriteFile(path, []byte("nope"), 0o600)
	s := Open(path)
	if _, err := s.List(); err == nil {
		t.Error("List of a broken file")
	}
	if _, err := s.Add("x", nil, ""); err == nil {
		t.Error("Add to a broken file")
	}
	if b, _ := os.ReadFile(path); string(b) != "nope" {
		t.Errorf("a broken file was written over: %q", b)
	}
	os.WriteFile(path, nil, 0o600)
	if list, err := s.List(); err != nil || len(list) != 0 {
		t.Errorf("an empty file: %v, %v", list, err)
	}
	os.Mkdir(filepath.Join(dir, "dir.json"), 0o700)
	if _, err := Open(filepath.Join(dir, "dir.json")).List(); err == nil {
		t.Error("List of a directory")
	}
	os.Mkdir(filepath.Join(dir, "other.json.lock"), 0o700)
	if _, err := Open(filepath.Join(dir, "other.json")).List(); err == nil {
		t.Error("List without a lock")
	}
}
