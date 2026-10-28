package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The memory screen is what the command /memory opens: the memories the
// agent of the owner has collected, drawn as a star chart, grouped by where
// they hold - the core ones, which go into every answer, on the innermost
// orbit, the global ones, and those of the project the conversation is in on
// the outermost. The one selected is named on the chart, with what it holds
// below it; a narrow dialog falls back to a list. Enter reads the one
// selected in full, tab shows the archived and expired ones too, and what is
// typed filters the chart by key and content. Nothing here changes a memory.

// The groups the memory screen lists, in the order it shows them.
const (
	groupCore = iota
	groupGlobal
	groupHere
)

// groupCount is how many groups the screen lists.
const groupCount = 3

// groupOf says which group a memory belongs to: the core ones go into every
// answer, those without a project hold anywhere, and the others in the
// project the conversation is in.
func groupOf(e plugin.Memory) int {
	switch {
	case e.Core:
		return groupCore
	case e.Project == "":
		return groupGlobal
	}
	return groupHere
}

// memoryScreen is the state of the memory screen.
type memoryScreen struct {
	listing   plugin.MemoryListing
	all       bool  // the archived and expired ones are shown too
	err       error // why the memories could not be read
	loaded    bool  // the listing came since the screen was opened
	asked     int   // how often it was asked for: an answer to an older question is dropped
	sel       int   // the memory selected, of those the filter leaves
	filter    string
	top       int            // the first line of the list shown
	page      int            // how many lines a page scrolls, as the view last laid it out
	detail    *plugin.Memory // the memory read in full, nil while the list is shown
	detailAt  int            // the first line of the memory that is read
	readLines int            // how many lines the memory that is read has, as the view last laid it out
	edit      *memoryEdit    // the memory being edited, nil while none is
	pick      string         // the id of a memory to select once the listing came, "" for none
	// changes shows the changes of the archivist instead of the memories,
	// see changesView.
	changes      bool
	changeLog    []plugin.MemoryChange
	changeErr    error
	changeLoaded bool
	changeAt     int
	undoAsk      int64 // the change whose undo waits for a second enter
}

// memoriesMsg is the memories of the node, on the way to the screen.
type memoriesMsg struct {
	listing plugin.MemoryListing
	err     error
	asked   int
}

// changesMsg is the changes of the archivist, on the way to the screen.
type changesMsg struct {
	changes []plugin.MemoryChange
	err     error
}

// changeUndoneMsg is how taking back a change went.
type changeUndoneMsg struct {
	id  int64
	err error
}

// loadChanges asks for the changes the archivist made to the memories.
func (m *Model) loadChanges() tea.Cmd {
	return func() tea.Msg {
		changes, err := m.backend.MemoryChanges(m.ctx, 20)
		return changesMsg{changes, err}
	}
}

// changesLoaded shows the changes, unless the memory screen was left.
func (m *Model) changesLoaded(msg changesMsg) tea.Cmd {
	s := &m.memories
	if m.screen != screenMemory {
		return nil
	}
	s.changeLog, s.changeErr, s.changeLoaded = msg.changes, msg.err, true
	s.changeAt = max(0, min(s.changeAt, len(msg.changes)-1))
	return nil
}

// changeUndone says how taking back a change went and reads the log anew.
func (m *Model) changeUndone(msg changeUndoneMsg) tea.Cmd {
	if msg.err != nil {
		m.setNotice(msg.err.Error(), true)
	} else {
		m.setNotice(fmt.Sprintf("change #%d undone", msg.id), false)
	}
	return m.loadChanges()
}

// openMemory opens the memories the agent has collected.
func (m *Model) openMemory() tea.Cmd {
	m.memories = memoryScreen{}
	m.screen = screenMemory
	return m.loadMemories()
}

// loadMemories asks for the memories of the project the conversation is in.
func (m *Model) loadMemories() tea.Cmd {
	s := &m.memories
	s.asked++
	asked, all, project := s.asked, s.all, m.chat.directory
	return func() tea.Msg {
		listing, err := m.backend.Memories(m.ctx, project, all)
		return memoriesMsg{listing, err, asked}
	}
}

