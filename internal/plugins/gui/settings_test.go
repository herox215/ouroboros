package gui

import (
	"strings"
	"testing"
)

// The window of the thread is read and set for the selected profile; one
// that is no positive duration is refused, as are an index the window has no
// profile for and a profile without its owner.
func TestWindowThreadWindow(t *testing.T) {
	w, _ := memoryWindow(t)
	if got, err := w.ThreadWindow(0); err != nil || got != "3h" {
		t.Fatalf("window %q, %v", got, err)
	}
	if holds, err := w.SetThreadWindow(0, "90m"); err != nil || holds == "" {
		t.Fatalf("set %q, %v", holds, err)
	}
	if got, err := w.ThreadWindow(0); err != nil || got != "90m" {
		t.Fatalf("window after set %q, %v", got, err)
	}
	if _, err := w.SetThreadWindow(0, "0"); err == nil {
		t.Fatal("a window of zero was set")
	}
	invalid := "invalid GUI profile index"
	for _, profile := range []int{-1, 1} {
		if _, err := w.ThreadWindow(profile); err == nil || !strings.Contains(err.Error(), invalid) {
			t.Fatalf("ThreadWindow(%d): %v", profile, err)
		}
		if _, err := w.SetThreadWindow(profile, "1h"); err == nil || !strings.Contains(err.Error(), invalid) {
			t.Fatalf("SetThreadWindow(%d): %v", profile, err)
		}
	}
	noOwner := newWindow(page{choices: []choice{{"none", ""}}, profiles: []*Profile{{}}})
	t.Cleanup(noOwner.Close)
	if _, err := noOwner.ThreadWindow(0); err == nil || !strings.Contains(err.Error(), "has no owner access") {
		t.Fatalf("ThreadWindow without owner: %v", err)
	}
	if _, err := noOwner.SetThreadWindow(0, "1h"); err == nil || !strings.Contains(err.Error(), "has no owner access") {
		t.Fatalf("SetThreadWindow without owner: %v", err)
	}
}
