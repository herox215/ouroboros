package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// manyMemories are n memories of every group, the latest updated first, as
// the node lists them.
func manyMemories(at time.Time, project string, n int) []plugin.Memory {
	var list []plugin.Memory
	for i := 0; i < n; i++ {
		updated := at.Add(-time.Duration(i) * time.Minute).Format(time.RFC3339)
		list = append(list,
			plugin.Memory{ID: fmt.Sprintf("c%d", i), Key: fmt.Sprintf("core%d", i), Content: "core holds", Core: true, Updated: updated},
			plugin.Memory{ID: fmt.Sprintf("g%d", i), Key: fmt.Sprintf("global%d", i), Content: "global holds", Updated: updated},
			plugin.Memory{ID: fmt.Sprintf("h%d", i), Key: fmt.Sprintf("here%d", i), Content: "here holds", Project: project, Updated: updated})
	}
	return list
}

// key is a key the press helper has no name for.
func keyOf(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

// A window too low for the chart lists the memories by group, with their age
// at the right edge; the keys page through the list, and it says where more
// is above and below.
func TestMemoryListPagesThroughManyMemories(t *testing.T) {
	m, b := newModel(t)
	at := time.Unix(1000, 0)
	m.now = func() time.Time { return at }
	m.chat.directory = "/home/lucas/Projects/ouroboros"
	b.memories = plugin.MemoryListing{Total: 30, Memories: manyMemories(at, m.chat.directory, 10)}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})

	// Before the listing came, the dialog says it reads them.
	cmd := run(m, "/memory")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "reading the memories") || !strings.Contains(view, "reading…") {
		t.Fatalf("the dialog before the listing:\n%s", view)
	}
	// A listing asked before the last one is let go.
	m.Update(memoriesMsg{asked: m.memories.asked - 1, listing: b.memories})
	if m.memories.loaded {
		t.Fatal("an old listing was shown")
	}
	m.Update(cmd())
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"Core · 10, given with every answer", "↓ more", "just now"} {
		if !strings.Contains(view, want) {
			t.Fatalf("%q is not in the list:\n%s", want, view)
		}
	}

	m.Update(keyOf(tea.KeyEnd))
	if m.memories.sel != 29 {
		t.Fatalf("end selected %d", m.memories.sel)
	}
	view = ansi.Strip(m.View().Content)
	if !strings.Contains(view, "↑ more") || !strings.Contains(view, "Here · 10, in ouroboros") {
		t.Fatalf("the end of the list:\n%s", view)
	}
	press(m, "pgup")
	if m.memories.sel >= 29 {
		t.Fatalf("pgup selected %d", m.memories.sel)
	}
	m.Update(keyOf(tea.KeyHome))
	press(m, "pgdown")
	if m.memories.sel == 0 {
		t.Fatal("pgdown stayed on the first")
	}

	// A click on a line selects its memory, and a click on the line
	// selected reads it, as enter does.
	m.Update(keyOf(tea.KeyHome))
	m.View()
	if clickOn(t, m, "global0", 0); m.memories.sel != 1 || m.memories.detail != nil {
		t.Fatalf("a click should select, not read: %d", m.memories.sel)
	}
	if cmd := clickOn(t, m, "global0", 0); cmd != nil {
		m.Update(cmd())
	}
	if m.memories.detail == nil || m.memories.detail.Key != "global0" {
		t.Fatalf("a click on the one selected should read it: %+v", m.memories.detail)
	}
}

// Without a project the memories of one say so in their title, and a memory
// whose age is not known goes without one.
func TestMemoryListWithoutProject(t *testing.T) {
	m, b := newModel(t)
	b.memories = plugin.MemoryListing{Total: 2, Memories: []plugin.Memory{
		{Key: "build", Content: "make test runs the tests", Project: "/somewhere"},
		{Key: "tea", Content: "oolong", Project: "/somewhere"},
	}}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	m.Update(run(m, "/memory")())
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "no project selected: /cd names one") || strings.Contains(view, "just now") {
		t.Fatalf("the list without a project:\n%s", view)
	}
}

