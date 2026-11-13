package tui

import (
	"slices"
	"strings"
	"testing"
)

// A history is the lines that stand and the lines of what is still coming,
// parted by a blank line where both have some, with a window on them.
func TestHistoryViewLines(t *testing.T) {
	var v historyView
	if v.Width() != 0 || v.Height() != 0 || v.TotalLineCount() != 0 || v.YOffset() != 0 || !v.AtBottom() {
		t.Fatalf("an empty history: %+v", v)
	}
	if v.GetContent() != "" || v.View() != "" {
		t.Fatalf("an empty history shows %q", v.GetContent())
	}
	v.SetWidth(10)
	v.SetHeight(2)
	v.SetContent("a\nb\nc")
	if v.TotalLineCount() != 3 || v.Height() != 2 || v.Width() != 10 || v.AtBottom() || v.YOffset() != 0 {
		t.Fatalf("content at 10x2: %+v", v)
	}
	v.GotoBottom()
	if got := strings.Split(v.View(), "\n"); !slices.Equal(got, []string{"b", "c"}) {
		t.Fatalf("the window: %q", got)
	}
	if v.GetContent() != "a\nb\nc" {
		t.Fatalf("content: %q", v.GetContent())
	}
	v.GotoTop()
	if v.YOffset() != 0 || v.AtBottom() || !strings.HasPrefix(v.View(), "a\n") {
		t.Fatalf("at the top: %+v", v)
	}
	v.SetYOffset(99) // beyond the end, it comes to rest at it
	if v.YOffset() != 1 {
		t.Fatalf("beyond the end: %d", v.YOffset())
	}
	v.SetYOffset(-5)
	if v.YOffset() != 0 {
		t.Fatalf("before the top: %d", v.YOffset())
	}
	// What is still coming stands below a blank line, and the window counts it.
	v.SetLines([]string{"one", "two"}, []string{"three", "four"})
	if v.TotalLineCount() != 5 || v.GetContent() != "one\ntwo\n\nthree\nfour" {
		t.Fatalf("settled and live: %q", v.GetContent())
	}
	if v.line(2) != "" || v.line(3) != "three" || v.line(5) != "" || v.line(-1) != "" {
		t.Fatalf("lines: %q %q %q", v.line(2), v.line(3), v.line(5))
	}
	if v.line(4) != "four" {
		t.Fatalf("the last live line: %q", v.line(4))
	}
	// Only what is still coming, and only what stands, part nothing.
	v.SetLines(nil, []string{"live"})
	if v.TotalLineCount() != 1 || v.GetContent() != "live" {
		t.Fatalf("only live: %q", v.GetContent())
	}
	v.SetLines([]string{"settled"}, nil)
	if v.TotalLineCount() != 1 || v.GetContent() != "settled" {
		t.Fatalf("only settled: %q", v.GetContent())
	}
	// Content that is set anew replaces both.
	v.SetContent("")
	if v.TotalLineCount() != 0 || v.GetContent() != "" {
		t.Fatalf("cleared: %q", v.GetContent())
	}
}

// A window that is not laid out yet shows nothing, and a line that is gone
// reads empty.
func TestHistoryViewWithoutAWindow(t *testing.T) {
	var v historyView
	v.SetLines([]string{"a"}, []string{"b"})
	v.SetWidth(0)
	if v.View() != "" {
		t.Fatalf("no width: %q", v.View())
	}
	v.SetWidth(4)
	v.SetHeight(0)
	if v.View() != "" {
		t.Fatalf("no height: %q", v.View())
	}
	if v.line(9) != "" || v.line(-1) != "" {
		t.Fatal("a line beyond the history is empty")
	}
}

// appendLines parts two blocks as the chat does, with a blank line, and
// keeps one block that is only a blank line.
func TestAppendLines(t *testing.T) {
	got := appendLines(nil, []string{"a\nb", "", "c"}, 0)
	if want := []string{"a", "b", "", "", "", "c"}; !slices.Equal(got, want) {
		t.Fatalf("lines %q, want %q", got, want)
	}
	// A block on its own that is to come after blocks already there gets the
	// parting line too.
	got = appendLines([]string{"x"}, []string{"y"}, 1)
	if want := []string{"x", "", "y"}; !slices.Equal(got, want) {
		t.Fatalf("appended %q, want %q", got, want)
	}
}
