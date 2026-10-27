package tui

import (
	"cmp"
	"fmt"
	"image/color"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The chat is laid out as the window of the GUI is: the sessions that run now
// on the left, the conversation in the middle, below its head and above the
// composer, and on the right where those sessions stand. Nothing is framed: a
// line above the composer parts it from the conversation, and the
// conversation is as wide as it reads well, in the middle of its column.
// A narrow window leaves out the column on the right first, then the one on
// the left, as the GUI does.

const (
	sideMin, sideMax = 24, 40 // how wide the columns beside the conversation are, with their margins
	sideFrom         = 80     // a narrower window has no sidebar
	railFrom         = 120    // and no column on the right, unless the jobs are listed there
	transcriptMax    = 100    // how wide the conversation is at most
	sendWidth        = 3      // the button that sends, " ↑ "
	stopWidth        = 6      // the button that stops the answer, " Stop "
	buttonsWidth     = 2 + stopWidth + sendWidth
	promptWidth      = 2  // "› " before the composer
	columnPad        = 2  // the margin inside the columns beside the conversation
	composerShare    = 30 // percent of the window the composer grows to at most
	panelShare       = 40 // and the question above it
)

// columnWidth is how wide a column beside the conversation is in a window
// w cells wide: a fourth of it, within bounds.
func columnWidth(w int) int { return min(sideMax, max(sideMin, w*23/100)) }

// sideWidth is how wide the sidebar of the sessions is, 0 without it.
func (m *Model) sideWidth() int {
	if m.width < sideFrom {
		return 0
	}
	return columnWidth(m.width)
}

// railWidth is how wide the column on the right is, 0 without it. The jobs
// get it in any window, a third of it at most.
func (m *Model) railWidth() int {
	switch {
	case m.width >= railFrom:
		return columnWidth(m.width)
	case m.jobsShown:
		return min(columnWidth(m.width), m.width/3)
	}
	return 0
}

// chatWidth is how wide the column of the conversation is: what the
// columns beside it leave of the window.
func (m *Model) chatWidth() int { return max(1, m.width-m.sideWidth()-m.railWidth()) }

// transcriptWidth is how wide the conversation and the composer are.
func (m *Model) transcriptWidth() int { return max(1, min(transcriptMax, m.chatWidth()-2*columnPad)) }

// transcriptLeft is the column of the screen the conversation begins at.
func (m *Model) transcriptLeft() int {
	return m.sideWidth() + (m.chatWidth()-m.transcriptWidth())/2
}

// compact reports whether the window is too low for the margins of the
// head.
func (m *Model) compact() bool { return m.height < 24 }

// headerHeight is how many rows the head of the conversation takes, with
// its margins.
func (m *Model) headerHeight() int {
	if m.compact() {
		return 3
	}
	return 5
}

// historyTop is the row of the screen the conversation begins at.
func (m *Model) historyTop() int { return m.headerHeight() }

// composerInner is how wide the field of the composer is: what the prompt
// and the buttons leave of it.
func (m *Model) composerInner() int { return max(1, m.transcriptWidth()-promptWidth-buttonsWidth) }

// composerRows is how many rows the composer takes: as many as what is
// written needs, from one to a share of the window.
func (m *Model) composerRows() int {
	w := m.composerInner()
	rows := 0
	for _, line := range strings.Split(m.chat.showLabels(m.input.Value()), "\n") {
		rows += max(1, (ansi.StringWidth(line)+w)/w)
	}
	return min(max(1, rows), max(1, m.height*composerShare/100))
}

// bottomRows is how many rows are below the conversation: the line of the
// status, the line above the composer, what is asked or offered above it,
// the composer, the line of the actions and what waits for a decision.
func (m *Model) bottomRows() int {
	rows := 3 + m.composerRows() + m.pendingRows()
	if panel := m.panelLines(); len(panel) > 0 {
		rows += len(panel) + 1
	}
	return rows
}

// chatHeight is how high the conversation is.
func (m *Model) chatHeight() int { return max(1, m.height-m.headerHeight()-m.bottomRows()) }

// pad fills s with spaces to w cells, cut to w if it is wider.
func pad(s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-lipgloss.Width(s)))
}

// spread puts left at the start and right at the end of w cells; left gives
// way where both do not fit.
func spread(left, right string, w int) string {
	right = ansi.Truncate(right, w, "…")
	left = ansi.Truncate(left, max(0, w-lipgloss.Width(right)-1), "…")
	return left + strings.Repeat(" ", max(0, w-lipgloss.Width(left)-lipgloss.Width(right))) + right
}

// lineStyle draws the lines that part, as the GUI draws its borders.
func lineStyle() lipgloss.Style { return lipgloss.NewStyle().Foreground(ui.line) }

// chatScreen is the whole window of the chat: the three columns side by
// side, each as high as the window.
func (m *Model) chatScreen() string {
	h := m.height
	columns := []string{}
	if w := m.sideWidth(); w > 0 {
		columns = append(columns, m.sidebarView(w, h))
	}
	columns = append(columns, m.mainView())
	if w := m.railWidth(); w > 0 {
		columns = append(columns, m.railView(w, h))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, columns...)
}