// The space bar types a space into the filter, and a filter that holds
// nothing says so; with the archived ones shown too, an empty store says
// only that nothing is remembered.
func TestMemoryFilterFindsNothing(t *testing.T) {
	m, b := newModel(t)
	b.memories = plugin.MemoryListing{Total: 1, Memories: []plugin.Memory{{Key: "tea", Content: "oolong"}}}
	m.Update(run(m, "/memory")())
	press(m, "t")
	m.Update(keyOf(tea.KeySpace))
	press(m, "x")
	if m.memories.filter != "t x" {
		t.Fatalf("filter %q", m.memories.filter)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Nothing here holds “t x”.") {
		t.Fatalf("an empty filter:\n%s", view)
	}
	press(m, "esc")

	b.memories = plugin.MemoryListing{}
	m.Update(press(m, "tab")())
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Nothing remembered yet.") || strings.Contains(view, "what you tell it") {
		t.Fatalf("nothing at all:\n%s", view)
	}
	// Neither ctrl+e nor a click has a memory to open.
	if press(m, "ctrl+e"); m.memories.edit != nil {
		t.Fatal("an edit opened without a memory")
	}
	if m.memories.open(0); m.memories.detail != nil {
		t.Fatal("a memory opened without one")
	}
}

// A memory read in full scrolls through what it holds by the keys, and says
// where more is above it; its line says whether it is archived or expired.
func TestMemoryReadScrolls(t *testing.T) {
	m, b := newModel(t)
	at := time.Unix(1000, 0)
	m.now = func() time.Time { return at }
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	b.memories = plugin.MemoryListing{Total: 1, Memories: []plugin.Memory{{
		Key: "long", Content: strings.Join(lines, "\n"), Archived: true,
		Expires: at.Add(-time.Hour).Format(time.RFC3339),
	}}}
	m.Update(run(m, "/memory")())
	press(m, "enter")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "archived · expired") {
		t.Fatalf("the memory read:\n%s", view)
	}
	press(m, "down", "down")
	if m.memories.detailAt != 2 {
		t.Fatalf("down read from %d", m.memories.detailAt)
	}
	press(m, "up")
	press(m, "pgdown")
	if m.memories.detailAt <= 1 {
		t.Fatalf("pgdown read from %d", m.memories.detailAt)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "↑ more") {
		t.Fatalf("scrolled:\n%s", view)
	}
	press(m, "pgup")
	m.Update(keyOf(tea.KeyEnd))
	m.View() // the view keeps what is left
	if m.memories.detailAt == 0 || strings.Contains(ansi.Strip(m.View().Content), "↓ more") {
		t.Fatalf("end read from %d", m.memories.detailAt)
	}
	m.Update(keyOf(tea.KeyHome))
	if m.memories.detailAt != 0 {
		t.Fatalf("home read from %d", m.memories.detailAt)
	}
}

// The chart names the memory selected even where the stars leave its name
// no room, and says below it that it is archived.
func TestMemoryChartCrowded(t *testing.T) {
	m, b := newModel(t)
	at := time.Unix(1000, 0)
	m.now = func() time.Time { return at }
	var list []plugin.Memory
	for i := 0; i < 450; i++ {
		list = append(list, plugin.Memory{Key: fmt.Sprintf("crowded%03d", i), Content: "holds", Core: i%3 == 0,
			Project: [...]string{"", "", "/p"}[i%3], Archived: i == 0, Updated: at.Format(time.RFC3339)})
	}
	b.memories = plugin.MemoryListing{Total: len(list), Memories: list}
	m.Update(tea.WindowSizeMsg{Width: 64, Height: 21}) // the smallest chart there is
	m.Update(run(m, "/memory")())
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "crowded000") || !strings.Contains(view, "· archive") {
		t.Fatalf("the crowded chart:\n%s", view)
	}
}

// The edit takes keys in the field of the key too, and what is pasted goes
// into the field that has the keys: one line of it into the key.
func TestMemoryEditTypesAndPastes(t *testing.T) {
	m, b := newModel(t)
	b.memories = plugin.MemoryListing{Total: 1, Memories: []plugin.Memory{{ID: "a", Key: "tea", Content: "oolong"}}}
	m.Update(run(m, "/memory")())
	if m.memories.paste("nowhere") != nil {
		t.Fatal("a paste without an edit did something")
	}
	press(m, "ctrl+e", "tab", "s")
	if got := m.memories.edit.key.Value(); got != "teas" {
		t.Fatalf("the key is %q", got)
	}
	m.Update(tea.PasteMsg{Content: "y\nnot this"})
	if got := m.memories.edit.key.Value(); got != "teasy" {
		t.Fatalf("the key after the paste is %q", got)
	}
	press(m, "tab")
	m.Update(tea.PasteMsg{Content: " and\ngreen"})
	if got := m.memories.edit.content.Value(); got != "oolong and\ngreen" {
		t.Fatalf("what it holds after the paste is %q", got)
	}
}

// A paste into the list filters it by its first line.
func TestMemoryPasteFilters(t *testing.T) {
	m, b := newModel(t)
	b.memories = plugin.MemoryListing{Total: 1, Memories: []plugin.Memory{{Key: "tea", Content: "oolong"}}}
	m.Update(run(m, "/memory")())
	m.Update(tea.PasteMsg{Content: " oolong \nmore"})
	if m.memories.filter != "oolong" || m.memories.sel != 0 {
		t.Fatalf("filter %q", m.memories.filter)
	}
}

