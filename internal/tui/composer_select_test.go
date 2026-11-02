package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/cursor"
	tea "charm.land/bubbletea/v2"
)

// The composer grows with what is written and shows every row of it: the
// first wrap does not scroll the line that wrapped out of it.
func TestComposerShowsEveryLineOfWhatIsTyped(t *testing.T) {
	m, _ := newModel(t)
	inner := m.composerInner()
	for i := 1; i <= 2*inner+5; i++ {
		m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
		value, lines := m.input.Value(), m.composerLines(m.transcriptWidth())
		if len(lines) != m.composerRows() || m.composerRows() != m.input.Height() {
			t.Fatalf("%d cells: %d rows shown, %d of the composer, %d of the field", len(value), len(lines), m.composerRows(), m.input.Height())
		}
		if got := strings.Count(strings.Join(lines, "\n"), "x"); got != len(value) {
			t.Fatalf("%d cells: %d of them shown at the %dth, %q", len(value), got, i, lines)
		}
	}
	// Beyond the share of the window the field scrolls, and the rows it shows
	// keep their height.
	share := max(1, m.height*composerShare/100)
	for i := 0; i < 30*inner; i++ {
		m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
		if m.composerRows() > share || len(m.composerLines(m.transcriptWidth())) != m.composerRows() {
			t.Fatalf("the composer takes %d rows, beyond the share of %d", m.composerRows(), share)
		}
	}
}

// A drag in the composer selects what it covers and copies it, as a drag in
// the history does. The mark stays while the text does, also while the
// cursor blinks, and a key that changes the text drops it.
func TestSelectAndCopyTheComposer(t *testing.T) {
	copied := clipboardOf(t)
	m, _ := newModel(t)
	m.input.SetValue("hello world")
	m.layout()
	x, y := m.transcriptLeft()+promptWidth, m.composerTop()
	cmd := dragged(m, x, y, x+4, y)
	if cmd == nil {
		t.Fatal("the selection of the composer should be copied")
	}
	for _, c := range cmd().(tea.BatchMsg) {
		if c != nil {
			c()
		}
	}
	if len(*copied) != 1 || (*copied)[0] != "hello" {
		t.Fatalf("copied %q", *copied)
	}
	if line := m.composerLines(m.transcriptWidth())[0]; !strings.Contains(line, "\x1b[7m") {
		t.Fatalf("the selection is not marked: %q", line)
	}
	m.Update(cursor.BlinkMsg{})
	if m.inputSel == nil || !strings.Contains(m.composerLines(m.transcriptWidth())[0], "\x1b[7m") {
		t.Fatal("the blink dropped the selection of the composer")
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.inputSel != nil {
		t.Fatal("what is typed left the selection of the composer")
	}
	// A click marks nothing.
	m.input.SetValue("hello world")
	m.layout()
	mouseAt(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	mouseAt(m, tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	if m.inputSel != nil {
		t.Fatal("a click left a selection in the composer")
	}
}
