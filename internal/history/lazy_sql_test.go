package history

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSQLMissingArchiveReads(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "profile", "archive")
	a := Open(dir)
	defer a.Close()
	if list, err := a.List(); err != nil || len(list) != 0 {
		t.Fatalf("list: %v %v", list, err)
	}
	if hits, err := a.Match("needle"); err != nil || len(hits) != 0 {
		t.Fatalf("match: %v %v", hits, err)
	}
	if _, n, err := a.Search(Who{}, "needle", true, 0); err != nil || n != 0 {
		t.Fatalf("search: %d %v", n, err)
	}
	if _, n, err := a.Sessions(Who{}); err != nil || n != 0 {
		t.Fatalf("sessions: %d %v", n, err)
	}
	if title, err := a.Title("owner/missing"); err != nil || title != "" {
		t.Fatalf("title: %q %v", title, err)
	}
	if value, ok, err := a.Effort("owner/missing"); err != nil || ok || value != "" {
		t.Fatalf("effort: %q %v %v", value, ok, err)
	}
	if value, ok, err := a.Directory("owner/missing"); err != nil || ok || value != "" {
		t.Fatalf("directory: %q %v %v", value, ok, err)
	}
	if _, err := a.Records("owner/missing"); !errors.Is(err, ErrNotArchived) {
		t.Fatalf("records: %v", err)
	}
	if _, err := a.Page("owner/missing", 0); !errors.Is(err, ErrNotArchived) {
		t.Fatalf("page: %v", err)
	}
	if _, _, _, err := a.ToolResult("owner/missing", "call"); !errors.Is(err, ErrNotArchived) {
		t.Fatalf("tool: %v", err)
	}
	if _, err := a.Read(Who{}, "missing", 0, 0, 0); err == nil {
		t.Fatal("read missing session succeeded")
	}
	if err := a.Delete("owner/missing"); !errors.Is(err, ErrNotArchived) {
		t.Fatalf("delete: %v", err)
	}
	if ids, err := a.GoingOn(); err != nil || len(ids) != 0 {
		t.Fatalf("going on: %v %v", ids, err)
	}
	if _, err := os.Stat(filepath.Join(root, "profile")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reads created profile: %v", err)
	}
	// A missing read must not cache absence: another handle may create it.
	b := Open(dir)
	defer b.Close()
	if err := b.SetDirectory("owner/draft", ""); err != nil {
		t.Fatal(err)
	}
	if list, err := a.List(); err != nil || len(list) != 1 || list[0].ID != "owner/draft" {
		t.Fatalf("created after read: %v %v", list, err)
	}
}

func TestSQLBrokenArchiveReads(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "absent"), link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(file, "archive"), link, filepath.Join(link, "archive")} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			a := Open(path)
			defer a.Close()
			if _, err := a.List(); err == nil {
				t.Fatal("broken directory hidden by list")
			}
			if _, _, err := a.Directory("owner/draft"); err == nil {
				t.Fatal("broken directory hidden by metadata read")
			}
			if _, err := a.Records("owner/draft"); err == nil || errors.Is(err, ErrNotArchived) {
				t.Fatalf("broken directory hidden by records: %v", err)
			}
		})
	}
}

func TestSQLMetadataOnlyListRestart(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	if err := a.SetEffort("owner/draft", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = Open(dir)
	defer a.Close()
	if list, err := a.List(); err != nil || len(list) != 1 || list[0].ID != "owner/draft" || list[0].Size != 0 {
		t.Fatalf("metadata list: %v %v", list, err)
	}
	if _, n, err := a.Sessions(Who{}); err != nil || n != 0 {
		t.Fatalf("transcript listing: %d %v", n, err)
	}
	if hits, err := a.Match("draft"); err != nil || len(hits) != 0 {
		t.Fatalf("metadata search: %v %v", hits, err)
	}
	if err := a.Delete("owner/draft"); !errors.Is(err, ErrNotArchived) {
		t.Fatal(err)
	}
	if list, err := a.List(); err != nil || len(list) != 0 {
		t.Fatalf("deleted metadata: %v %v", list, err)
	}
}

func TestSQLConcurrentGoingOn(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	defer a.Close()
	if err := a.GoOn("owner/draft"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan []string, 4)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := Open(dir)
			defer b.Close()
			ids, err := b.GoingOn()
			results <- ids
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	var ids []string
	for got := range results {
		ids = append(ids, got...)
	}
	if len(ids) != 1 || ids[0] != "owner/draft" {
		t.Fatalf("marks not consumed exactly once: %v", ids)
	}
}