// mainView is the column of the conversation: its head, the conversation
// and below it the composer.
func (m *Model) mainView() string {
	w := m.chatWidth()
	tw := m.transcriptWidth()
	left := strings.Repeat(" ", (w-tw)/2)
	var lines []string
	for _, l := range m.headerLines(w - 2*columnPad) {
		lines = append(lines, pad(strings.Repeat(" ", columnPad)+l, w))
	}
	for _, l := range m.historyLines() {
		lines = append(lines, pad(left+l, w))
	}
	for _, l := range m.bottomLines(tw) {
		lines = append(lines, pad(left+l, w))
	}
	for len(lines) < m.height {
		lines = append(lines, strings.Repeat(" ", w))
	}
	return strings.Join(lines[:m.height], "\n")
}

// headerLines is the head of the conversation, w cells wide: what the GUI
// heads its window with, the conversation shown, and where it works with
// which model.
func (m *Model) headerLines(w int) []string {
	c := m.chat
	eyebrow := "OUROBOROS"
	if m.profileOf > 1 {
		eyebrow += fmt.Sprintf(" · %s %d/%d", strings.ToUpper(m.headerName()), m.profileAt, m.profileOf)
	}
	var marks []string
	if n := m.suggestionRequests.unread; n > 0 {
		marks = append(marks, lipgloss.NewStyle().Foreground(warn).Render(fmt.Sprintf("✦ %d SUGGESTIONS", n)))
	}
	if m.status.Debug {
		marks = append(marks, boldStyle.Foreground(ui.background).Background(warn).Render(" DEBUG MODE "))
	}
	if m.status.Yolo {
		marks = append(marks, boldStyle.Foreground(ui.background).Background(accent).Render(" YOLO MODE "))
	}
	head := spread(boldStyle.Foreground(muted).Render(eyebrow), strings.Join(marks, "  "), w)
	back := ""
	if c.contextID != plugin.ThreadID {
		back = threadButton()
	}
	title := spread(strings.ToUpper(m.chatName()), back, w)
	meta := spread(m.metaText(), "", w)
	if m.compact() {
		return []string{head, title, meta}
	}
	return []string{"", head, title, meta, ""}
}

// threadButton is what takes the owner back to the thread, as the GUI's
// "To the thread" does.
func threadButton() string {
	return lipgloss.NewStyle().Foreground(muted).Background(ui.field).Render(" To the thread ")
}

// chatName is what the head calls the conversation shown.
func (m *Model) chatName() string {
	c := m.chat
	switch {
	case c.contextID == plugin.ThreadID:
		return "Thread"
	case c.peer != "":
		return c.peer + " · " + cmp.Or(c.title, "conversation")
	}
	return cmp.Or(short(c.title, 80), "New conversation")
}

// metaText is the line below the name of the conversation: where it works,
// and with which model, or what the node needs first.
func (m *Model) metaText() string {
	s := m.status
	parts := []string{cmp.Or(m.chat.directory, "Profile workspace")}
	switch {
	case !s.Configured:
		return mutedStyle.Render(parts[0]+" · ") + lipgloss.NewStyle().Foreground(warn).Render("○ not configured") + mutedStyle.Render("  type /setup")
	case s.Running:
		if s.Debug {
			parts = append(parts, fmt.Sprintf("%s:%d", s.ID, s.Port))
		}
		if model := strings.TrimLeft(m.modelText(), " ·"); model != "" { // the effort without a model
			parts = append(parts, model)
		}
	default:
		parts = append(parts, "type /start")
	}
	return mutedStyle.Render(strings.Join(parts, " · "))
}

// historyLines are the rows of the conversation as the column shows them,
// the selection marked and the older lines faded out at the top. A node that
// is not set up has none yet: it shows how to set it up.
func (m *Model) historyLines() []string {
	tw, h := m.transcriptWidth(), m.chatHeight()
	if !m.status.Configured {
		hint := mutedStyle.Render("Type /setup to set up your node and chat with its agent.")
		lines := []string{hint}
		if tw >= logoWidth() && h >= len(logoLines)+2 {
			lines = append(strings.Split(lipgloss.NewStyle().Foreground(accent).Render(logoText(logoLines, tw)), "\n"), "", hint)
		}
		for len(lines) < h {
			lines = append(lines, "")
		}
		return lines[:h]
	}
	lines := m.highlight(strings.Split(m.history.View(), "\n"))
	lines = append(lines, make([]string, max(0, h-len(lines)))...)[:h] // the viewport is as high once laid out
	if m.history.YOffset() > 0 {
		m.fadeTop(lines)
	}
	return lines
}

// fadeTop fades the older lines out towards the top, as the GUI does while
// there are older ones above: over the upper two fifths, the first line
// nearly the background.
func (m *Model) fadeTop(lines []string) {
	n := len(lines) * 2 / 5
	for i := 0; i < n && i < len(lines); i++ {
		p := float64(i) / float64(n)
		cover := 0.7 * (1 - (p-0.14)/0.86)
		if p < 0.14 {
			cover = 1 - p/0.14*0.3
		}
		lines[i] = fadeLine(lines[i], cover)
	}
}