// memoriesLoaded shows the memories, unless the screen was opened again or
// asked anew since.
func (m *Model) memoriesLoaded(msg memoriesMsg) tea.Cmd {
	s := &m.memories
	if msg.asked != s.asked || m.screen != screenMemory {
		return nil
	}
	s.listing, s.err, s.loaded = msg.listing, msg.err, true
	s.sel = max(0, min(s.sel, len(s.rows())-1))
	// A memory that was saved is selected where it is now, the latest
	// updated first, so that the chart shows it where the edit left it.
	if s.pick != "" {
		for i, e := range s.rows() {
			if e.ID == s.pick {
				s.sel = i
			}
		}
		s.pick = ""
	}
	return nil
}

// rows are the memories the screen lists: those the filter holds, in key and
// content, in the order the node listed them.
func (s *memoryScreen) rows() []plugin.Memory {
	if s.filter == "" {
		return s.listing.Memories
	}
	want := strings.ToLower(s.filter)
	out := make([]plugin.Memory, 0, len(s.listing.Memories))
	for _, e := range s.listing.Memories {
		if strings.Contains(strings.ToLower(e.Key+" "+e.Content), want) {
			out = append(out, e)
		}
	}
	return out
}

// handleKey handles keys on the memory screen: what is typed filters the
// list, up and down select a memory, enter reads it in full, tab shows the
// archived and expired ones too, and esc takes back what is typed and then
// goes back to the chat.
func (s *memoryScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	if s.edit != nil {
		return s.editKey(m, msg)
	}
	if s.detail != nil {
		return s.readingKey(m, msg)
	}
	if s.changes {
		return s.changesKey(m, msg)
	}
	rows := s.rows()
	move := func(to int) { s.sel = max(0, min(to, len(rows)-1)) }
	switch msg.String() {
	case "esc":
		if s.filter == "" {
			m.screen = screenChat
		} else {
			s.filter, s.sel, s.top = "", 0, 0
		}
		return nil
	case "ctrl+e": // a letter of its own, as a typed one filters the list
		return s.openEdit(m, s.sel)
	case "ctrl+r":
		s.changes, s.changeAt, s.undoAsk = true, 0, 0
		return m.loadChanges()
	case "up":
		move(s.sel - 1)
	case "down":
		move(s.sel + 1)
	case "pgup":
		move(s.sel - max(1, s.page))
	case "pgdown":
		move(s.sel + max(1, s.page))
	case "home":
		move(0)
	case "end":
		move(len(rows) - 1)
	case "tab":
		s.all, s.sel, s.top = !s.all, 0, 0
		return m.loadMemories() // the archived and expired ones, or not
	case "enter":
		if len(rows) > 0 {
			s.open(s.sel)
		}
	case "backspace":
		if r := []rune(s.filter); len(r) > 0 {
			s.filter, s.sel, s.top = string(r[:len(r)-1]), 0, 0
		}
	default:
		typed := msg.String()
		if typed == "space" { // the space bar is told by its name, but it types a space
			typed = " "
		}
		if r := []rune(typed); len(r) == 1 && unicode.IsPrint(r[0]) {
			s.filter, s.sel, s.top = s.filter+string(r), 0, 0
		}
	}
	return nil
}

// changesKey handles keys on the changes of the archivist: up and down move,
// enter asks once more and then takes the change back, and esc takes the
// question back and then the log.
func (s *memoryScreen) changesKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	rows := s.changeLog
	move := func(to int) { s.changeAt = max(0, min(to, len(rows)-1)) }
	switch msg.String() {
	case "esc":
		if s.undoAsk != 0 {
			s.undoAsk = 0
		} else {
			s.changes = false
		}
	case "ctrl+r":
		s.changes = false
	case "up":
		move(s.changeAt - 1)
	case "down":
		move(s.changeAt + 1)
	case "pgup":
		move(s.changeAt - max(1, s.page))
	case "pgdown":
		move(s.changeAt + max(1, s.page))
	case "home":
		move(0)
	case "end":
		move(len(rows) - 1)
	case "enter":
		if s.changeAt >= len(rows) || rows[s.changeAt].Undone {
			return nil
		}
		id := rows[s.changeAt].ID
		if s.undoAsk != id {
			s.undoAsk = id
			return nil
		}
		s.undoAsk = 0
		return func() tea.Msg {
			return changeUndoneMsg{id: id, err: m.backend.UndoMemoryChange(m.ctx, id)}
		}
	}
	return nil
}

