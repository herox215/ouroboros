package history

import (
	"errors"
	"testing"
)

func TestSQLMetadataOnlyDelete(t *testing.T) {
	a := Open(t.TempDir())
	defer a.Close()
	if err := a.SetTitle("owner/draft", "Draft"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetDirectory("owner/draft", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.GoOn("owner/draft"); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete("owner/draft"); !errors.Is(err, ErrNotArchived) {
		t.Fatalf("metadata-only delete: %v", err)
	}
	if title, err := a.Title("owner/draft"); err != nil || title != "" {
		t.Fatalf("title: %q %v", title, err)
	}
	if _, ok, err := a.Directory("owner/draft"); err != nil || ok {
		t.Fatalf("directory: %v %v", ok, err)
	}
	if ids, err := a.GoingOn(); err != nil || len(ids) != 0 {
		t.Fatalf("goon: %v %v", ids, err)
	}
}

func TestSQLMetadataWriteFailure(t *testing.T) {
	a := Open(t.TempDir())
	defer a.Close()
	if err := a.SetTitle("owner/draft", "Before"); err != nil {
		t.Fatal(err)
	}
	db, err := a.database()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TRIGGER keep_title BEFORE UPDATE OF title ON history_sessions BEGIN SELECT RAISE(ABORT,'broken'); END"); err != nil {
		t.Fatal(err)
	}
	if err = a.SetTitle("owner/draft", "After"); err == nil {
		t.Fatal("write succeeded")
	}
	if title, err := a.Title("owner/draft"); err != nil || title != "Before" {
		t.Fatalf("title after failure: %q %v", title, err)
	}
}
