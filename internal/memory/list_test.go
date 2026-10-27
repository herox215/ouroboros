package memory

import (
	"context"
	"testing"
	"time"
)

// List lists the memories a dialog shows: the global ones and those of the
// project shown, never those of another project, the core ones among them,
// and leaves the archived and expired ones out unless they are asked for.
func TestList(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.CreateCore(ctx, "workflow", "changes go in a branch", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "", "drink", "oolong tea", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "/p", "build", "make test runs the tests", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, "/other", "elsewhere", "of another project", "s1"); err != nil {
		t.Fatal(err)
	}

	list, err := s.List(ctx, "/p", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 3 || list.Hidden != 0 || len(list.Memories) != 3 {
		t.Fatalf("the project: %+v", list)
	}
	for _, e := range list.Memories {
		if e.Key == "elsewhere" {
			t.Fatal("a memory of another project is listed")
		}
	}
	if list, err = s.List(ctx, "", false, 0); err != nil || list.Total != 2 || len(list.Memories) != 2 {
		t.Fatalf("no project: %+v %v", list, err)
	}
	if list, err = s.List(ctx, "/other", false, 0); err != nil || list.Total != 3 {
		t.Fatalf("the other project: %+v %v", list, err)
	}

	// An archived memory is left out, and counted, unless it is asked for.
	build, err := s.Search(ctx, "/p", "project", "build", 1)
	if err != nil || len(build) != 1 {
		t.Fatalf("build: %+v %v", build, err)
	}
	if err = s.Archive(ctx, "/p", []string{build[0].ID}); err != nil {
		t.Fatal(err)
	}
	list, err = s.List(ctx, "/p", false, 0)
	if err != nil || list.Total != 3 || list.Hidden != 1 || len(list.Memories) != 2 {
		t.Fatalf("archived left out: %+v %v", list, err)
	}
	list, err = s.List(ctx, "/p", true, 0)
	if err != nil || list.Total != 3 || list.Hidden != 0 || len(list.Memories) != 3 {
		t.Fatalf("archived asked for: %+v %v", list, err)
	}

	// An expired one is left out as well, until it is asked for.
	if _, _, err = s.Remember(ctx, "", "mood", "tired", "s1", false, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	list, err = s.List(ctx, "", false, 0)
	if err != nil || list.Hidden != 1 || len(list.Memories) != 2 {
		t.Fatalf("expired left out: %+v %v", list, err)
	}
	list, err = s.List(ctx, "", true, 0)
	if err != nil || len(list.Memories) != 3 || list.Hidden != 0 {
		t.Fatalf("expired asked for: %+v %v", list, err)
	}

	// The listing is capped, but the count says how many there are.
	if list, err = s.List(ctx, "/p", false, 2); err != nil || len(list.Memories) != 2 || list.Total != 4 {
		t.Fatalf("capped: %+v %v", list, err)
	}
}