// changesView shows the changes the archivist made, the newest first, with
// the one selected marked, and what enter does with it.
func (s *memoryScreen) changesView(m *Model, w, h, inner int) string {
	head := []string{boldStyle.Render("Archivist changes") + mutedStyle.Render(fmt.Sprintf(" · %d", len(s.changeLog))), ""}
	if s.changeErr != nil {
		return s.box(m, w, h, inner, append(head, errStyle.Render("✗ The changes cannot be listed: "+s.changeErr.Error())))
	}
	if !s.changeLoaded {
		return s.box(m, w, h, inner, append(head, m.spinner.View()+mutedStyle.Render(" reading the changes…")))
	}
	if len(s.changeLog) == 0 {
		return s.box(m, w, h, inner, append(head, mutedStyle.Render("The archivist has changed nothing yet.")))
	}
	var lines []string
	for i, c := range s.changeLog {
		when := c.At
		if t, err := time.Parse(time.RFC3339Nano, c.At); err == nil {
			when = ago(t, m.now())
		}
		state := ""
		if c.Undone {
			state = " · undone"
		}
		line := fmt.Sprintf("#%d %s · %s · %s%s", c.ID, c.Action, when, short(c.Reason, max(8, inner-32)), state)
		if i == s.changeAt {
			line = lipgloss.NewStyle().Foreground(accent).Render("› ") + line
		} else {
			line = "  " + line
		}
		if c.Undone {
			line = mutedStyle.Render(line)
		}
		lines = append(lines, line)
	}
	if s.undoAsk != 0 {
		lines = append(lines, "", mutedStyle.Render(fmt.Sprintf("Enter again to undo change #%d; esc takes the question back.", s.undoAsk)))
	} else {
		lines = append(lines, "", mutedStyle.Render("enter undo · esc back"))
	}
	room := max(1, h-2-len(head))
	top := max(0, min(s.changeAt, max(0, len(lines)-room)))
	s.page = max(1, room)
	return s.box(m, w, h, inner, append(head, lines[top:min(len(lines), top+room)]...))
}

// readingKey handles keys on a memory that is read in full: esc and enter go
// back to the list, and the rest moves through what it holds.
func (s *memoryScreen) readingKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "enter":
		s.detail = nil
	case "e": // no letter types here, as none filters the list
		edit, cmd := editing(*s.detail, m.light)
		s.edit = edit
		return cmd
	case "up":
		s.detailAt = max(0, s.detailAt-1)
	case "down":
		s.detailAt++
	case "pgup":
		s.detailAt = max(0, s.detailAt-max(1, s.page))
	case "pgdown":
		s.detailAt += max(1, s.page)
	case "home":
		s.detailAt = 0
	case "end":
		s.detailAt = s.readLines // the view keeps what is left of it
	}
	return nil
}

