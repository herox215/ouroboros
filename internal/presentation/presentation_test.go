package presentation

import (
	"context"
	"sync"
	"testing"
)

func TestTrace(t *testing.T) {
	base := context.Background()
	if Current(base) != "" || Terminal(base) != "" {
		t.Fatal("context without trace has an identity")
	}
	ctx := Ensure(base)
	terminal := Terminal(ctx)
	if terminal == "" || Current(ctx) != "" || Ensure(ctx) != ctx {
		t.Fatal("Ensure did not initialize or preserve trace")
	}
	first, second := Next(ctx), Next(ctx)
	if first == "" || second == "" || first == second || first == terminal || second == terminal || Current(ctx) != second {
		t.Fatal("Next did not advance to a unique identity")
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if Current(child) != second || Terminal(child) != terminal {
		t.Fatal("derived context lost trace")
	}
	fresh := New(ctx)
	if Current(fresh) != "" || Terminal(fresh) == "" || Terminal(fresh) == terminal || Current(ctx) != second {
		t.Fatal("New did not isolate trace")
	}
	if Terminal(ctx) != terminal {
		t.Fatal("terminal identity changed")
	}
}

func TestConcurrentTrace(t *testing.T) {
	ctx := New(context.Background())
	terminal := Terminal(ctx)
	ids := make(chan string, 200)
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			ids <- Next(ctx)
			ids <- NewID()
			_ = Current(ctx)
			if Terminal(Ensure(ctx)) != terminal {
				t.Error("terminal identity changed")
			}
		})
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{terminal: true}
	for id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("empty or duplicate identity %q", id)
		}
		seen[id] = true
	}
	if current := Current(ctx); current == "" || current == terminal || !seen[current] {
		t.Fatalf("current identity %q not generated", current)
	}
}