// The save says what the store would not take before it is asked: a key or
// content that is empty, or longer in bytes than the store takes; a save
// that is on its way is not asked twice, and the store's own error is shown
// as it is.
func TestMemoryEditRefusesWhatTheStoreWouldNot(t *testing.T) {
	m, b := newModel(t)
	b.memories = plugin.MemoryListing{Total: 1, Memories: []plugin.Memory{{ID: "a", Key: "tea", Content: "oolong", Version: 1}}}
	m.Update(run(m, "/memory")())
	press(m, "ctrl+e")
	e := m.memories.edit
	for _, c := range []struct{ key, content, want string }{
		{" ", "oolong", "without a key"},
		{"tea", " ", "without what it holds"},
		// The fields count runes, the store bytes: a key of 128 umlauts fits
		// the field and not the store.
		{strings.Repeat("ä", memoryMaxKey), "oolong", "longer than the 128"},
		{"tea", strings.Repeat("ä", memoryMaxContent), "longer than the 8192"},
	} {
		e.key.SetValue(c.key)
		e.content.SetValue(c.content)
		if cmd := press(m, "ctrl+s"); cmd != nil || e.err == nil || !strings.Contains(e.err.Error(), c.want) {
			t.Fatalf("%q/%d bytes: %v", c.key, len(c.content), e.err)
		}
	}
	e.key.SetValue("tea")
	e.content.SetValue("green")
	b.fail = map[string]error{"setmemory": errors.New("the disk is full")}
	cmd := press(m, "ctrl+s")
	if cmd == nil || press(m, "ctrl+s") != nil {
		t.Fatal("the save should be asked once")
	}
	// What the store says about another memory is not this one's.
	m.Update(memorySavedMsg{id: "b", err: errors.New("other")})
	if !e.saving {
		t.Fatal("an answer about another memory ended the save")
	}
	m.Update(cmd())
	if e.saving || e.err == nil || e.err.Error() != "the disk is full" {
		t.Fatalf("the error of the store: %v", e.err)
	}
}

// ago says the day for a time this year past the last week, and the month
// and year for one before.
func TestAgoNamesTheDayAndTheYear(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	if got := ago(time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC), now); got != "2 Mar" {
		t.Fatalf("this year: %q", got)
	}
	if got := ago(time.Date(2024, 3, 2, 12, 0, 0, 0, time.UTC), now); got != "Mar 2024" {
		t.Fatalf("before: %q", got)
	}
}

// The chart draws only inside itself: a star or a name off its edge is not
// drawn, a name that does not fit is said to, an orbit leaves what a star
// holds, and a cell without a style is drawn plainly.
func TestChartDrawsInsideItself(t *testing.T) {
	c := newChart(4, 3, false)
	if c.put(-1, 0, 'x', chartLabel) || c.put(0, 3, 'x', chartLabel) {
		t.Fatal("a cell off the chart was drawn")
	}
	if c.write(2, 0, "abc", chartLabel) {
		t.Fatal("a name past the edge fit")
	}
	if got := c.style(chartStyle(0)).Render("a"); got != "a" {
		t.Fatalf("a cell without a style is %q", got)
	}
	c.put(1, 1, '✸', chartStarCore)
	orbit(c, 1, 1, 1, 1)
	if c.cells[1][1].r != '✸' {
		t.Fatal("the orbit drew over the star")
	}
	// A ring round the corner reaches cells off the chart, and dots left of
	// its first column.
	orbit(c, 0, 0, 0.3, 3)

	// The forced spot of a name is above a star over the centre and below
	// one under it, and none when that is off the chart.
	if at, ok := chartSpotForced(c, 2, 0, 2, 1, 1); ok {
		t.Fatalf("a spot above the chart: %v", at)
	}
	if at, ok := chartSpotForced(c, 2, 2, 2, 1, 1); ok {
		t.Fatalf("a spot below the chart: %v", at)
	}
	if at, ok := chartSpotForced(c, 2, 1, 2, 1, 1); !ok || at != [2]int{1, 2} {
		t.Fatalf("the spot below: %v, %v", at, ok)
	}
	// A full chart has no spot for a name.
	full := newChart(3, 3, false)
	for y := 0; y < 3; y++ {
		full.write(0, y, "xxx", chartLabel)
	}
	if _, ok := chartSpot(full, 1, 1, 2, 1, 1); ok {
		t.Fatal("a full chart had room")
	}
}