// fadeLine is line with each of its colors the share cover of the way to
// the background, as a translucent layer of the background over it looks.
// What has no color of its own has that of the text.
func fadeLine(line string, cover float64) string {
	toward := func(c color.Color) string {
		r, g, b, _ := blend(ui.background, c, cover).RGBA()
		return fmt.Sprintf("2;%d;%d;%d", r>>8, g>>8, b>>8)
	}
	text := "38;" + toward(ui.text)
	faded := sgr.ReplaceAllStringFunc(line, func(seq string) string {
		params := strings.FieldsFunc(seq[2:len(seq)-1], func(r rune) bool { return r == ';' || r == ':' })
		if len(params) == 0 {
			return "\x1b[0;" + text + "m"
		}
		var out []string
		for i := 0; i < len(params); i++ {
			switch p := params[i]; {
			case (p == "38" || p == "48") && i+4 < len(params) && params[i+1] == "2":
				var r, g, b uint8
				fmt.Sscan(params[i+2], &r)
				fmt.Sscan(params[i+3], &g)
				fmt.Sscan(params[i+4], &b)
				out = append(out, p+";"+toward(color.RGBA{r, g, b, 0xff}))
				i += 4
			case (p == "38" || p == "48") && i+2 < len(params) && params[i+1] == "5":
				if p == "38" {
					out = append(out, text)
				}
				i += 2
			case p == "0" || p == "39":
				out = append(out, p, text)
			default:
				out = append(out, p)
			}
		}
		return "\x1b[" + strings.Join(out, ";") + "m"
	})
	return "\x1b[" + text + "m" + faded + "\x1b[m"
}

// bottomLines are the rows below the conversation, w cells wide: the line of
// the status, the line above the composer, what is asked or offered, the
// composer, and the line of what happened and the keys.
func (m *Model) bottomLines(w int) []string {
	divider := lineStyle().Render(strings.Repeat("─", w))
	lines := []string{m.statusLine(w), divider}
	if panel := m.panelLines(); len(panel) > 0 {
		lines = append(lines, panel...)
		lines = append(lines, divider)
	}
	lines = append(lines, m.composerLines(w)...)
	hint := m.footerHint()
	if m.inDialog() {
		hint = "" // a dialog says its keys below itself
	}
	lines = append(lines, spread(m.chatTitle(), hint, w))
	if m.pendingRows() > 0 {
		lines = append(lines, m.suggestionRequests.pendingView(m, w))
	}
	return lines
}

// statusLine says on the right where the conversation stands, as the GUI
// says it above its composer, and on the left a picture moves while an
// answer is on its way.
func (m *Model) statusLine(w int) string {
	c := m.chat
	text := "Peer session · read only"
	if c.peer == "" {
		kind, state := "Session", "Ready"
		if c.contextID == plugin.ThreadID {
			kind = "Thread"
		}
		if label := m.waiting(); label != "" {
			state = label
		}
		if a := c.asking; a != nil && !a.sent {
			state = "Needs your answer"
		}
		text = kind + " · " + state
		if c.contextID == plugin.ThreadID {
			n := m.activeSessions()
			text += fmt.Sprintf(" · %d active sessions", n)
			if n == 1 {
				text = strings.TrimSuffix(text, "s")
			}
		}
	}
	label := mutedStyle.Render(ansi.Truncate(strings.ToUpper(text), w, "…"))
	room := w - lipgloss.Width(label) - 2
	if m.waiting() == "" || room <= 0 {
		return pad(strings.Repeat(" ", max(0, w-lipgloss.Width(label)))+label, w)
	}
	art := arts[c.art%len(arts)].row(room, m.ticks)
	return lipgloss.NewStyle().Foreground(faded(muted, 0.4)).Render(string(art)) + "  " + label
}

// activeSessions is how many sessions work besides the thread.
func (m *Model) activeSessions() int {
	n := 0
	for _, c := range m.chats {
		if c.contextID != plugin.ThreadID && c.running {
			n++
		}
	}
	return n
}

// composerLines is the composer, w cells wide: the prompt, the field and on
// its last row the buttons that stop and send.
func (m *Model) composerLines(w int) []string {
	inner := m.composerInner()
	field := strings.Split(m.chat.showLabels(m.input.View()), "\n")
	stop := strings.Repeat(" ", stopWidth)
	if m.chat.pending && m.chat.peer == "" {
		stop = lipgloss.NewStyle().Foreground(bad).Background(ui.errBackground).Render(" Stop ")
	}
	send := lipgloss.NewStyle().Foreground(muted).Background(ui.field).Render(" ↑ ")
	if m.canSend() {
		send = boldStyle.Foreground(accent).Background(ui.hover).Render(" ↑ ")
	}
	lines := make([]string, len(field))
	for i, f := range field {
		prompt := "  "
		if i == 0 {
			prompt = mutedStyle.Render("› ")
		}
		buttons := strings.Repeat(" ", buttonsWidth)
		if i == len(field)-1 {
			buttons = " " + stop + " " + send
		}
		lines[i] = pad(prompt+pad(f, inner)+buttons, w)
	}
	return lines
}

