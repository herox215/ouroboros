package memory

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestIndexFailureRollsBackMutation(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e, err := s.Create(ctx, "", "drink", "oolong", "owner")
	if err != nil {
		t.Fatal(err)
	}
	// Fail after the FTS delete but before replacement content can be inserted.
	if _, err := s.db.Exec(`CREATE TRIGGER fail_index BEFORE INSERT ON memory_fts_data BEGIN SELECT RAISE(ABORT,'index failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, "", e.ID, 1, "drink", "espresso", "owner"); err == nil {
		t.Fatal("index failure accepted")
	}
	got, err := s.Read(ctx, "", e.ID)
	if err != nil || got != e {
		t.Fatalf("row not rolled back: %+v %v", got, err)
	}
	// Remove the fault before FTS reads (its writes may be deferred until commit).
	if _, err := s.db.Exec("DROP TRIGGER fail_index"); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search(ctx, "", "all", "oolong", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("original index not restored: %+v %v", hits, err)
	}
	hits, err = s.Search(ctx, "", "all", "espresso", 10)
	if err != nil || len(hits) != 0 {
		t.Fatalf("failed content indexed: %+v %v", hits, err)
	}
}

func TestConcurrentVersionAcrossPools(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ctx := context.Background()
	e, err := a.Create(ctx, "", "drink", "original", "owner")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, s := range []*Store{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Update(ctx, "", e.ID, 1, "drink", []string{"oolong", "espresso"}[i], "owner")
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
	got, err := a.Read(ctx, "", e.ID)
	if err != nil || got.Version != 2 {
		t.Fatalf("concurrent update: %+v %v", got, err)
	}
	for _, token := range []string{"original", "oolong", "espresso"} {
		hits, err := b.Search(ctx, "", "global", token, 10)
		want := 0
		if got.Content == token {
			want = 1
		}
		if err != nil || len(hits) != want {
			t.Fatalf("index %s: %+v %v", token, hits, err)
		}
	}
}

func TestRejectNULText(t *testing.T) {
	s := testStore(t)
	for _, pair := range [][2]string{{"hidden\x00key", "content"}, {"key", "hidden\x00content"}} {
		if _, err := s.Create(context.Background(), "", pair[0], pair[1], "owner"); err == nil {
			t.Fatal("NUL text accepted; SQLite excerpts would truncate it")
		}
	}
}