// view shows, in a dialog, the memories as a star chart, or in a list where
// the dialog is too small for one, or the memory that is read in full.
func (s *memoryScreen) view(m *Model) (string, []hit) {
	w, h := m.dialogSize()
	inner := max(10, w-4)
	if s.edit != nil {
		return s.editingView(m, w, h, inner), nil
	}
	if s.changes {
		return s.changesView(m, w, h, inner), nil
	}
	if s.detail != nil {
		return s.reading(m, w, h, inner), nil
	}
	head := []string{boldStyle.Render("Memories") + mutedStyle.Render(" · "+s.summary()), ""}
	if s.filter != "" {
		head = append(head, mutedStyle.Render("find ")+ansi.Truncate(s.filter, max(1, inner-6), "…"), "")
	}
	if s.err == nil && s.loaded && len(s.rows()) > 0 && inner >= chartMinWidth && h-2-len(head) >= chartMinRows+chartFootRows {
		return s.chartView(m, w, h, inner, head, s.rows())
	}
	var lines []string
	var hits []hit
	at, height := 0, 1
	switch {
	case s.err != nil:
		lines = append(lines, errStyle.Render("✗ The memories cannot be listed: "+s.err.Error()))
	case !s.loaded:
		lines = append(lines, m.spinner.View()+mutedStyle.Render(" reading the memories…"))
	case len(s.rows()) == 0:
		lines = append(lines, mutedStyle.Render(s.nothing()))
	default:
		rows := s.rows()
		var counts [groupCount]int
		keyWidth := 0
		for _, e := range rows {
			counts[groupOf(e)]++
			keyWidth = max(keyWidth, ansi.StringWidth(e.Key))
		}
		keyWidth = min(keyWidth, 24)
		group := -1
		for i, e := range rows {
			if g := groupOf(e); g != group {
				group = g
				if len(lines) > 0 {
					lines = append(lines, "")
				}
				lines = append(lines, s.groupTitle(m, g, counts[g]))
			}
			if i == s.sel {
				at = len(lines)
			}
			mark, style := "   ", lipgloss.NewStyle()
			if i == s.sel {
				mark, style = lipgloss.NewStyle().Foreground(accent).Render("›  "), style.Bold(true)
			}
			key := style.Width(keyWidth + 2).Render(ansi.Truncate(e.Key, keyWidth, "…"))
			aged := s.age(e, m)
			room := inner - lipgloss.Width(mark) - keyWidth - 2 - lipgloss.Width(aged) - 2
			if room < 8 && aged != "" { // no room for both: the content stays
				aged, room = "", inner-lipgloss.Width(mark)-keyWidth-2
			}
			line := mark + key + mutedStyle.Render(short(e.Content, max(8, room)))
			if aged != "" { // the age at the right edge, so that the column stands
				line += strings.Repeat(" ", max(1, inner-lipgloss.Width(line)-lipgloss.Width(aged))) + mutedStyle.Render(aged)
			}
			hits = append(hits, onLine(len(lines), choosing(i == s.sel, func() { s.sel = i }, func() tea.Cmd {
				return s.handleKey(m, enterKey)
			})))
			lines = append(lines, line)
		}
	}
	room := max(1, h-2-len(head))
	scrolls := len(lines) > room && room > 1
	if scrolls {
		room-- // the line that says there is more
	}
	top := s.top
	if top > at || top+room < at+height {
		top = at
	}
	top = max(0, min(top, max(0, len(lines)-room)))
	s.top, s.page = top, max(1, room)
	more := len(lines)-top-room > 0
	hits = shifted(hits, top, len(head), room)
	shown := lines[top:min(len(lines), top+room)]
	if scrolls && more {
		shown = append(shown, mutedStyle.Render("  ↓ more"))
	}
	if scrolls && top > 0 {
		head[len(head)-1] = mutedStyle.Render("  ↑ more")
	}
	return s.box(m, w, h, inner, append(head, shown...)), hits
}

// reading shows the memory selected in full, its content wrapped to the
// dialog and moved through by the keys.
func (s *memoryScreen) reading(m *Model, w, h, inner int) string {
	e := s.detail
	head := []string{
		boldStyle.Render(ansi.Truncate(e.Key, max(1, inner-14), "…")) + mutedStyle.Render(" · "+holds(e)),
		mutedStyle.Render(memoryAbout(e, m.now())),
		"",
	}
	lines := wrapped(e.Content, "", inner, lipgloss.NewStyle())
	s.readLines = len(lines)
	room := max(1, h-2-len(head))
	scrolls := len(lines) > room && room > 1
	if scrolls {
		room--
	}
	top := max(0, min(s.detailAt, max(0, len(lines)-room)))
	s.detailAt, s.page = top, max(1, room)
	shown := lines[top:min(len(lines), top+room)]
	if scrolls && len(lines)-top-room > 0 {
		shown = append(shown, mutedStyle.Render("  ↓ more"))
	}
	if scrolls && top > 0 {
		head[len(head)-1] = mutedStyle.Render("  ↑ more")
	}
	return s.box(m, w, h, inner, append(head, shown...))
}