// canSend reports whether what is written can be sent.
func (m *Model) canSend() bool {
	return m.status.Configured && m.chat.peer == "" && strings.TrimSpace(m.input.Value()) != ""
}

// panelHeight reserves the composer, controls and one history row before
// assigning space to questions and command suggestions.
func (m *Model) panelHeight() int {
	available := m.height - m.headerHeight() - (3 + m.composerRows() + m.pendingRows()) - 2
	return max(0, available)
}

// panelLines is what lies above the composer: the question of the agent
// with its answers, as the GUI asks for a decision, or the commands that fit
// what is typed. None without either.
func (m *Model) panelLines() []string {
	lines, _ := m.panel()
	return lines
}

// panel is what lies above the composer, as panelLines says, and where a
// click on it does something: on a command, it runs it.
func (m *Model) panel() ([]string, []hit) {
	w := m.transcriptWidth()
	var lines []string
	var hits []hit
	if a := m.chat.asking; a != nil {
		lines, hits = m.questionLines(a, w)
	}
	if found, _ := m.suggestions(); len(found) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		suggested, top := m.suggestionLines()
		_, selected := m.suggestions()
		room := min(len(suggested), max(0, m.panelHeight()-len(lines)))
		start := max(0, min(selected-top-room+1, len(suggested)-room))
		suggested, top = suggested[start:start+room], top+start
		for i, line := range suggested {
			hits = append(hits, onLine(len(lines), func() tea.Cmd {
				m.suggest = top + i
				cmd, _ := m.handleSuggestionKey(enterKey)
				return cmd
			}))
			lines = append(lines, pad(strings.TrimPrefix(line, "  "), w))
		}
	}
	limit := min(len(lines), m.panelHeight())
	return lines[:limit], shifted(hits, 0, 0, limit)
}

// runningSessions are the sessions that run now, besides the thread: what the
// sidebar lists and the column on the right says of, wherever the
// conversation stands. One that ends leaves both.
//
// A session keeps its place among them while it runs, as the place it got
// when it began to run says, so that neither column changes its order while
// the sessions work: a session that ended and begins anew stands last.
func (m *Model) runningSessions() []*conversation {
	var fresh, list []*conversation
	order := make(map[string]int, len(m.runOrder))
	for _, s := range m.chats {
		if s.contextID == plugin.ThreadID || s.peer != "" || !s.running {
			continue
		}
		n, ok := m.runOrder[s.contextID]
		if !ok {
			fresh = append(fresh, s)
		}
		order[s.contextID] = n
		list = append(list, s)
	}
	// Those that began to run in the same moment stand in the order of what
	// they did last, then of their ID, as nothing else tells them apart.
	slices.SortFunc(fresh, func(a, b *conversation) int {
		return cmp.Or(a.last.Compare(b.last), strings.Compare(a.contextID, b.contextID))
	})
	for _, s := range fresh {
		order[s.contextID] = m.runNext
		m.runNext++
	}
	m.runOrder = order // one that ended gives its place up
	slices.SortFunc(list, func(a, b *conversation) int {
		return cmp.Or(order[a.contextID]-order[b.contextID], strings.Compare(a.contextID, b.contextID))
	})
	return list
}

// sideRow is a row of the sidebar, and the session it shows, if it does.
type sideRow struct {
	text string
	c    *conversation
}

// sidebarRows are the rows of the list of the sidebar, inner cells wide: the
// sessions that run now, oldest change last. Nothing else is listed, and
// where the thread is scrolled changes nothing.
func (m *Model) sidebarRows(inner int) []sideRow {
	rows := []sideRow{{text: boldStyle.Foreground(muted).Render("RUNNING")}, {}}
	for _, c := range m.runningSessions() {
		rows = append(rows, m.sessionItem(c, inner)...)
	}
	if len(rows) == 2 {
		rows = append(rows, sideRow{text: mutedStyle.Render("Nothing runs.")})
	}
	return rows
}

// sessionItem is a session that runs now in the sidebar: its name, that it
// works, a bar that runs with it, and the line below it. The one shown
// stands out.
func (m *Model) sessionItem(c *conversation, inner int) []sideRow {
	selected := c == m.chat
	// The one shown is on the background of the selection, all of its width.
	on := func(st lipgloss.Style) lipgloss.Style {
		if selected {
			return st.Background(ui.selected)
		}
		return st
	}
	title := on(lipgloss.NewStyle().Bold(true))
	if selected {
		title = title.Foreground(accent)
	}
	when := ""
	if !c.last.IsZero() {
		when = " · " + c.last.Format("2 Jan 2006, 15:04")
	}
	bar := make([]rune, inner)
	for x := range bar {
		bar[x] = '▪'
		if (x-m.ticks/2)%3 == 0 { // the dashes run while it works
			bar[x] = ' '
		}
	}
	border := lineStyle()
	if selected {
		border = lipgloss.NewStyle().Foreground(accent)
	}
	return []sideRow{
		{title.Render(pad(strings.ToUpper(cmp.Or(c.title, "new conversation")), inner)), c},
		{on(mutedStyle).Render(pad("State: working · own"+when, inner)), c},
		{on(lipgloss.NewStyle().Foreground(accent)).Render(string(bar)), c},
		{border.Render(strings.Repeat("─", inner)), c},
		{},
	}
}

