package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// /memory shows the memories the agent has collected, grouped by where they
// hold, the latest updated first; typing filters them, enter reads the one
// selected in full, and tab shows the archived and expired ones too.
func TestMemoryDialog(t *testing.T) {
	m, b := newModel(t)
	at := time.Unix(1000, 0)
	m.now = func() time.Time { return at }
	m.chat.directory = "/home/lucas/Projects/ouroboros"
	b.memories = plugin.MemoryListing{
		Total:  3,
		Hidden: 2,
		Memories: []plugin.Memory{
			{Key: "workflow", Content: "Changes go in a branch, as an MR against main", Core: true,
				Updated: at.Add(-2 * time.Hour).Format(time.RFC3339), Used: 4, Loaded: 9, Version: 2},
			{Key: "drink", Content: "oolong tea in the afternoon", Updated: at.Add(-48 * time.Hour).Format(time.RFC3339)},
			{Key: "build", Content: "make test runs the tests", Project: m.chat.directory,
				Updated: at.Add(-time.Minute).Format(time.RFC3339)},
		},
	}

	m.Update(run(m, "/memory")())
	if m.screen != screenMemory || !m.inDialog() || b.memoryProject != m.chat.directory || b.memoryAll {
		t.Fatalf("screen %v, dialog %v, asked for %q, all %v", m.screen, m.inDialog(), b.memoryProject, b.memoryAll)
	}
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"Memories · 3 remembered · ✸ 1 core · ✦ 1 global · ✧ 1 here · 2 hidden",
		"· core ·", "· global ·", "· here ·",
		"workflow", "drink", "build",
		"Changes go in a branch", "2h",
		"tab archived",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("%q is not in the chart:\n%s", want, view)
		}
	}
	// The memory selected is the accent chip the rest of the UI marks a
	// selection with, here on its name in the chart: a background before it.
	raw := m.View().Content
	mark := strings.Index(raw, "workflow")
	if mark < 0 || !strings.Contains(raw[strings.LastIndex(raw[:mark], "\x1b["):mark], "48;2;") {
		t.Fatalf("the memory selected is not marked:\n%s", raw)
	}

	// Up and down select; enter reads the one selected in full.
	press(m, "down")
	if m.memories.sel != 1 {
		t.Fatalf("selected %d", m.memories.sel)
	}
	press(m, "enter")
	view = ansi.Strip(m.View().Content)
	for _, want := range []string{"drink · global · remembered anywhere", "updated 2d", "used 0× · loaded 0×", "oolong tea in the afternoon", "esc back"} {
		if !strings.Contains(view, want) {
			t.Fatalf("%q is not in the memory:\n%s", want, view)
		}
	}
	press(m, "esc")
	if m.memories.detail != nil || m.screen != screenMemory {
		t.Fatal("esc should go back to the list, not to the chat")
	}
	press(m, "up")
	press(m, "enter")
	if !strings.Contains(ansi.Strip(m.View().Content), "workflow · core · given with every answer") {
		t.Fatalf("the core memory read:\n%s", m.View().Content)
	}
	press(m, "esc", "esc")
	if m.screen != screenChat {
		t.Fatal("esc from the list should go back to the chat")
	}

	// What is typed filters by key and content, and esc takes it back first.
	m.Update(run(m, "/memory")())
	press(m, "b", "u", "i", "l")
	view = ansi.Strip(m.View().Content)
	if m.memories.filter != "buil" || !strings.Contains(view, "1 of 3") ||
		!strings.Contains(view, "make test runs the tests") || strings.Contains(view, "oolong tea") {
		t.Fatalf("filter %q:\n%s", m.memories.filter, view)
	}
	press(m, "backspace")
	if m.memories.filter != "bui" {
		t.Fatalf("filter %q", m.memories.filter)
	}
	press(m, "esc")
	if m.memories.filter != "" || m.screen != screenMemory {
		t.Fatal("esc should take the filter back first")
	}
	press(m, "esc")

	// Tab shows the archived and expired ones too.
	m.Update(run(m, "/memory")())
	m.Update(press(m, "tab")())
	if !m.memories.all || !b.memoryAll || !strings.Contains(ansi.Strip(m.View().Content), "tab archived") {
		t.Fatalf("tab: all %v, asked %v:\n%s", m.memories.all, b.memoryAll, m.View().Content)
	}
}

