package history

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A conversation of the owner marked to go on is returned once, and the
// mark goes with the conversation when it is deleted.
func TestGoOn(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	if err := a.GoOn("nowhere"); err == nil {
		t.Error("a mark of no session")
	}
	for _, id := range []string{"owner/s1", "owner/s2"} {
		if err := a.GoOn(id); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := a.GoingOn(); err != nil || !slices.Equal(got, []string{"owner/s1", "owner/s2"}) {
		t.Fatalf("going on: %v, %v", got, err)
	}
	if got, err := a.GoingOn(); err != nil || len(got) != 0 {
		t.Fatalf("again: %v, %v", got, err)
	}

	a.GoOn("owner/s3")
	os.WriteFile(filepath.Join(dir, "owner", "s3.jsonl"), nil, 0o600)
	if err := a.Delete("owner/s3"); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.GoingOn(); len(got) != 0 {
		t.Fatalf("a deleted session goes on: %v", got)
	}

	// A mark that cannot be taken away is an error, and it is not returned
	// as gone.
	a.GoOn("owner/s4")
	os.Chmod(filepath.Join(dir, "owner"), 0o500)
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "owner"), 0o700) })
	if got, err := a.GoingOn(); err == nil || len(got) != 0 {
		t.Fatalf("a mark that stays: %v, %v", got, err)
	}
}