// sidebarLayout is where the rows of the sidebar go in a column h rows high:
// the rows that fit, from the first of them shown, and the row of the screen
// they begin at. As in the GUI, the list sits between the top and the button
// of the settings at the bottom, and it scrolls to the session shown.
func (m *Model) sidebarLayout(inner, h int) (rows []sideRow, top int) {
	rows = m.sidebarRows(inner)
	most := max(4, h*48/100+2)
	if len(rows) > most {
		at := slices.IndexFunc(rows, func(r sideRow) bool { return r.c == m.chat })
		first := 2 // the heading stays
		if at > first+most-6 {
			first = min(at-2, len(rows)-(most-2))
		}
		rows = append(rows[:2:2], rows[first:first+most-2]...)
	}
	free := max(0, h-3-len(rows)) // the margins and the button below
	return rows, 1 + free/2
}

// sidebarView is the sidebar, w cells wide and h rows high.
func (m *Model) sidebarView(w, h int) string {
	inner := max(1, w-2*columnPad)
	rows, top := m.sidebarLayout(inner, h)
	margin := strings.Repeat(" ", columnPad)
	lines := make([]string, h)
	for i := range lines {
		lines[i] = strings.Repeat(" ", w)
	}
	for i, r := range rows {
		if y := top + i; y < h-2 {
			lines[y] = pad(margin+r.text, w)
		}
	}
	if h >= 2 {
		lines[h-2] = pad(margin+mutedStyle.Render(gear), w)
	}
	return strings.Join(lines, "\n")
}

// gear is the button of the settings at the bottom of the sidebar, as the
// GUI has it: drawn as text, not as a picture twice as wide.
const gear = "⚙\ufe0e"

// sidebarAt is the session the sidebar shows at the row y of the screen,
// nil for none.
func (m *Model) sidebarAt(y int) *conversation {
	w := m.sideWidth()
	if w == 0 {
		return nil
	}
	rows, top := m.sidebarLayout(max(1, w-2*columnPad), m.height)
	if i := y - top; i >= 0 && i < len(rows) && y < m.height-2 {
		return rows[i].c
	}
	return nil
}

// clock is the time t as the GUI writes it above a message: the time of
// day today, the date with it before.
func clock(t, now time.Time) string {
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("2 Jan 2006, 15:04")
}

