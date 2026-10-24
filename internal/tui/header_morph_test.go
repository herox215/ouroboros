package tui

import (
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestMorphNameKeepsGraphemesAndEasesWidth(t *testing.T) {
	for _, names := range [][2]string{
		{"alpha", "Luna"}, {"a", "Long profile"}, {"Long profile", "a"},
		{"", "🙂"}, {"🙂", ""}, {"🧑‍💻e\u0301", "南月"},
		{"東京", "👨‍👩‍👧‍👦 Lua"}, {"e\u0301", "e\u0302"}, {"same", "same"},
	} {
		t.Run(names[0]+" to "+names[1], func(t *testing.T) {
			from, to := names[0], names[1]
			for _, at := range []time.Duration{-time.Second, 0} {
				if got := morphName(from, to, at); got != from {
					t.Fatalf("before the morph: %q", got)
				}
			}
			for _, at := range []time.Duration{headerMorphDuration, time.Second} {
				if got := morphName(from, to, at); got != to {
					t.Fatalf("after the morph: %q", got)
				}
			}
			allowed := append(nameGraphemes(from), nameGraphemes(to)...)
			allowed = append(allowed, headerMorphMarks[:]...)
			allowed = append(allowed, " ")
			start, end := ansi.StringWidth(from), ansi.StringWidth(to)
			previous := start
			for at := time.Duration(0); at <= headerMorphDuration; at += headerMorphFrame / 2 {
				got := morphName(from, to, at)
				if !utf8.ValidString(got) || got != morphName(from, to, at) {
					t.Fatalf("an invalid or random frame at %s: %q", at, got)
				}
				for _, letter := range nameGraphemes(got) {
					if !slices.Contains(allowed, letter) {
						t.Fatalf("a grapheme split at %s: %q in %q", at, letter, got)
					}
				}
				width := ansi.StringWidth(got)
				if width < min(start, end) || width > max(start, end) ||
					end >= start && width < previous || end <= start && width > previous {
					t.Fatalf("the width jumps backward at %s: %d -> %d", at, previous, width)
				}
				previous = width
			}
			if width := ansi.StringWidth(morphName(from, to, headerMorphDuration/2)); width != (start+end+1)/2 {
				t.Fatalf("the width halfway through is %d, from %d to %d", width, start, end)
			}
		})
	}
	if got := nameGraphemes("e\u0301👨‍👩‍👧‍👦日本"); !slices.Equal(got, []string{"e\u0301", "👨‍👩‍👧‍👦", "日", "本"}) {
		t.Fatalf("not whole letters: %q", got)
	}
}

func TestMorphNameIsAWaveAndKeepsSharedLetters(t *testing.T) {
	first := morphName("ABCD", "WXYZ", headerMorphDuration/4)
	last := morphName("ABCD", "WXYZ", headerMorphDuration*3/4)
	if first == "ABCD" || !strings.HasSuffix(first, "CD") {
		t.Fatalf("the wave should begin on the left: %q", first)
	}
	if !strings.HasPrefix(last, "WX") || last == "WXYZ" {
		t.Fatalf("the left should settle before the right: %q", last)
	}
	if got := morphName("alpha", "alXha", headerMorphDuration/2); !strings.HasPrefix(got, "al") || !strings.HasSuffix(got, "ha") || got == "alpha" || got == "alXha" {
		t.Fatalf("only the changed letter should dissolve: %q", got)
	}
}

func TestHeaderMorphTicksStopAndIgnoreOldFrames(t *testing.T) {
	at := time.Unix(123, 0)
	var a headerMorph
	if a.begin("same", "same", at) != nil || a.active || a.name("same") != "same" {
		t.Fatal("equal names need no frames")
	}
	cmd := a.begin("Terra", "Luna", at)
	if cmd == nil || !a.active || a.name("Luna") != "Terra" {
		t.Fatal("the first frame should keep the old name")
	}
	msg, ok := cmd().(headerMorphMsg)
	if !ok || msg.generation != a.generation || msg.at.IsZero() {
		t.Fatalf("the tick lost its generation or time: %+v", msg)
	}
	if a.update(headerMorphMsg{generation: a.generation - 1, at: at.Add(time.Second)}) != nil || a.elapsed != 0 {
		t.Fatal("an old generation changed the morph")
	}
	if a.update(headerMorphMsg{generation: a.generation, at: at.Add(-time.Second)}) == nil || a.elapsed != 0 {
		t.Fatal("a frame before the start moved the morph backward")
	}
	if a.update(headerMorphMsg{generation: a.generation, at: at.Add(headerMorphDuration / 2)}) == nil || a.elapsed != headerMorphDuration/2 {
		t.Fatal("a frame did not advance and request the next one")
	}
	if a.update(headerMorphMsg{generation: a.generation, at: at.Add(headerMorphDuration / 4)}) == nil || a.elapsed != headerMorphDuration/2 {
		t.Fatal("a late frame moved the morph backward")
	}
	if a.name("renamed") != "renamed" {
		t.Fatal("a status rename was hidden by an old target")
	}
	if a.update(headerMorphMsg{generation: a.generation, at: at.Add(headerMorphDuration)}) != nil || a.active || a.name("Luna") != "Luna" {
		t.Fatal("the final frame should stop and show the new name")
	}
	if a.update(headerMorphMsg{generation: a.generation, at: at.Add(time.Second)}) != nil {
		t.Fatal("an ended morph started ticking again")
	}
	a.begin("Terra", "Luna", at)
	generation := a.generation
	if a.begin("Terra", "Terra", at) != nil || a.active || a.generation == generation {
		t.Fatal("a switch to the name shown must cancel pending frames")
	}
}

func TestProfileSwitchMorphsOnlyTheHeaderName(t *testing.T) {
	root, terra, luna, _ := profiles(t)
	at := time.Unix(123, 0)
	terra.now, luna.now = func() time.Time { return at }, func() time.Time { return at }
	root.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	luna.input.SetValue("Luna draft")
	_, cmd := root.Update(shiftTab)
	a := &root.profiles.morph
	if !a.active || a.from != "alpha" || a.to != "Luna" || a.start != at {
		t.Fatalf("the switch did not start the name morph: %+v", a)
	}
	batch := cmd().(tea.BatchMsg)
	if _, ok := batch[len(batch)-1]().(headerMorphMsg); !ok {
		t.Fatal("header frames must go to the shared UI, not a profile")
	}
	if head := ansi.Strip(luna.headerView()); !strings.HasPrefix(head, " OUROBOROS · ALPHA 2/2 · ") || luna.View().WindowTitle != "ouro · Luna (2/2)" || luna.input.Value() != "Luna draft" {
		t.Fatalf("only the header name should keep the old profile: %q", head)
	}
	root.Update(headerMorphMsg{generation: a.generation, at: at.Add(headerMorphDuration / 2)})
	middle := luna.headerName()
	if middle == "alpha" || middle == "Luna" || terra.headerName() != "alpha" {
		t.Fatalf("the morph must move only the visible header: %q", middle)
	}
	root.Update(profileMsg{profile: 0, msg: statusMsg{status: Status{Configured: true, Name: "Renamed"}}})
	if luna.headerName() != middle || terra.headerName() != "Renamed" {
		t.Fatal("a background status changed the shared name")
	}
	if _, cmd := root.Update(headerMorphMsg{generation: a.generation, at: at.Add(headerMorphDuration)}); cmd != nil || a.active {
		t.Fatal("the final frame should stop requesting ticks")
	}
	if head := ansi.Strip(luna.headerView()); !strings.HasPrefix(head, " OUROBOROS · LUNA 2/2 · ") {
		t.Fatalf("the new name did not settle: %q", head)
	}
	if view := ansi.Strip(root.View().Content); strings.Contains(view, "Type /setup") || strings.Contains(view, "RENAMED ") {
		t.Fatalf("the morph changed the profile's view:\n%s", view)
	}
}

func TestHeaderMorphFitsNarrowAndDebugHeaders(t *testing.T) {
	root, _, luna, _ := profiles(t)
	root.Update(shiftTab)
	a := &root.profiles.morph
	root.Update(headerMorphMsg{generation: a.generation, at: a.start.Add(headerMorphDuration / 2)})
	luna.status.Debug = true
	for _, light := range []bool{false, true} {
		luna.light = light
		for _, width := range []int{1, 7, 12, 16, 40, 100} {
			root.Update(tea.WindowSizeMsg{Width: width, Height: 30})
			head := ansi.Strip(luna.headerView())
			if !utf8.ValidString(head) || ansi.StringWidth(head) > width || strings.Contains(head, "\n") {
				t.Fatalf("the header does not fit in %d columns: %q", width, head)
			}
			if width >= 12 && !strings.HasSuffix(head, " DEBUG MODE ") {
				t.Fatalf("the morph hid the debug marker: %q", head)
			}
		}
	}
}

func TestRapidProfileSwitchContinuesFromVisibleName(t *testing.T) {
	root, terra, luna, _ := profiles(t)
	now := time.Unix(123, 0)
	terra.now, luna.now = func() time.Time { return now }, func() time.Time { return now }
	root.Update(shiftTab)
	a := &root.profiles.morph
	oldGeneration := a.generation
	now = now.Add(headerMorphDuration / 2)
	root.Update(headerMorphMsg{generation: oldGeneration, at: now})
	visible := luna.headerName()
	root.Update(shiftTab)
	if a.from != visible || a.to != "alpha" || terra.headerName() != visible || a.generation == oldGeneration {
		t.Fatal("a quick switch jumped instead of continuing from the name shown")
	}
	if _, cmd := root.Update(headerMorphMsg{generation: oldGeneration, at: now.Add(time.Second)}); cmd != nil || !a.active || a.elapsed != 0 {
		t.Fatal("a stale frame ended the new morph")
	}
	root.Update(headerMorphMsg{generation: a.generation, at: now.Add(headerMorphDuration)})
	if a.active || terra.headerName() != "alpha" || luna.headerName() != "Luna" {
		t.Fatal("the quick switch did not settle on the correct profile")
	}
}

func TestHeaderMorphFallbackAndSingleProfile(t *testing.T) {
	single, _ := newModel(t)
	if single.headerName() != "alpha" {
		t.Fatal("a single profile's name should be static")
	}
	single.status.Name = ""
	if single.headerName() != "ouroboros" {
		t.Fatal("an unnamed profile should use the program name")
	}
	root, _, luna, lb := profiles(t)
	lb.status.Name = ""
	root.Update(shiftTab)
	a := &root.profiles.morph
	if a.to != "ouroboros" {
		t.Fatal("the new name should be the same fallback as the header")
	}
	root.Update(headerMorphMsg{generation: a.generation, at: a.start.Add(headerMorphDuration)})
	if luna.headerName() != "ouroboros" {
		t.Fatal("the fallback did not settle")
	}
}

// On its way the letters are marks that change with the frame and from one
// letter to the next.
func TestMorphNameMarksFollowTheFrame(t *testing.T) {
	if got := morphName("ab", "cd", 300*time.Millisecond); got != "⠂⠶" {
		t.Fatalf("after 300ms: %q", got)
	}
	if got := morphName("ab", "cd", 340*time.Millisecond); got != "c⠿" {
		t.Fatalf("after 340ms: %q", got)
	}
}