// Without a store to read, the dialog says why; without memories it says
// that too, and it stays inside a narrow window.
func TestMemoryDialogEmptyAndNarrow(t *testing.T) {
	m, b := newModel(t)
	b.fail = map[string]error{"memories": errors.New("the store cannot be read")}
	m.Update(run(m, "/memory")())
	if !strings.Contains(ansi.Strip(m.View().Content), "The memories cannot be listed: the store cannot be read") {
		t.Fatalf("failed:\n%s", m.View().Content)
	}
	press(m, "esc")

	b.fail = nil
	m.Update(run(m, "/memory")())
	if !strings.Contains(ansi.Strip(m.View().Content), "Nothing remembered yet") {
		t.Fatalf("empty:\n%s", m.View().Content)
	}
	press(m, "esc")

	// A narrow window keeps the dialog inside it, whatever the key holds.
	b.memories = plugin.MemoryListing{Total: 1, Memories: []plugin.Memory{{
		Key: "a very long key that does not fit a narrow dialog", Content: strings.Repeat("what it holds ", 30),
		Updated: m.now().Format(time.RFC3339),
	}}}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})
	m.Update(run(m, "/memory")())
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	if len(lines) != 12 {
		t.Fatalf("the view is %d lines high", len(lines))
	}
	w, _ := m.dialogSize()
	if w > m.width-4 {
		t.Fatalf("the dialog is %d wide in a window of %d", w, m.width)
	}
	for i, l := range lines {
		if lipgloss.Width(l) > m.width {
			t.Fatalf("line %d is wider than the window: %q", i, l)
		}
	}
	press(m, "enter")
	for i, l := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if lipgloss.Width(l) > m.width {
			t.Fatalf("the memory: line %d is wider than the window: %q", i, l)
		}
	}
}

// A click on a name of the chart selects that memory, and a click on the one
// selected reads it, as enter does.
func TestMemoryChartClicks(t *testing.T) {
	m, b := newModel(t)
	at := time.Unix(1000, 0)
	m.now = func() time.Time { return at }
	var list []plugin.Memory
	for _, k := range []string{"workflow", "drink", "build", "minerva", "hooks", "tea"} {
		list = append(list, plugin.Memory{Key: k, Content: "what " + k + " holds",
			Updated: at.Format(time.RFC3339)})
	}
	b.memories = plugin.MemoryListing{Total: len(list), Memories: list}
	m.Update(run(m, "/memory")())

	if clickOn(t, m, "drink", 0); m.memories.sel != 1 || m.memories.detail != nil {
		t.Fatalf("a click should select, not read: %d, %v", m.memories.sel, m.memories.detail)
	}
	clickOn(t, m, "drink", 0)
	if m.memories.detail == nil || m.memories.detail.Key != "drink" {
		t.Fatalf("a click on the one selected should read it: %v", m.memories.detail)
	}
}

// The chart names every memory where it has room, and keeps the dialog in a
// window wide enough for it, also with many memories.
func TestMemoryChartNamesThem(t *testing.T) {
	m, b := newModel(t)
	at := time.Unix(1000, 0)
	m.now = func() time.Time { return at }
	m.chat.directory = "/home/lucas/Projects/ouroboros"
	keys := []string{"workflow", "answers", "drink", "music", "names", "build",
		"minerva", "hooks", "tea", "piano", "windows", "gofmt"}
	var list []plugin.Memory
	for i, k := range keys {
		list = append(list, plugin.Memory{Key: k, Content: "what " + k + " holds",
			Updated: at.Format(time.RFC3339), Project: m.chat.directory, Core: i < 2})
	}
	b.memories = plugin.MemoryListing{Total: len(list), Memories: list}
	m.Update(run(m, "/memory")())
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "· core ·") || !strings.Contains(view, "· here ·") {
		t.Fatalf("the chart should name the orbits:\n%s", view)
	}
	for _, k := range keys {
		if !strings.Contains(view, k) {
			t.Fatalf("the chart does not name %q:\n%s", k, view)
		}
	}
	for i, l := range strings.Split(view, "\n") {
		if lipgloss.Width(l) > m.width {
			t.Fatalf("line %d is wider than the window: %q", i, l)
		}
	}
}

// The dialog of the memories is as square as the room allows: a cell of the
// terminal is about twice as tall as it is wide, so the dialog takes half as
// many rows as it has columns, and all the room of a window that is short.
func TestMemoryDialogIsSquare(t *testing.T) {
	m, b := newModel(t)
	b.memories = plugin.MemoryListing{Total: 1, Memories: []plugin.Memory{{
		Key: "workflow", Content: "what it holds", Updated: m.now().Format(time.RFC3339),
	}}}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	m.Update(run(m, "/memory")())
	if w, h := m.dialogSize(); w != memoryWidth || h != (w+1)/2 {
		t.Fatalf("in a tall window the dialog is %d×%d, not square", w, h)
	}
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	if len(lines) != 60 {
		t.Fatalf("the view is %d lines high", len(lines))
	}
	top, bottom := -1, -1
	for i, l := range lines {
		switch r := []rune(l); {
		case top < 0 && strings.ContainsRune(string(r), '╭'):
			top = i
		case top >= 0 && bottom < 0 && strings.ContainsRune(string(r), '╰'):
			bottom = i
		}
	}
	// The chat is faded around it, so the box sits in the middle of the
	// window with as much room above as below.
	if above, below := top, len(lines)-bottom-2; above-below < -1 || above-below > 1 {
		t.Fatalf("the square should stand in the middle: %d lines above, %d below", above, below)
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	if w, h := m.dialogSize(); w != memoryWidth || h != 26 {
		t.Fatalf("in a short window the dialog is %d×%d, it should take the room it has", w, h)
	}
}