// box is what lies over the chat: its lines cut to what it holds, in the
// border of a dialog.
func (s *memoryScreen) box(m *Model, w, h, inner int, lines []string) string {
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], inner, "…")
	}
	body := lipgloss.NewStyle().Width(inner).MaxHeight(max(1, h-2)).Render(strings.Join(lines, "\n"))
	return box(true, m.light).Width(w).Render(body)
}

// groupTitle says what a group holds, with how many of the memories shown
// are in it, and where those of the project hold.
func (s *memoryScreen) groupTitle(m *Model, group, n int) string {
	name, about := "Core", "given with every answer"
	switch group {
	case groupGlobal:
		name, about = "Global", "remembered anywhere"
	case groupHere:
		name, about = "Here", "in this project"
		if dir := m.chat.directory; dir != "" {
			about = "in " + short(filepath.Base(dir), 40)
		} else {
			about = "no project selected: /cd names one"
		}
	}
	return boldStyle.Render(name) + mutedStyle.Render(fmt.Sprintf(" · %d, %s", n, about))
}

// summary is what the line beside the title says: how many memories there
// are, of each group, and how many are hidden.
func (s *memoryScreen) summary() string {
	switch {
	case s.err != nil:
		return "not read"
	case !s.loaded:
		return "reading…"
	}
	if s.filter != "" {
		return fmt.Sprintf("%d of %d", len(s.rows()), s.listing.Total)
	}
	var counts [groupCount]int
	for _, e := range s.listing.Memories {
		counts[groupOf(e)]++
	}
	parts := []string{fmt.Sprintf("%d remembered", s.listing.Total),
		fmt.Sprintf("%c %d core", starGlyph(groupCore), counts[groupCore]),
		fmt.Sprintf("%c %d global", starGlyph(groupGlobal), counts[groupGlobal]),
		fmt.Sprintf("%c %d here", starGlyph(groupHere), counts[groupHere])}
	if s.listing.Hidden > 0 {
		parts = append(parts, fmt.Sprintf("%d hidden", s.listing.Hidden))
	}
	return strings.Join(parts, " · ")
}

// nothing says what an empty list means.
func (s *memoryScreen) nothing() string {
	if s.filter != "" {
		return "Nothing here holds “" + short(s.filter, 20) + "”."
	}
	if s.all {
		return "Nothing remembered yet."
	}
	return "Nothing remembered yet: what you tell it, and what its reviews find, is kept here."
}

// age says how old a memory is, "" if its time is not known: "just now",
// the minutes, hours and days, then the day or the year.
func (s *memoryScreen) age(e plugin.Memory, m *Model) string {
	t, err := time.Parse(time.RFC3339, e.Updated)
	if err != nil {
		return ""
	}
	return ago(t, m.now())
}

// ago says how long ago t was, in the fewest words: the time of day for
// today, the day for the last week, the month for the year, and the year
// for anything older.
func ago(t, now time.Time) string {
	switch d := now.Sub(t); {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case t.Year() == now.Year():
		return t.Format("2 Jan")
	}
	return t.Format("Jan 2006")
}

// holds says where a memory holds.
func holds(e *plugin.Memory) string {
	switch groupOf(*e) {
	case groupCore:
		return "core · given with every answer"
	case groupGlobal:
		return "global · remembered anywhere"
	}
	return "here · in " + short(filepath.Base(e.Project), 40) // a memory of no project is global
}

// memoryAbout says what a memory is: when it was last written, how often it
// was used and loaded, and whether it is archived or expired.
func memoryAbout(e *plugin.Memory, now time.Time) string {
	var parts []string
	if e.Archived {
		parts = append(parts, "archived")
	}
	if until, err := time.Parse(time.RFC3339, e.Expires); e.Expires != "" && err == nil && !until.After(now) {
		parts = append(parts, "expired")
	}
	if t, err := time.Parse(time.RFC3339, e.Updated); err == nil {
		parts = append(parts, "updated "+ago(t, now))
	}
	parts = append(parts, fmt.Sprintf("used %d× · loaded %d×", e.Used, e.Loaded))
	if e.Version > 1 {
		parts = append(parts, fmt.Sprintf("v%d", e.Version))
	}
	return strings.Join(parts, " · ")
}
