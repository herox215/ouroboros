package cli

import (
	"reflect"
	"testing"

	"ouroboros/internal/home"
)

// The GUI shows the node's profiles, so opening it needs no profile even
// when other commands do.
func TestGUIProfileRouting(t *testing.T) {
	first, other := &App{}, &App{}
	first.profiles = map[home.Dir]*App{"first": first, "other": other}
	for _, args := range [][]string{{"gui"}, {"gui", "--help"}} {
		app, rest, err := first.forProfile(args)
		if err != nil || app != first || !reflect.DeepEqual(rest, args) {
			t.Fatalf("%v: app %p, args %v, %v", args, app, rest, err)
		}
	}
	if _, _, err := first.forProfile([]string{"chat", "hello"}); err == nil {
		t.Fatal("a profile command without a profile should fail")
	}
}
