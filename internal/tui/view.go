package tui

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"image/color"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The colors of the UI are those of the guiPalette of the GUI, see guiPalette.go:
// these are what is drawn with, set by applyPalette.
var (
	accent color.Color
	peer   color.Color
	muted  color.Color
	good   color.Color
	bad    color.Color
	warn   color.Color

	mutedStyle lipgloss.Style
	imageStyle lipgloss.Style // the label of a picture, like [Image_1]
	errStyle   lipgloss.Style
	boldStyle  = lipgloss.NewStyle().Bold(true)
)

// borderMuted is the color of a box that does not have focus: the color of
// the lines of the GUI.
func borderMuted(bool) color.Color { return ui.line }

// box is a frame as the dialogs of the GUI have it: rounded, in the color of
// its lines, a stronger one for what has the focus.
func box(focused, _ bool) lipgloss.Style {
	color := ui.line
	if focused {
		color = ui.lineStrong
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(color).Padding(0, 1)
}

// inputStyles is the style of the composer: no background on the line the
// cursor is on, the cursor in the accent, and what it shows when empty in
// the muted color.
func inputStyles(light bool) textarea.Styles {
	s := textarea.DefaultStyles(!light)
	s.Cursor.Color = accent
	for _, st := range []*textarea.StyleState{&s.Focused, &s.Blurred} {
		st.CursorLine = lipgloss.NewStyle()
		st.Base = lipgloss.NewStyle()
		st.Text = lipgloss.NewStyle().Foreground(ui.text)
		st.Placeholder = lipgloss.NewStyle().Foreground(muted)
		st.EndOfBuffer = lipgloss.NewStyle().Foreground(ui.background)
	}
	return s
}

// bodyHeight is the height between the header and the footer of a screen
// that takes the whole window, and the line below the footer if something
// is pending.
func (m *Model) bodyHeight() int { return max(0, m.height-2-m.pendingRows()) }

// pendingRows is how many rows the line below the composer takes: one while
// something waits for a decision, except on the screen that lists it.
func (m *Model) pendingRows() int {
	if m.suggestionRequests.trust.waiting() == 0 || m.screen == screenSuggestions {
		return 0
	}
	return 1
}

// pendingView is the line below the composer, w cells wide: how much waits
// for a decision, where to decide, and what it is. The command comes before
// what waits, so that a narrow window cuts off the latter.
func (r *requestsScreen) pendingView(m *Model, w int) string {
	n := len(r.trust.Suggestions)
	what := "a review suggests a change of ouro"
	if n > 1 {
		what = fmt.Sprintf("reviews suggest %d changes of ouro", n)
	}
	line := lipgloss.NewStyle().Foreground(warn).Bold(true).Render(fmt.Sprintf("● %d suggestions", n)) +
		mutedStyle.Render(" · type ") + lipgloss.NewStyle().Foreground(accent).Render("/suggestions") +
		mutedStyle.Render(" to decide · "+what)
	return ansi.Truncate(line, w, "…")
}

// layout sizes the widgets after the window, or what is below the
// conversation, changed.
func (m *Model) layout() {
	atBottom := m.history.AtBottom()
	// A history on its way to its end goes on to where its end is now: the
	// composer moves it, as it grows and shrinks, while an answer and what
	// follows it come in.
	toEnd := m.glide != nil && m.glide.view == &m.history && m.glide.target == topLine(&m.history)
	width := m.transcriptWidth()
	if m.history.Width() != width {
		m.selected, m.pressed = nil, nil // its lines are wrapped anew
	}
	m.input.SetWidth(m.composerInner())
	m.input.SetHeight(m.composerRows())
	m.history.SetWidth(width)
	m.history.SetHeight(m.chatHeight())
	m.renderHistory(atBottom && !toEnd)
	if toEnd {
		m.glideTo(&m.history, topLine(&m.history)) // on its way already: no frame to ask for
	}
	m.context.size(m)
}

// renderHistory fills the history viewport with the conversation, the loop of
// every answer included. Each message is headed as the GUI heads it, who
// wrote it and when, its text below the head, and a blank line between two
// of them.
func (m *Model) renderHistory(toBottom bool) {
	c := m.chat
	first := m.threadStart(c)
	m.blockStarts = nil
	if first == len(c.entries) && !c.pending && len(c.queued) == 0 {
		c.shown = rendered{first: first}
		if m.resuming == c {
			m.history.SetContent("")
			return
		}
		art := emptyChat(m.agentName(), m.history.Width(), m.history.Height())
		if a := &m.nameAnimation; a.active {
			art = drawNameAnimation(m.agentName(), m.history.Width(), m.history.Height(), a.elapsed, m.light, a.look, 1, a.tagline)
		}
		m.history.SetContent(art)
		m.stopGlide(&m.history)
		m.history.GotoTop()
		return
	}
	width := m.history.Width()
	body := lipgloss.NewStyle().Width(width)
	// What the agent says is markdown, and shown formatted. Every block is
	// wrapped here and not by the viewport, which would measure all of the
	// history on every step of scrolling.
	render := func(s string) string { return renderMarkdown(s, width) }
	wrap := func(s string) string { return lipgloss.Wrap(s, width, "") }
	summaries := c.summaries()
	// owners are the entries the blocks are of, a block each: which part of
	// the thread a line is in, see sectionShown.
	var owners []int
	// one adds what the chat shows of the entry i to blocks and says whether
	// the last block there is the words of a round of an answer that is in,
	// below which the line of its calls goes.
	one := func(i int, blocks []string, words bool) ([]string, bool) {
		if i < first {
			return blocks, words
		}
		e := c.entries[i]
		if e.reveal != nil {
			e.text = string(e.reveal.runes[:e.reveal.shown])
		}
		add := func(block string) { blocks, owners = append(blocks, block), append(owners, i) }
		// The report of a task is a line of its own, as the GUI writes what
		// the agent handed to a session: what the agent makes of it is its
		// answer.
		if e.task {
			line := mutedStyle.Render("↳ " + strings.ToUpper(e.text))
			if e.failed {
				line = errStyle.Render("↳ " + strings.ToUpper(e.text))
			}
			add(wrap(line))
			return blocks, false
		}
		// What the review of an answer proposes is a faint line below it:
		// a thought of the agent, not part of the conversation, which waits
		// in /suggestions to be taken or dropped.
		if e.suggestion {
			lines := strings.Split(lipgloss.Wrap(e.text, max(20, width-2), ""), "\n")
			add(mutedStyle.Render("✦ " + strings.Join(lines, "\n  ") + " · /suggestions"))
			return blocks, false
		}
		if !m.thoughtsHidden && e.thought != "" {
			add(wrap(thoughtBlock(e.thought, width)))
			words = false
		}
		// A round of the loop is its words; the calls of all the rounds of
		// an answer are one line below its last words, also while the
		// answer is on its way, drawn as the GUI draws a call: on a line
		// hung from the left. What every call was is on the context screen
		// (/context), and the sessions it concerned are beside the chat.
		if len(e.calls) > 0 {
			if e.text != "" {
				add(wrap(m.entryHead(e) + "\n" + render(e.text)))
				words = true
			}
			if used := summaries[i]; used != "" {
				// The line goes right below the last words of the answer.
				line := wrap(lineStyle().Render("│ ") + c.muted("↳ "+used))
				if words {
					blocks[len(blocks)-1] += "\n" + line
				} else {
					add(line)
				}
				words = false
			}
			return blocks, words
		}
		// Nothing of it to show: an entry without words and without a call of
		// its own would leave a block of only its head, which reads as a
		// message that got lost.
		if strings.TrimSpace(e.text) == "" && !e.mine && !e.failed {
			return blocks, words
		}
		// What the owner wrote stands to the right, as the GUI shows a
		// message of the owner, apart from what the agent answers.
		if e.mine {
			add(mineBlock(m.entryHead(e), c.showLabels(c.labelled(e.text)), width))
			return blocks, false
		}
		text := render(e.text)
		if e.failed {
			text = body.Foreground(bad).Render(e.text)
		}
		add(wrap(m.entryHead(e) + "\n" + text))
		return blocks, false
	}
	// What an answer has written stays as it is: the entries that cannot
	// change anymore are drawn once and kept, so that an answer on its way
	// costs only its own words, however long the conversation is.
	settled, kept := c.settled(), c.shown.n
	blocks, owners := c.shown.blocks, c.shown.owners
	if c.shown.first != first || c.shown.width != width || c.shown.thoughts == m.thoughtsHidden || kept > settled {
		kept, blocks, owners = 0, nil, nil
	}
	words := false // the last block is the words of a round of an answer that is in
	for i := kept; i < settled; i++ {
		blocks, words = one(i, blocks, words)
	}
	c.shown = rendered{first: first, n: settled, width: width, thoughts: !m.thoughtsHidden, blocks: blocks, owners: owners}
	blocks, owners = slices.Clone(blocks), slices.Clone(owners)
	for i := settled; i < len(c.entries); i++ {
		blocks, words = one(i, blocks, words)
	}
	last := len(c.entries) - 1
	// The answer the model is still writing, as far as the stream has brought
	// it; the Answer of the end replaces it with the whole of it.
	if c.pending && c.partial != "" {
		head := boldStyle.Foreground(muted).Render(m.agentName() + " · writing …")
		blocks = append(blocks, wrap(head+"\n"+render(threadIn(c.partial, c.advance())))+mutedStyle.Render(" ▌"))
		owners = append(owners, last)
	}
	// What waits for the answer on its way to take it comes last.
	for i, text := range c.queued {
		state := " · queued"
		if i == len(c.queued)-1 {
			state += ", ↑ takes it back"
		}
		head := boldStyle.Foreground(muted).Render(cmp.Or(c.peer, "You") + state)
		blocks = append(blocks, mineBlock(head, lipgloss.NewStyle().Faint(true).Render(c.showLabels(c.labelled(text))), width))
		owners = append(owners, last)
	}
	line := 0
	for k, b := range blocks {
		m.blockStarts = append(m.blockStarts, blockStart{line, owners[k]})
		line += strings.Count(b, "\n") + 2
	}
	m.history.SetContent(m.behind(strings.Join(blocks, "\n\n")))
	if toBottom {
		m.stopGlide(&m.history) // the bottom is where it goes now
		m.history.GotoBottom()
	}
}

// blockStart is where a block of the history begins, and the entry it is
// of.
type blockStart struct{ line, entry int }

// sectionShown is the part of the thread in view, by the entry of the
// message of the owner that opened it, -1 for what came before the first:
// the part of the line in the middle of the history, or the newest one at
// its end, as the GUI tells it.
func (m *Model) sectionShown() int {
	c := m.chat
	if len(m.blockStarts) == 0 {
		return c.exchangeOf(len(c.entries) - 1)
	}
	if m.followsEnd() {
		return c.exchangeOf(m.blockStarts[len(m.blockStarts)-1].entry)
	}
	mid := m.history.YOffset() + m.history.Height()/2
	entry := m.blockStarts[0].entry
	for _, b := range m.blockStarts {
		if b.line > mid {
			break
		}
		entry = b.entry
	}
	return c.exchangeOf(entry)
}

// exchangeOf is the message of the owner the entry i answers, by its index:
// the last one at or before it, -1 for none.
func (c *conversation) exchangeOf(i int) int {
	for i = min(i, len(c.entries)-1); i >= 0; i-- {
		if c.entries[i].mine {
			return i
		}
	}
	return -1
}

// rendered are the entries of a conversation as the chat shows them, the
// first n of them, wrapped for the width they were drawn at.
type rendered struct {
	first    int      // first entry inside the visible thread window
	n        int      // of the entries, how many are in blocks
	width    int      // the width of the chat they are wrapped for
	thoughts bool     // whether what the model thought was shown then
	blocks   []string // what the chat shows of them, a block for every entry
	owners   []int    // the entry each block is of
}

// settled is how many of the entries of c are shown as they will stay: all of
// them but those of the answer that is on its way, whose words, calls and line
// of calls still change.
func (c *conversation) settled() int {
	for i, e := range c.entries {
		if e.reveal != nil {
			return i
		}
	}
	if !c.pending {
		return len(c.entries)
	}
	for i := len(c.entries) - 1; i >= 0; i-- {
		if c.entries[i].asks() {
			return i
		}
	}
	return 0
}

// The glyphs the newest word of the answer is threaded in with, of the same
// width as the letters they stand for: narrow ones for the letters of Latin,
// wide ones for those of Chinese, Japanese and Korean.
const (
	narrowGlyphs = "▚▞▌▐░▒▓╱╲│┼╪▖▗▘▝"
	wideGlyphs   = "線索縲繧螻雜亂紋風錯綜纏綿"
)

// threadIn shows the answer as far as the stream brought it, with its newest
// word still coming in: at frame 0 all of its letters are glyphs, and fewer
// with every frame until the word stands. What is no letter stays as it is, so
// that the markdown of the answer keeps its shape.
func threadIn(text string, frame int) string {
	if frame >= settleFrames {
		return text
	}
	i := strings.LastIndexFunc(text, unicode.IsSpace)
	word := text[i+1:]
	if word == "" {
		return text
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(word))
	at := h.Sum32()
	rs := []rune(word)
	for j, r := range rs {
		seed := at + uint32(j)*2654435761 + uint32(frame)*40503
		// What is settled stays: of the letters, fewer are noise the further
		// the animation is.
		if (!unicode.IsLetter(r) && !unicode.IsDigit(r)) || seed%settleFrames >= settleFrames-uint32(frame) {
			continue
		}
		rs[j] = glyph(seed, wideRune(r))
	}
	return text[:i+1] + string(rs)
}

// glyph is one of the glyphs of the noise, by the same seed that decided that
// this letter is noise.
func glyph(seed uint32, wide bool) rune {
	pool := []rune(narrowGlyphs)
	if wide {
		pool = []rune(wideGlyphs)
	}
	return pool[seed%uint32(len(pool))]
}

// wideRune is whether r takes two cells of the terminal: the glyph that stands
// for it has to take the same room, or the line jumps while the word comes in.
func wideRune(r rune) bool {
	return r >= 0x1100 && (r <= 0x115f || r >= 0x2e80 && r <= 0xa4cf || r >= 0xac00 && r <= 0xd7a3 ||
		r >= 0xf900 && r <= 0xfaff || r >= 0xfe30 && r <= 0xfe6f || r >= 0xff00 && r <= 0xff60 ||
		r >= 0xffe0 && r <= 0xffe6 || r >= 0x20000)
}

// thoughtKey is the key that shows what the model thought, and hides it
// again.
const thoughtKey = "ctrl+r"

// maxThought is how many of its lines the chat shows of the thinking of a
// round of the loop: the whole of it would push the words away, and the
// answer is what belongs to the chat.
const maxThought = 3

// thoughtBlock is what the model thought before a round of the loop, as the
// chat shows it: its last lines, faint, under a head that says what they are.
func thoughtBlock(text string, width int) string {
	one := strings.Join(strings.Fields(text), " ")
	lines := strings.Split(lipgloss.Wrap(one, max(20, width-4), ""), "\n")
	lines = lines[max(0, len(lines)-maxThought):]
	faint := lipgloss.NewStyle().Faint(true)
	out := []string{mutedStyle.Render("· thought")}
	for _, l := range lines {
		out = append(out, faint.Render("  "+l))
	}
	return strings.Join(out, "\n")
}

// summaries says for every answer, by the index of the round of the loop the
// line goes below, how many tools it used: the last of its rounds, "" for
// the rounds before it. The answer on its way is in it as far as it has
// come.
func (c *conversation) summaries() map[int]string {
	summaries := map[int]string{}
	var calls []call
	last := -1
	end := func() {
		if last >= 0 {
			summaries[last] = c.summary(calls)
		}
		calls, last = nil, -1
	}
	for i, e := range c.entries {
		if e.asks() {
			end()
			continue
		}
		if len(e.calls) > 0 {
			if last >= 0 {
				summaries[last] = ""
			}
			calls, last = append(calls, e.calls...), i
		}
	}
	end()
	return summaries
}

// summary sums up the calls of an answer in one line: how many tools it used,
// and how many of the calls failed. What every call was is on the context
// screen (/context), so that even a long loop stays one line in the chat.
func (c *conversation) summary(calls []call) string {
	failed := 0
	var pictures []string
	for _, call := range calls {
		if call.failed || strings.Contains(call.text, " ✗ ") {
			failed++
		}
		// How large a picture was shown, as the tool that read it said.
		if _, picture, ok := strings.Cut(call.text, " · picture "); ok && !slices.Contains(pictures, picture) {
			pictures = append(pictures, picture)
		}
	}
	line := "Used 1 tool"
	if len(calls) != 1 {
		line = fmt.Sprintf("Used %d tools", len(calls))
	}
	if failed > 0 {
		line += fmt.Sprintf(", %d failed", failed)
	}
	for _, picture := range pictures {
		line += " · " + c.short(picture)
	}
	return line
}

// bubbleShare is how much of the width of the chat a message of the owner
// takes at most, in quarters: the rest stays free on its left, so that what
// the owner wrote stands apart from what the agent answers, as the GUI shows
// it.
const bubbleShare = 3 // of 4

// bubbleWidth is how wide the words of a message of the owner are wrapped in
// a chat width cells wide: most of it, but never all of it, unless the chat
// is too narrow for that.
func bubbleWidth(width int) int { return min(width, max(24, width*bubbleShare/4)) }

// mineBlock lays out a message of the owner in a chat width cells wide: what
// heads it and what it says, wrapped narrower than the chat and laid out to
// the right, the block as wide as its longest line, so that its right edge
// is the right edge of the chat. The head goes with the block: what the
// owner wrote reads as one.
func mineBlock(head, text string, width int) string {
	lines := strings.Split(lipgloss.Wrap(head+"\n"+text, bubbleWidth(width), ""), "\n")
	longest := 0
	for _, l := range lines {
		longest = max(longest, lipgloss.Width(l))
	}
	indent := strings.Repeat(" ", max(0, width-longest))
	for i := range lines {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

// entryHead is the line above the text of a message, as the GUI heads it:
// who wrote it and when, in the muted color.
func (m *Model) entryHead(e entry) string {
	name := m.agentName()
	switch {
	case e.mine:
		name = cmp.Or(m.chat.peer, "You")
	case e.question:
		name = "Question"
	case e.failed:
		name = "Status"
	}
	return boldStyle.Foreground(muted).Render(name + " · " + clock(e.at, m.now()))
}

func (m *Model) View() tea.View {
	if m.profiles != nil {
		return m.profiles.current().View()
	}
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeAllMotion // a mouse that rests shows a picture, see hover
	v.WindowTitle = m.windowTitle()
	// The terminal takes the colors of the window of the GUI once it said
	// whether its background is light: asked before, it would answer with
	// those, and the UI could not follow the terminal again.
	if m.backgroundKnown {
		v.BackgroundColor, v.ForegroundColor = ui.background, ui.text
	}
	if m.width == 0 {
		return v
	}
	if m.onboard != nil {
		v.SetContent(m.onboardView())
		return v
	}
	if m.splash.show {
		v.SetContent(m.splash.view(m))
		return v
	}
	var body string
	switch m.screen {
	case screenSuggestions, screenHelp, screenKeys, screenModel, screenSettings, screenSessions, screenEffort, screenJobs:
		dialog, _ := m.dialogView()
		body = m.overlay(dialog)
	case screenPlugin, screenContext:
		// A screen that takes the whole window keeps a line above and below it.
		screen := m.context.view(m)
		if m.screen == screenPlugin {
			screen = m.plugins.view(m)
		}
		parts := []string{m.headerView(), screen, m.footerView()}
		if m.pendingRows() > 0 {
			parts = append(parts, " "+m.suggestionRequests.pendingView(m, m.width-1))
		}
		body = lipgloss.JoinVertical(lipgloss.Left, parts...)
	default:
		body = m.withPreview(m.chatScreen())
	}
	v.SetContent(body)
	return v
}

// windowTitle is what the terminal shows in the title of its window: the node
// and, when it has several profiles, which of them this UI shows.
func (m *Model) windowTitle() string {
	if m.profileOf < 2 {
		return "ouro"
	}
	if name := cmp.Or(m.status.Name, m.status.ID); name != "" {
		return fmt.Sprintf("ouro · %s (%d/%d)", name, m.profileAt, m.profileOf)
	}
	return fmt.Sprintf("ouro · %d/%d", m.profileAt, m.profileOf)
}

// view lists the changes of ouro the reviews of answers suggested.
func (r *requestsScreen) view(m *Model) (string, []hit) {
	w, h := m.dialogSize()
	title, bulk := "Suggestions", "a: accept all · suggestions go into one new chat"
	lines := []string{boldStyle.Render(title), ""}
	// A click on an entry selects it, and on Accept all selected accepts
	// them all; a click on accept or reject below the one selected decides.
	var hits []hit
	choose := func(i int) func() tea.Cmd {
		return choosing(i == r.sel, func() { r.selectAt(m, i) }, func() tea.Cmd { return r.decide(m, m.choice == accept) })
	}
	if r.trust.waiting() > 0 {
		hits = append(hits, onLine(len(lines), choose(r.trust.waiting())), onLine(len(lines)+1, choose(r.trust.waiting())))
		marker := "  "
		if r.sel == r.trust.waiting() {
			marker = lipgloss.NewStyle().Foreground(accent).Render("› ")
		}
		label := "Accept all"
		if r.acceptingAll {
			label = "Accepting all…"
		}
		lines = append(lines, marker+boldStyle.Render(label),
			mutedStyle.Render(bulk), "")
	}
	if r.trust.waiting() == 0 {
		lines = append(lines, mutedStyle.Render("No suggestions."))
	}
	selectedEnd := 2
	selected := func(i int) string {
		if i == r.sel {
			return lipgloss.NewStyle().Foreground(accent).Render("› ")
		}
		return "  "
	}
	// decide is what can be done with the entry i, if it is selected.
	decide := func(i int) []string {
		if i != r.sel {
			return nil
		}
		selectedEnd = len(lines) + 1
		hits = append(hits, choiceHits(len(lines), 4, []string{"accept", "reject"}, func(j int) tea.Cmd {
			m.choice = j
			return r.decide(m, j == accept)
		})...)
		return []string{"    " + choices([]string{"accept", "reject"}, m.choice)}
	}
	if len(r.trust.Suggestions) > 0 {
		lines = append(lines, "", boldStyle.Render("Changes of ouro the reviews of answers suggest"),
			mutedStyle.Render("accept: your agent makes it in a new conversation · reject: it is dropped"), "")
	}
	for i, s := range r.trust.Suggestions {
		at := i
		from := len(lines)
		// The text is what an LLM says; what was measured is what the node says.
		lines = append(lines, selected(at)+boldStyle.Render("suggestion")+"  "+mutedStyle.Render("· "+s.At.Format("15:04")))
		for _, l := range strings.Split(lipgloss.Wrap(s.Text, max(1, w-8), ""), "\n") {
			lines = append(lines, "    "+l)
		}
		if len(s.Signals) > 0 {
			lines = append(lines, "    "+mutedStyle.Render("measured: "+strings.Join(s.Signals, "; ")))
		}
		for l := from; l < len(lines); l++ {
			hits = append(hits, onLine(l, choose(at)))
		}
		lines = append(lines, decide(at)...)
	}
	// Keep the selected decision in view, also with many or long suggestions.
	rows := max(1, h-4)
	if selectedEnd > rows+2 {
		top := max(2, selectedEnd-rows)
		hits = shifted(hits, top, 2, len(lines))
		lines = append([]string{boldStyle.Render(title), ""}, lines[top:]...)
	}
	return m.dialog(lines), hits
}

// shortKey abbreviates a base64 public key for display.
func shortKey(publicKey string) string {
	if len(publicKey) <= 10 {
		return publicKey
	}
	return publicKey[:10] + "…"
}

// view shows the title of the splash in the middle of the screen.
func (s *splashScreen) view(m *Model) string {
	return drawSplash(m.width, m.height, s.elapsed, m.light)
}

// logoText joins the logo lines, each truncated to width.
func logoText(logo []string, width int) string {
	lines := make([]string, len(logo))
	for i := range lines {
		lines[i] = ansi.Truncate(logo[i], width, "…")
	}
	return strings.Join(lines, "\n")
}

// logoWidth is the width of the widest logo line.
func logoWidth() int {
	width := 0
	for _, l := range logoLines {
		width = max(width, ansi.StringWidth(l))
	}
	return width
}

// modelText is what is shown of the model of the node: the provider that
// answers with it before it, like "iGate / brain", or the model alone.
func (m *Model) modelText() string {
	text := m.status.Model
	if m.status.Provider != "" {
		text = m.status.Provider + " / " + text
	}
	if effort := m.chatEffort(); effort != "" {
		text += " · effort " + effort + m.tookText(effort)
	}
	return text
}

// tookText says what the API took of effort at the last call in the
// conversation shown, when it took less or something else: " → high" for
// "max", say. The value is the API's own.
func (m *Model) tookText(effort string) string {
	took := m.chat.cache.Metrics
	if !took.Fell || took.Effort != effort {
		return ""
	}
	return " → " + cmp.Or(took.Took, "not taken")
}

// chatEffort is how much the model thinks in the conversation shown: its
// own effort, or that of the node.
func (m *Model) chatEffort() string {
	if m.chat.efforted {
		return m.chat.effort
	}
	return m.status.Effort
}

func (m *Model) headerView() string {
	// The head of the chat on one line: what the GUI heads its window with,
	// the conversation shown, and where it works with which model.
	eyebrow := "OUROBOROS"
	if m.profileOf > 1 { // of how many profiles of the node this is the one shown
		eyebrow += fmt.Sprintf(" · %s %d/%d", strings.ToUpper(m.headerName()), m.profileAt, m.profileOf)
	}
	line := " " + boldStyle.Foreground(muted).Render(eyebrow) + mutedStyle.Render(" · ") +
		strings.ToUpper(m.chatName()) + mutedStyle.Render(" · ") + m.metaText()
	if n := m.suggestionRequests.unread; n > 0 {
		line += lipgloss.NewStyle().Foreground(warn).Render(fmt.Sprintf("  ✦ %d SUGGESTIONS", n))
	}
	if !m.status.Debug {
		return ansi.Truncate(line, m.width, "…")
	}
	// The debug mode is not to be missed: on the right, whatever the rest
	// leaves of the line.
	debug := boldStyle.Foreground(ui.background).Background(warn).Render(" DEBUG MODE ")
	if m.width <= lipgloss.Width(debug) {
		return ansi.Truncate(debug, m.width, "")
	}
	return spread(line, debug, m.width)
}

// chatTitle is what the line below the composer, or the footer of a screen
// that takes the window, says of the conversation on its left: whose it is,
// if a peer's, how fast and at what cost the last call was, and how much of
// the context it took. Who answers is in the head. A chat without a word yet
// has none, nor has a node that is not set up.
func (m *Model) chatTitle() string {
	c := m.chat
	if !m.status.Configured || len(c.entries) == 0 && c.peer == "" {
		return ""
	}
	var parts []string
	if c.peer != "" {
		parts = append(parts, boldStyle.Foreground(accent).Render(c.peer)+mutedStyle.Render(" · only to read"))
	}
	if n := len(c.tasks); n > 0 {
		running := "1 task runs"
		if n > 1 {
			running = fmt.Sprintf("%d tasks run", n)
		}
		parts = append(parts, boldStyle.Foreground(accent).Render(running)+mutedStyle.Render(" · esc stops"))
	}
	if metrics := callMetrics(c.cache); metrics != "" {
		parts = append(parts, mutedStyle.Render(metrics))
	}
	if c.context > 0 {
		parts = append(parts, mutedStyle.Render("context "+contextSize(c.context)))
	}
	if c.cache.Tokens > 0 {
		parts = append(parts, mutedStyle.Render("cached "+cacheRate(c.cache)))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, mutedStyle.Render("  · "))
}

// sgr is a sequence that sets how the text after it looks.
var sgr = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// fadeEdges fades what a box shows of the history, or of the context, out
// over two lines at the top when more of it is above, and at the bottom when
// more of it is below, so the text runs out at the edges of the box instead
// of being cut off there: the line at the edge is nearly the color of the
// background, the one next to it dim.
func (m *Model) fadeEdges(lines []string, above, below bool) []string {
	n := len(lines)
	if n < 4 { // too few lines to give up two at each edge
		return lines
	}
	edge := lipgloss.NewStyle().Foreground(fadeColor(m.light))
	if above {
		lines[0], lines[1] = edge.Render(ansi.Strip(lines[0])), faint(lines[1])
	}
	if below {
		lines[n-1], lines[n-2] = edge.Render(ansi.Strip(lines[n-1])), faint(lines[n-2])
	}
	return lines
}

// fadeColor is the color of the line at a faded edge, and of what lies
// behind a dialog: the muted color, nearly gone into the background.
func fadeColor(light bool) color.Color {
	p := guiDark
	if light {
		p = guiLight
	}
	return blend(p.muted, p.background, 0.35)
}

// faint dims a line and keeps its colors. Every sequence in it may turn the
// dimming off, so it is turned on again after each.
func faint(line string) string {
	const on, off = "\x1b[2m", "\x1b[22m"
	return on + sgr.ReplaceAllString(line, "${0}"+on) + off
}

// contextSize is how long a context of n bytes is, as the chat shows it:
// in KiB, and about how many tokens that is, at some 4 bytes a token.
func contextSize(n int) string {
	tokens := fmt.Sprintf("%d", n/4)
	if n >= 4000 {
		tokens = fmt.Sprintf("%.1fk", float64(n)/4000)
	}
	return fmt.Sprintf("%.1f KiB ≈ %s tokens", float64(n)/1024, tokens)
}

// cacheRate is how much of what the LLM was sent the provider had cached, as
// the chat shows it: of the last call, and of all calls in the session.
func cacheRate(c plugin.SessionCache) string {
	return fmt.Sprintf("%d%%, session %d%%", c.Cached*100/c.Tokens, c.AllCached*100/max(c.AllTokens, 1))
}

// footerHint is what the line below the composer, a dialog or a screen
// that takes the window says on its right: the keys that do something
// here, after what there is to notice.
func (m *Model) footerHint() string {
	help := ""
	if m.chat.pending {
		help = "esc stop"
	}
	if m.chat.asking != nil {
		help = "↑↓ choose · enter answer"
		if m.chat.pending {
			help += " · esc stop"
		}
	}
	if found, _ := m.suggestions(); len(found) > 0 {
		help = "↑↓ select · tab complete · enter run"
	}
	switch m.screen {
	case screenSuggestions:
		help = "↑↓ select · ←→ accept or reject · enter decide · esc back"
	case screenHelp, screenKeys:
		help = "esc back"
	case screenEffort:
		help = "←→ choose · enter set · esc back"
	case screenJobs:
		help = "↑↓ select · enter stop · esc back"
	case screenModel:
		help = "↑↓ choose · enter set · p for peers · f fallback · esc back"
	case screenSessions:
		help = "type to filter · ↑↓ select · enter show · ctrl+d delete · tab yours/peers · esc back"
	case screenContext:
		help = "↑↓ pgup/pgdn scroll · / search · esc back"
		if m.context.searching() {
			help = "↑↓ previous/next match · j/k pgup/pgdn scroll · / search anew · esc end search"
		}
		if m.context.typing {
			help = "↑↓ previous/next match · enter keep · esc end search"
		}
	case screenSettings:
		help = "↑↓ select · ←→ choose · enter open · esc back"
		if m.config.opened {
			help = "↑↓ select · ←→ choose · enter do · esc back"
		}
		if m.config.editing != nil {
			help = "enter save · esc keep what was"
		}
		if m.config.info != nil {
			help = "↑↓ scroll · enter / esc back"
		}
	case screenPlugin:
		help = "esc back"
		if len(m.plugins.screens[m.plugins.open].Actions) > 0 {
			help = "←→ select · enter run · " + help
		}
	}
	if m.screen == screenChat && m.jobsShown { // the key that hides the list again
		if help != "" {
			help += " · "
		}
		help += jobsKey + " hide"
	}
	right := mutedStyle.Render(help)
	if m.notice != "" {
		style := lipgloss.NewStyle().Foreground(accent)
		if m.noticeErr {
			style = errStyle
		}
		notice := style.Render(m.notice)
		if help != "" {
			notice += mutedStyle.Render("  │  ")
		}
		right = notice + right
	}
	return right
}

// footerView is the line below the body: the conversation on the left, where
// the chat is to be seen, and the hint on the right. The hint is shown whole
// as long as the line holds it, the conversation in what it leaves of the
// line.
func (m *Model) footerView() string {
	right := m.footerHint()
	if right != "" {
		right += " "
	}
	right = ansi.Truncate(right, m.width, "…")
	left := ""
	switch m.screen {
	case screenChat, screenHelp, screenKeys, screenModel, screenEffort, screenSettings, screenSessions: // the dialogs lie over the chat
		if room := m.width - lipgloss.Width(right) - 1; room > 0 && m.chatTitle() != "" {
			left = ansi.Truncate(" "+m.chatTitle(), room, "…")
		}
	}
	return left + strings.Repeat(" ", max(0, m.width-lipgloss.Width(left)-lipgloss.Width(right))) + right
}