// since says how long ago a time was, as the facts of a session show it:
// seconds below a minute, minutes below an hour, hours and minutes above.
func since(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// railView is the column on the right, w cells wide and h rows high: beside
// the thread where the sessions that run stand, beside a session of its own
// the session shown in more detail, or the jobs once ctrl+j lists them
// there. As in the GUI, it stands at the bottom of its column.
func (m *Model) railView(w, h int) string {
	inner := max(1, w-2*columnPad)
	content, _, top := m.railLayout(inner, h)
	margin := strings.Repeat(" ", columnPad)
	lines := make([]string, h)
	for i := range lines {
		lines[i] = strings.Repeat(" ", w)
	}
	for i, l := range content {
		if y := top + i; y < h {
			lines[y] = pad(margin+ansi.Truncate(l, inner, "…"), w)
		}
	}
	return strings.Join(lines, "\n")
}

// railHead is how many lines of the column on the right stand above its
// details: its heading and the line below it.
const railHead = 2

// railLayout is what the column on the right shows, in a column h rows
// high, the session of each row and the row it begins at: the details cut
// where they would take more than its room.
func (m *Model) railLayout(inner, h int) (content []string, owners []*conversation, top int) {
	content, owners = m.railRows(inner)
	most := max(1, h*58/100)
	if len(content) > railHead+most {
		content = append(content[:railHead+most-1], mutedStyle.Render("…"))
		owners = owners[:len(content)]
	}
	return content, owners, max(0, h-2-len(content))
}

// railRows are the lines of the column on the right and the session of each,
// nil for a line that shows none: the sessions that run now beside the
// thread, the session shown in detail beside itself, or the jobs once ctrl+j
// lists them there.
func (m *Model) railRows(inner int) (lines []string, owners []*conversation) {
	title := "RUNNING"
	switch {
	case m.jobsShown:
		title = "JOBS"
	case m.detailShown():
		title = "SESSION"
	}
	add := func(text string, c *conversation) {
		lines, owners = append(lines, text), append(owners, c)
	}
	add(boldStyle.Foreground(muted).Render(title), nil)
	add(lineStyle().Render(strings.Repeat("─", inner)), nil)
	if m.jobsShown {
		for _, l := range m.jobLines(inner) {
			add(l, nil)
		}
		return lines, owners
	}
	if m.detailShown() {
		for _, l := range m.sessionDetail(m.chat, inner) {
			add(l, m.chat)
		}
		return lines, owners
	}
	list := m.runningSessions()
	if len(list) == 0 {
		add(mutedStyle.Render("Nothing runs."), nil)
		return lines, owners
	}
	for i, s := range list {
		if i > 0 {
			add("", nil)
		}
		for _, l := range m.artefact(s, inner) {
			add(l, s)
		}
	}
	return lines, owners
}

// detailShown reports whether the column on the right shows the session shown
// in detail: a session of the owner's own, not the thread, which lists the
// sessions that run beside it. A conversation of a peer is only to read and
// lists them too.
func (m *Model) detailShown() bool {
	return m.chat.contextID != plugin.ThreadID && m.chat.peer == ""
}

// railAt is the session the column on the right shows at the row y of the
// screen, nil for none: a click on it shows it, as one in the sidebar does.
func (m *Model) railAt(y int) *conversation {
	w := m.railWidth()
	if w == 0 {
		return nil
	}
	_, owners, top := m.railLayout(max(1, w-2*columnPad), m.height)
	if i := y - top; i >= railHead && i < len(owners) {
		return owners[i]
	}
	return nil
}

// jobLines lists the jobs of the session shown, the tasks it delegated and
// the services that run past the answers, inner cells wide.
func (m *Model) jobLines(inner int) []string {
	heading := func(s string) string { return boldStyle.Render(strings.ToUpper(s)) }
	wrap := func(text string) []string {
		wrapped := strings.Split(lipgloss.Wrap(text, max(1, inner-2), ""), "\n")
		for i := 1; i < len(wrapped); i++ {
			wrapped[i] = "  " + wrapped[i]
		}
		return wrapped
	}
	var lines []string
	if len(m.chat.jobs) == 0 {
		lines = append(lines, mutedStyle.Render("No command runs in the background."))
	}
	for _, job := range m.chat.jobs {
		lines = append(lines, wrap(job)...)
	}
	if len(m.chat.tasks) > 0 {
		lines = append(lines, "", heading("Tasks"))
	}
	for _, task := range m.chat.tasks {
		lines = append(lines, wrap(task)...)
	}
	if len(m.services) > 0 {
		lines = append(lines, "", heading("Services"))
	}
	for _, sv := range m.services {
		text := sv.Name
		if sv.PeerKey != "" {
			text += " · " + sv.Peer
		}
		if sv.Port > 0 {
			text += fmt.Sprintf(" · :%d", sv.Port)
		}
		lines = append(lines, wrap(text)...)
		for _, l := range wrap(sv.Command) {
			lines = append(lines, mutedStyle.Render("  "+l))
		}
	}
	return lines
}

// stand is where a session stands, as its events tell: the phase of the
// loop to its answer, the call it is in, the one it did last, and what comes
// next. See sessionPhase in the app.js of the GUI.
type stand struct {
	phase, word, what, next, question string
	live, bad                         bool
	last, pending                     *call
	calls                             []call
}

// standOf is where the session c stands.
func standOf(c *conversation) stand {
	var s stand
	for i := range c.entries {
		for j := range c.entries[i].calls {
			call := c.entries[i].calls[j]
			if call.done {
				s.calls = append(s.calls, call)
				s.last = &s.calls[len(s.calls)-1]
			} else {
				s.pending = &c.entries[i].calls[j]
			}
		}
	}
	if s.last != nil { // the slice may have grown since
		s.last = &s.calls[len(s.calls)-1]
	}
	working := c.pending || c.running
	if !working {
		s.pending = nil
	}
	switch {
	case c.asking != nil && !c.asking.sent:
		s.phase, s.question = "asked", c.asking.question
	case working && s.pending != nil:
		s.phase = "tool"
	case working && c.partial != "":
		s.phase = "answering"
	case working:
		s.phase = "thinking"
	case len(c.entries) == 0:
		s.phase = "empty"
	default:
		last := c.entries[len(c.entries)-1]
		s.phase = "done"
		if last.failed {
			s.phase = "failed"
			if last.text == "stopped before it answered" {
				s.phase = "canceled"
			}
		}
	}
	s.live = working || s.phase == "asked"
	s.bad = s.phase == "failed"
	s.word = map[string]string{"asked": "asks you", "tool": "calls", "thinking": "thinks", "answering": "writes the answer",
		"done": "done", "failed": "failed", "canceled": "canceled", "empty": "nothing yet"}[s.phase]
	s.next = "nothing"
	switch s.phase {
	case "asked":
		s.next = "your answer"
	case "tool":
		tool := cmp.Or(s.pending.tool, "tool")
		s.next = "the result of " + tool
		s.what = strings.TrimPrefix(s.pending.text, tool+" · ")
		if s.what == tool {
			s.what = ""
		}
	case "answering":
		s.next = "the end of the answer"
	case "thinking":
		s.next = "a tool or the answer"
	}
	return s
}

// The lanes of the chart of the tools of a session: at most so many, one
// per tool, the most used strongest.
var laneShades = []float64{1, 0.74, 0.55, 0.4, 0.28, 0.18}

// artefact is a session as the column on the right shows it, inner cells
// wide: its name, where it stands, what it did last and what it does next,
// and the chart of its tools under it.
func (m *Model) artefact(c *conversation, inner int) []string {
	s := standOf(c)
	lines := m.standHead(c, s, inner)
	lines = append(lines, m.standFacts(c, s, inner)...)
	return append(lines, m.toolChart(s, inner)...)
}

// standHead is the name of a session and where it stands, inner cells wide:
// its name, whether it works, what it does and when it last did something.
func (m *Model) standHead(c *conversation, s stand, inner int) []string {
	name := strings.ToUpper(cmp.Or(c.title, "new conversation"))
	if s.live {
		name += " · WORKING"
	}
	dot := lipgloss.NewStyle().Foreground(ui.lineStrong)
	word := boldStyle
	switch {
	case s.bad:
		dot, word = errStyle, boldStyle.Foreground(bad)
	case s.live:
		dot = lipgloss.NewStyle().Foreground(accent)
		if m.ticks/8%2 == 1 { // it beats while the session works
			dot = lipgloss.NewStyle().Foreground(faded(accent, 0.3))
		}
	}
	state := dot.Render("●") + " " + word.Render(s.word)
	if s.what != "" {
		state += " " + mutedStyle.Render(s.what)
	}
	return []string{
		boldStyle.Render(ansi.Truncate(name, inner, "…")),
		spread(state, mutedStyle.Render(clock(c.last, m.now())), inner),
	}
}

// standFacts is what a session does in the round it is in, inner cells wide:
// what it asks, what it did last, what comes next, and which of its commands
// run.
func (m *Model) standFacts(c *conversation, s stand, inner int) []string {
	fact := func(key, value, at string, bad bool) string {
		v := lipgloss.NewStyle()
		if bad {
			v = errStyle
		}
		return spread(mutedStyle.Render(fmt.Sprintf("%-6s", key))+v.Render(value), mutedStyle.Render(at), inner)
	}
	var lines []string
	if s.question != "" {
		lines = append(lines, fact("question", " "+short(s.question, inner), "", false))
	}
	if s.last != nil {
		lines = append(lines, fact("last", s.last.text, clock(s.last.at, m.now()), s.last.failed))
	} else {
		lines = append(lines, fact("last", "no call yet", "", false))
	}
	lines = append(lines, fact("next", s.next, "", false))
	if len(c.jobs) > 0 {
		job := c.jobs[0]
		if len(c.jobs) > 1 {
			job += " …"
		}
		lines = append(lines, fact("running", " "+job, "", false))
	}
	return lines
}

// sessionDetail is the session shown as the column on the right says of it
// beside itself, inner cells wide: where it stands, what it works with, what
// it did step by step and what runs for it. Beside the thread, where all the
// sessions that run are listed, a session says only where it stands; here it
// has the column to itself and says more.
func (m *Model) sessionDetail(c *conversation, inner int) []string {
	s := standOf(c)
	lines := m.standHead(c, s, inner)
	lines = append(lines, m.standFacts(c, s, inner)...)
	rule := func() { lines = append(lines, lineStyle().Render(strings.Repeat("─", inner))) }
	// A key and its value, the value hanging below its key where it does not
	// fit beside it.
	fact := func(key, value string) {
		lines = append(lines, hang(mutedStyle.Render(fmt.Sprintf("%-8s", key)), strings.Repeat(" ", 8), value, inner)...)
	}
	rule()
	fact("project", cmp.Or(c.directory, "Profile workspace"))
	if m.status.Configured && m.status.Model != "" {
		fact("model", m.modelText())
	}
	if c.round > 0 {
		fact("round", fmt.Sprintf("%d of the loop", c.round))
	}
	if s.live && !c.since.IsZero() {
		fact("since", fmt.Sprintf("%s · %s", clock(c.since, m.now()), since(m.now().Sub(c.since))))
	}
	if c.context > 0 {
		fact("context", contextSize(c.context))
	}
	if c.cache.Tokens > 0 {
		fact("cached", cacheRate(c.cache))
	}
	if metrics := callMetrics(c.cache); metrics != "" {
		fact("speed", metrics)
	}
	if len(c.queued) > 0 {
		queued := fmt.Sprintf("%d messages", len(c.queued))
		if len(c.queued) == 1 {
			queued = "1 message"
		}
		fact("queued", queued)
	}
	if len(c.jobs) > 0 || len(c.tasks) > 0 || len(m.services) > 0 {
		rule()
		lines = append(lines, boldStyle.Render("WORK"))
		for i, l := range m.jobLines(inner) {
			if i == 0 && len(c.jobs) == 0 && strings.HasPrefix(ansi.Strip(l), "No command runs") {
				continue // the work is the tasks or the services
			}
			lines = append(lines, l)
		}
	}
	if calls := m.callLines(c, s, 12, inner); len(calls) > 0 {
		rule()
		lines = append(lines, boldStyle.Render("CALLS"))
		lines = append(lines, calls...)
	}
	return lines
}

// callLines are the calls of the session c, the newest first and the one on
// its way above them all, at most n of them, inner cells wide: each with the
// tool and what it did, and when, as the chart shows them all at once.
func (m *Model) callLines(c *conversation, s stand, n, inner int) []string {
	all := s.calls
	if s.pending != nil {
		all = append(slices.Clone(all), *s.pending)
	}
	var lines []string
	for i := len(all) - 1; i >= 0 && len(lines) < n; i-- {
		call := all[i]
		if call.text == "" && call.tool == "" {
			continue // nothing is shown of it
		}
		text := call.text
		if text == "" {
			text = call.tool
		}
		style := lipgloss.NewStyle()
		if call.failed {
			style = errStyle
		}
		mark := "  "
		if s.pending != nil && i == len(all)-1 {
			mark = mutedStyle.Render("→ ") // the one on its way
		}
		at := ""
		if !call.at.IsZero() {
			at = clock(call.at, m.now())
		}
		lines = append(lines, spread(mark+style.Render(text), mutedStyle.Render(at), inner))
	}
	return lines
}

// toolChart draws the tools of a session as one picture instead of their
// calls one by one: a lane per tool, its calls where their time puts them,
// so that its mix and its rhythm read under the state. A call on its way
// blinks, and a dashed line marks where now is.
func (m *Model) toolChart(s stand, inner int) []string {
	all := s.calls
	if s.pending != nil {
		all = append(slices.Clone(all), *s.pending)
	}
	if len(all) == 0 {
		return nil
	}
	name := func(c call) string { return cmp.Or(c.tool, strings.Split(c.text, " · ")[0], "tool") }
	tally := map[string]int{}
	var tools []string // every tool, in the order in which it was used first
	for _, c := range all {
		if tally[name(c)] == 0 {
			tools = append(tools, name(c))
		}
		tally[name(c)]++
	}
	// The lanes are the tools used most; among them each keeps the place and
	// the shade it got when it was used first, so that the chart does not
	// jump about while the session works.
	most := slices.Clone(tools)
	slices.SortStableFunc(most, func(a, b string) int { return tally[b] - tally[a] })
	most = most[:min(len(most), len(laneShades))]
	var lanes []string
	for _, tool := range tools {
		if slices.Contains(most, tool) {
			lanes = append(lanes, tool)
		}
	}
	const labels, counts = 9, 3
	plot := inner - labels - counts
	if plot < 4 {
		return nil
	}
	var first, last time.Time
	for _, c := range all {
		if c.at.IsZero() {
			continue
		}
		if first.IsZero() || c.at.Before(first) {
			first = c.at
		}
		if c.at.After(last) {
			last = c.at
		}
	}
	if s.live && m.now().After(last) {
		last = m.now()
	}
	span := last.Sub(first)
	at := func(c call, i int) int {
		offset := float64(i) / float64(max(1, len(all)))
		if span > 0 && !c.at.IsZero() {
			offset = float64(c.at.Sub(first)) / float64(span)
		}
		return min(plot-1, max(0, int(offset*float64(plot-1)+0.5)))
	}
	var lines []string
	for lane, tool := range lanes {
		shade := laneShades[lane]
		row := make([]string, plot)
		base := lipgloss.NewStyle().Foreground(faded(accent, 0.22*shade+0.12)).Render("─")
		for x := range row {
			row[x] = base
		}
		if s.live {
			row[plot-1] = lipgloss.NewStyle().Foreground(faded(accent, 0.5)).Render("┊")
		}
		tick := lipgloss.NewStyle().Foreground(faded(accent, shade)).Render("┃")
		for i, c := range all {
			if name(c) != tool {
				continue
			}
			pending := s.pending != nil && i == len(all)-1
			if pending && s.live && m.ticks/6%2 == 1 {
				continue // it blinks while it is on its way
			}
			row[at(c, i)] = tick
		}
		label := ansi.Truncate(tool, labels-1, "…")
		lines = append(lines, lipgloss.NewStyle().Foreground(accent).Render(pad(label, labels))+strings.Join(row, "")+
			lipgloss.NewStyle().Foreground(accent).Render(fmt.Sprintf("%*d", counts, tally[tool])))
	}
	summary := fmt.Sprintf("%d tools · %d kinds", len(all), len(tools))
	if len(all) == 1 {
		summary = strings.Replace(summary, "tools", "tool", 1)
	}
	if len(tools) == 1 {
		summary = strings.TrimSuffix(summary, "s")
	}
	if len(tools) > len(lanes) {
		summary += fmt.Sprintf(" · %d lanes", len(lanes))
	}
	return append(lines, mutedStyle.Render(summary))
}
