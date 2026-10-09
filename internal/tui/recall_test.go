package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// keys presses each key and checks what the input holds after it.
func keys(t *testing.T, m *Model, steps ...[2]string) {
	t.Helper()
	for i, s := range steps {
		press(m, s[0])
		if got := m.input.Value(); got != s[1] {
			t.Fatalf("step %d: %s left %q in the input, want %q", i, s[0], got, s[1])
		}
	}
}

// Up brings back what was entered, messages and commands alike, the latest
// first. Down goes the other way, to what was typed, and below that to an
// empty input, from which up brings it back again.
func TestUpAndDownBringBackWhatWasEntered(t *testing.T) {
	m, _ := newModel(t)
	keys(t, m, [2]string{"up", ""}, [2]string{"down", ""}) // nothing was entered yet
	say(t, m, "hallo")
	if run(m, "/effort"); m.screen != screenEffort {
		t.Fatalf("/effort opens its screen, not %v", m.screen)
	}
	press(m, "esc")
	say(t, m, "hallo")

	// With nothing typed, there is nothing below it.
	keys(t, m, [2]string{"down", ""}, [2]string{"up", "hallo"}, [2]string{"down", ""}, [2]string{"down", ""})

	m.input.SetValue("abc")
	keys(t, m,
		[2]string{"up", "hallo"},
		[2]string{"up", "/effort"},
		[2]string{"up", "hallo"},
		[2]string{"up", "hallo"}, // nothing older
		[2]string{"down", "/effort"},
		[2]string{"down", "hallo"},
		[2]string{"down", "abc"},
		[2]string{"down", ""},
		[2]string{"down", ""}, // nothing below
		[2]string{"up", "abc"},
		[2]string{"up", "hallo"},
		[2]string{"down", "abc"},
	)
	if m.screen != screenChat {
		t.Fatal("bringing back a command does not run it")
	}
}

// The same line twice in a row is kept once. A line brought back and changed
// is what is typed from then on, and what is entered starts up and down
// anew from the latest.
func TestChangedLinesAreTyped(t *testing.T) {
	m, _ := newModel(t)
	say(t, m, "a")
	say(t, m, "b")
	say(t, m, "b")
	keys(t, m, [2]string{"up", "b"}, [2]string{"up", "a"}, [2]string{"up", "a"})
	keys(t, m, [2]string{"x", "ax"}, [2]string{"up", "b"}, [2]string{"down", "ax"})

	// A command the UI does not know stays in the input, to be corrected.
	run(m, "/nope")
	m.input.Reset()
	say(t, m, "c")
	keys(t, m, [2]string{"up", "c"}, [2]string{"up", "/nope"})
	press(m, "enter") // unknown: it stays
	if !strings.Contains(m.notice, "unknown command") {
		t.Fatalf("notice %q", m.notice)
	}
	keys(t, m, [2]string{"up", "/nope"}, [2]string{"up", "c"}, [2]string{"up", "/nope"}, [2]string{"down", "c"})
}

// In an input of several lines, up and down move between them first, also
// between the lines a long one wraps into; at its first line up, and at
// its last down, bring back what was entered.
func TestUpAndDownMoveBetweenLinesFirst(t *testing.T) {
	m, _ := newModel(t)
	long := strings.Repeat("word ", 60)
	say(t, m, "first")
	say(t, m, "one\ntwo")
	say(t, m, long)

	// Brought back, the cursor is at the end of the line: up goes through
	// what it wraps into before it brings back the one before.
	press(m, "up")
	ups := 0
	for m.input.Value() == strings.TrimSpace(long) {
		press(m, "up")
		ups++
	}
	if ups < 2 || m.input.Value() != "one\ntwo" {
		t.Fatalf("after %d times up: %q", ups, m.input.Value())
	}
	keys(t, m, [2]string{"up", "one\ntwo"}, [2]string{"up", "first"}, [2]string{"down", "one\ntwo"}, [2]string{"down", strings.TrimSpace(long)})

	// What is typed keeps the cursor moving down to its last line.
	m.input.SetValue("x\ny")
	keys(t, m, [2]string{"up", "x\ny"})
	if m.input.Line() != 0 {
		t.Fatal("up moves to the first line")
	}
	keys(t, m, [2]string{"down", "x\ny"}, [2]string{"down", ""}, [2]string{"up", "x\ny"})
	m.input.SetValue(long)
	m.input.MoveToBegin()
	downs := 0
	for m.input.Value() != "" {
		press(m, "down")
		downs++
	}
	if downs < 2 {
		t.Fatalf("down went below what was typed at once: %d", downs)
	}
}

// While a command is typed, up and down select one of those that fit; a
// command that was brought back is no command typed.
func TestBroughtBackCommandsAreNoSuggestions(t *testing.T) {
	m, _ := newModel(t)
	run(m, "/help")
	press(m, "esc")
	say(t, m, "hallo")
	keys(t, m, [2]string{"up", "hallo"}, [2]string{"up", "/help"}, [2]string{"down", "hallo"})

	m.input.SetValue("/s")
	keys(t, m, [2]string{"down", "/s"})
	if found, selected := m.suggestions(); len(found) < 2 || selected != 1 {
		t.Fatalf("down selects the next command: %d of %d", selected, len(found))
	}
	keys(t, m, [2]string{"x", "/sx"}) // no command fits
	keys(t, m, [2]string{"up", "hallo"})
}

// A message brought back has the pictures it had, also in another
// conversation, where they get labels of its own. A path in quotes that is
// no picture stays as it was typed.
func TestBroughtBackPictures(t *testing.T) {
	dir := files(t, "cat.png", "dog.png")
	cat, dog := filepath.Join(dir, "cat.png"), filepath.Join(dir, "dog.png")
	m, _ := newModel(t)
	m.Update(tea.PasteMsg{Content: cat})
	m.input.InsertString("look")
	press(m, "enter")
	say(t, m, `read "/no/such.png"`)
	keys(t, m, [2]string{"up", `read "/no/such.png"`}, [2]string{"up", "[Image_1] look"})

	m.input.Reset()
	run(m, "/new")
	m.Update(tea.PasteMsg{Content: dog})
	press(m, "enter")
	keys(t, m,
		[2]string{"up", "[Image_1]"},
		[2]string{"up", "/new"},
		[2]string{"up", `read "/no/such.png"`},
		[2]string{"up", "[Image_2] look"},
	)
	if got, want := m.chat.expand(m.input.Value()), quote(cat)+" look"; got != want {
		t.Fatalf("sent as %q, want %q", got, want)
	}
}
