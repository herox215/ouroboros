package tui

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"image/color"
	"math"
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

const inputHeight = 3 // text rows of the message input

// The 16 ANSI colors follow the terminal theme, so they work on light and dark backgrounds.
var (
	accent = lipgloss.Color("6")
	peer   = lipgloss.Color("5")
	muted  = lipgloss.Color("8")
	good   = lipgloss.Color("2")
	bad    = lipgloss.Color("1")
	warn   = lipgloss.Color("3")

	mutedStyle = lipgloss.NewStyle().Foreground(muted)
	imageStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("4")) // the label of a picture, like [Image_1]
	errStyle   = lipgloss.NewStyle().Foreground(bad)
	boldStyle  = lipgloss.NewStyle().Bold(true)
)

// borderMuted is the color of a box that does not have focus. It is a subtle
// gray that follows the terminal background, so the frame recedes behind the
// content instead of standing out.
func borderMuted(light bool) color.Color {
	if light {
		return lipgloss.Color("252")
	}
	return lipgloss.Color("237")
}

func box(focused, light bool) lipgloss.Style {
	color := borderMuted(light)
	if focused {
		color = accent
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(color).Padding(0, 1)
}

// inputStyles is the style of the message input. The cursor line has no
// background, so the line the cursor is on does not look like a black bar.
func inputStyles(light bool) textarea.Styles {
	s := textarea.DefaultStyles(!light)
	s.Cursor.Color = lipgloss.Color("250")
	if light {
		s.Cursor.Color = lipgloss.Color("242")
	}
	s.Focused.CursorLine = lipgloss.NewStyle()
	s.Blurred.CursorLine = lipgloss.NewStyle()
	return s
}

// bodyHeight is the height between the header and the footer, and the line
// below the footer if something is pending.
func (m *Model) bodyHeight() int { return max(0, m.height-2-m.pendingRows()) }

// pendingRows is how many rows the line below the footer takes: one while
// something waits for a decision, except on the screen that lists it.
func (m *Model) pendingRows() int {
	if m.requests.trust.waiting() == 0 || m.screen == screenRequests {
		return 0
	}
	return 1
}

// pendingView is the line below the footer: how much waits for a decision,
// where to decide, and what it is. The command comes before what waits, so
// that a narrow window cuts off the latter.
func (r *requestsScreen) pendingView(m *Model) string {
	var what []string
	for _, r := range r.trust.Incoming {
		name := r.Name
		if name == "" {
			name = shortKey(r.PublicKey)
		}
		if r.Proposed {
			what = append(what, "your agent wants to connect to "+name)
		} else {
			what = append(what, name+" wants to chat")
		}
	}
	for _, p := range r.trust.Proposals {
		what = append(what, "your agent proposes to change "+p.Field)
	}
	line := lipgloss.NewStyle().Foreground(warn).Bold(true).Render(fmt.Sprintf("● %d pending", r.trust.waiting())) +
		mutedStyle.Render(" · type ") + lipgloss.NewStyle().Foreground(accent).Render("/pending") +
		mutedStyle.Render(" to decide · "+strings.Join(what, ", "))
	return ansi.Truncate(" "+line, m.width, "…")
}

// chatHeight is the height of the conversation box above the input box;
// what the box of the input does not take of its room, the chat does.
func (m *Model) chatHeight() int { return max(4, m.bodyHeight()-m.inputRows()) }

// inputBoxRows is how high the box of the input is: its rows and border.
const inputBoxRows = inputHeight + 2

// inputRows is how many rows of the body the box of the input takes: all of
// them once it is in, none while it is out, and on its way in or out the
// ones it has come up so far, slowing down as it arrives.
func (m *Model) inputRows() int {
	t := float64(m.inputLevel) / inputFrames
	return int(math.Round((1 - math.Pow(1-t, 3)) * inputBoxRows))
}

// inputVisible reports whether the box of the input is drawn under the
// chat: from a key on the chat until it is empty and idle, and whenever it
// holds text or a question of the agent waits in it.
func (m *Model) inputVisible() bool {
	if a := m.chat.asking; a != nil && !a.sent {
		return true
	}
	return m.inputShown || m.input.Value() != ""
}

// layout sizes the widgets after the window changed. The box of the input,
// unless it is on its way, is where inputVisible says at once.
func (m *Model) layout() {
	if !m.inputFading {
		m.inputLevel = 0
		if m.inputVisible() {
			m.inputLevel = inputFrames
		}
	}
	atBottom := m.history.AtBottom()
	m.selected, m.pressed = nil, nil // its lines are wrapped anew
	inner := max(1, m.chatWidth()-4) // border and padding
	m.input.SetWidth(inner)
	m.history.SetWidth(inner)
	m.history.SetHeight(max(1, m.chatHeight()-3)) // border and typing line
	m.renderHistory(atBottom)
	m.context.size(m)
}

// renderHistory fills the history viewport with the conversation, the loop of
// every answer included.
func (m *Model) renderHistory(toBottom bool) {
	c := m.chat
	if len(c.entries) == 0 {
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
	body := lipgloss.NewStyle().Width(width).PaddingLeft(2)
	// What the agent says is markdown, and shown formatted. Every block is
	// wrapped here and not by the viewport, which would measure all of the
	// history on every step of scrolling.
	render := func(s string) string {
		return "  " + strings.ReplaceAll(renderMarkdown(s, width-2), "\n", "\n  ")
	}
	wrap := func(s string) string { return lipgloss.Wrap(s, width, "") }
	summaries := c.summaries()
	// one adds what the chat shows of the entry i to blocks and says whether
	// the last block there is the words of a round of an answer that is in,
	// below which the line of its calls goes.
	one := func(i int, blocks []string, words bool) ([]string, bool) {
		e := c.entries[i]
		// The report of a task is a line of its own: what the agent makes
		// of it is its answer.
		if e.task {
			line := mutedStyle.Render("◆ " + e.text)
			if e.failed {
				line = lipgloss.NewStyle().Foreground(bad).Render("◆ " + e.text)
			}
			return append(blocks, wrap("  "+line)), false
		}
		if !m.thoughtsHidden && e.thought != "" {
			blocks, words = append(blocks, wrap(thoughtBlock(e.thought, width))), false
		}
		// A round of the loop is its words; the calls of all the rounds of
		// an answer are one line below its last words, also while the
		// answer is on its way: what the agent waits for is below the
		// input meanwhile, and what every call was is on the context
		// screen (/context).
		if len(e.calls) > 0 {
			if e.text != "" {
				blocks, words = append(blocks, wrap(render(e.text))), true
			}
			if used := summaries[i]; used != "" {
				// The line goes right below the last words of the answer.
				line := wrap(c.muted("  ↳ " + used))
				if words {
					blocks[len(blocks)-1] += "\n" + line
				} else {
					blocks = append(blocks, line)
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
		head := m.entryHead(e)
		text := render(e.text)
		if e.mine {
			text = body.Render(c.showLabels(c.labelled(e.text)))
		}
		if e.failed {
			text = body.Foreground(bad).Render("✗ " + e.text)
		}
		return append(blocks, wrap(head+"\n"+text)), false
	}
	// What an answer has written stays as it is: the entries that cannot
	// change anymore are drawn once and kept, so that an answer on its way
	// costs only its own words, however long the conversation is.
	settled, kept := c.settled(), c.shown.n
	blocks := c.shown.blocks
	if c.shown.width != width || c.shown.thoughts == m.thoughtsHidden || kept > settled {
		kept, blocks = 0, nil
	}
	words := false // the last block is the words of a round of an answer that is in
	for i := kept; i < settled; i++ {
		blocks, words = one(i, blocks, words)
	}
	c.shown = rendered{n: settled, width: width, thoughts: !m.thoughtsHidden, blocks: blocks}
	for i := settled; i < len(c.entries); i++ {
		blocks, words = one(i, blocks, words)
	}
	// The answer the model is still writing, as far as the stream has brought
	// it; the Answer of the end replaces it with the whole of it.
	if c.pending && c.partial != "" {
		blocks = append(blocks, wrap(render(threadIn(c.partial, c.advance())))+mutedStyle.Render(" ▌"))
	}
	// What waits for the answer on its way to take it comes last.
	for i, text := range c.queued {
		state := " · queued"
		if i == len(c.queued)-1 {
			state += ", ↑ takes it back"
		}
		head := boldStyle.Foreground(accent).Render("you") + mutedStyle.Render(state)
		blocks = append(blocks, wrap(head+"\n"+body.Faint(true).Render(c.showLabels(c.labelled(text)))))
	}
	m.history.SetContent(m.behind(strings.Join(blocks, "\n\n")))
	if toBottom {
		m.stopGlide(&m.history) // the bottom is where it goes now
		m.history.GotoBottom()
	}
}

// rendered are the entries of a conversation as the chat shows them, the
// first n of them, wrapped for the width they were drawn at.
type rendered struct {
	n        int      // of the entries, how many are in blocks
	width    int      // the width of the chat they are wrapped for
	thoughts bool     // whether what the model thought was shown then
	blocks   []string // what the chat shows of them, a block for every entry
}

// settled is how many of the entries of c are shown as they will stay: all of
// them but those of the answer that is on its way, whose words, calls and line
// of calls still change.
func (c *conversation) settled() int {
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
const thoughtKey = "ctrl+t"

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

// entryHead is the line above the text of a message: who wrote it and when.
func (m *Model) entryHead(e entry) string {
	name, color := m.agentName(), peer
	if e.mine {
		name, color = cmp.Or(m.chat.peer, "you"), accent
	}
	return boldStyle.Foreground(color).Render(name) + mutedStyle.Render(" · "+e.at.Format("15:04"))
}

func (m *Model) View() tea.View {
	if m.profiles != nil {
		return m.profiles.current().View()
	}
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = m.windowTitle()
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
	case screenRequests:
		body = m.requests.view(m)
	case screenHelp:
		body = m.overlay(m.helpView())
	case screenKeys:
		body = m.overlay(m.keysView())
	case screenPlugin:
		body = m.plugins.view(m)
	case screenModel:
		body = m.overlay(m.picker.view(m))
	case screenContext:
		body = m.context.view(m)
	case screenSettings:
		body = m.overlay(m.config.view(m))
	case screenSessions:
		body = m.overlay(m.sessions.view(m))
	case screenEffort:
		body = m.overlay(m.effort.view(m))
	case screenJobs:
		body = m.overlay(m.jobs.view(m))
	default:
		body = m.chatView()
	}
	parts := []string{m.headerView(), body, m.footerView()}
	if m.pendingRows() > 0 {
		parts = append(parts, m.requests.pendingView(m))
	}
	v.SetContent(lipgloss.JoinVertical(lipgloss.Left, parts...))
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

// requestsView lists what is waiting for a decision: the connection requests,
// and the changes of the configuration the agent proposed.
func (r *requestsScreen) view(m *Model) string {
	w, h := m.width, m.bodyHeight()
	lines := []string{boldStyle.Render("Connection requests"), ""}
	if len(r.trust.Incoming) == 0 {
		lines = append(lines, mutedStyle.Render("No pending requests."))
	}
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
		return []string{"    " + choices([]string{"accept", "reject"}, m.choice)}
	}
	for i, r := range r.trust.Incoming {
		name := r.Name
		if name == "" {
			name = shortKey(r.PublicKey)
		}
		marker := selected(i)
		what := "wants to chat"
		if r.Proposed {
			// Nobody asked yet: accepting sends the request, with this key.
			what = "· your agent wants to connect (" + shortKey(r.PublicKey) + ")"
		}
		lines = append(lines,
			marker+boldStyle.Render(name)+"  "+mutedStyle.Render(what),
			"    "+mutedStyle.Render(r.Addr+" · "+r.At.Format("15:04")))
		lines = append(lines, decide(i)...)
	}
	if len(r.trust.Outgoing) > 0 {
		lines = append(lines, "", mutedStyle.Render("Waiting for the other side:"))
		for _, r := range r.trust.Outgoing {
			lines = append(lines, "  "+mutedStyle.Render(r.Name+" ("+shortKey(r.PublicKey)+")"))
		}
	}
	if len(r.trust.Proposals) > 0 {
		lines = append(lines, "", boldStyle.Render("Changes of the configuration your agent proposes"), "")
	}
	for i, p := range r.trust.Proposals {
		// Now and then are what the node says. The reason is what the agent
		// says, and it is shown as that.
		lines = append(lines,
			selected(len(r.trust.Incoming)+i)+boldStyle.Render(p.Field)+"  "+mutedStyle.Render("· "+p.At.Format("15:04")),
			"    "+mutedStyle.Render("now:  ")+p.Now,
			"    "+mutedStyle.Render("then: ")+p.Then,
			"    "+mutedStyle.Render("your agent says: "+p.Reason))
		if p.Stale {
			lines = append(lines, "    "+errStyle.Render("the configuration was changed since: accepting drops this without applying it"))
		}
		lines = append(lines, decide(len(r.trust.Incoming)+i)...)
	}
	return box(true, m.light).Width(w).Height(h).MaxHeight(h).Render(strings.Join(lines, "\n"))
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
	s := m.status
	// The profile is known by its name; one that has none yet by the program.
	title := boldStyle.Foreground(accent).Render(" " + m.headerName() + " ")
	if m.profileOf > 1 { // of how many profiles of the node this is the one shown
		title += mutedStyle.Render(fmt.Sprintf("[%d/%d]", m.profileAt, m.profileOf))
	}
	// Which profile it is, and where it listens, is said only for debugging.
	var id, where string
	if s.Debug {
		id = s.ID + " · "
		where = fmt.Sprintf("%s:%d · ", id, s.Port)
	}
	// Whether the node is online is not said: while the UI is open it is.
	var state string
	switch {
	case s.Running:
		state = mutedStyle.Render(where + m.modelText())
	case !s.Configured:
		state = lipgloss.NewStyle().Foreground(warn).Render("○ not configured") + mutedStyle.Render("  type /setup")
	default:
		state = mutedStyle.Render(id + "type /start")
	}
	if m.requests.unread > 0 {
		state += lipgloss.NewStyle().Foreground(warn).Render(fmt.Sprintf("  ✉ %d", m.requests.unread))
	}
	if !s.Debug {
		return ansi.Truncate(title+" "+state, m.width, "…")
	}
	// The debug mode is not to be missed: on the right, whatever the rest
	// leaves of the line.
	debug := boldStyle.Foreground(lipgloss.Color("0")).Background(warn).Render(" DEBUG MODE ")
	if m.width <= lipgloss.Width(debug) {
		return ansi.Truncate(debug, m.width, "")
	}
	left := ansi.Truncate(title+" "+state, m.width-lipgloss.Width(debug)-1, "…")
	return left + strings.Repeat(" ", m.width-lipgloss.Width(left)-lipgloss.Width(debug)) + debug
}

// chatTitle is what the footer says of the conversation on its left: whose
// it is, if a peer's, how fast and at what cost the last call was, and how
// much of the context it took. Who answers is in the header. A chat without
// a word yet has none, nor has a node that is not set up.
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
	return " " + strings.Join(parts, mutedStyle.Render("  · "))
}

// chatWidth is how wide the chat and the input are: what the list of the
// jobs leaves of the width, if it is shown.
func (m *Model) chatWidth() int { return max(1, m.width-m.jobsWidth()) }

// chatView is the chat with the input below it, and the list of the jobs on
// the right of both if it is shown.
func (m *Model) chatView() string {
	chat := m.chatColumn()
	if m.jobsWidth() == 0 {
		return chat
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, chat, m.jobsList())
}

// chatColumn is the chat with the input below it.
func (m *Model) chatColumn() string {
	w, h := m.chatWidth(), m.chatHeight()
	inner := max(1, w-4)
	c := m.chat
	var content string
	if !m.status.Configured {
		hint := mutedStyle.Render("Type /setup to set up your node and chat with its agent.")
		if inner >= logoWidth() && h-2 >= len(logoLines)+2 { // border, blank line and hint
			content = lipgloss.JoinVertical(lipgloss.Left,
				lipgloss.NewStyle().Foreground(accent).Render(logoText(logoLines, inner)), "", hint)
		} else {
			content = hint
		}
	} else {
		history := m.highlight(strings.Split(m.history.View(), "\n"))
		history = m.fadeEdges(history, m.history.YOffset() > 0, !m.history.AtBottom())
		content = lipgloss.JoinVertical(lipgloss.Left, strings.Join(history, "\n"), m.waitView(inner))
	}
	chat := box(false, m.light).Width(w).Height(h).MaxHeight(h).Render(content)
	// Suggestions and answers lie over the bottom of the chat, in a panel.
	// Its size and the positions of its history stay unchanged behind them.
	panel := m.panel()
	rows := m.inputRows()
	if rows == 0 {
		return chat
	}
	input := box(false, m.light).Width(w).Render(c.showLabels(m.input.View()))
	if m.inputLevel < inputFrames {
		input = m.arriving(input, rows)
	}
	if panel == "" {
		return lipgloss.JoinVertical(lipgloss.Left, chat, input)
	}
	back := strings.Split(lipgloss.JoinVertical(lipgloss.Left, chat, input), "\n")
	style := lipgloss.NewStyle().Foreground(fadeColor(m.light))
	for i, line := range back {
		back[i] = style.Render(ansi.Strip(line))
	}
	height := m.bodyHeight()
	canvas := lipgloss.NewCanvas(w, height)
	canvas.Compose(lipgloss.NewCompositor(
		lipgloss.NewLayer(strings.Join(back, "\n")),
		lipgloss.NewLayer(panel).X(2).Y(m.panelTop(panel)).Z(1), // in the middle: it is four cells narrower
	))
	return lipgloss.NewStyle().Width(w).Height(height).MaxHeight(height).Render(canvas.Render())
}

// arriving is the box of the input on its way: the rows of it that have come
// up from below, the gray of a faded edge first, then dim, until it is in.
func (m *Model) arriving(input string, rows int) string {
	lines := strings.Split(input, "\n")[:rows]
	t := float64(m.inputLevel) / inputFrames
	for i, line := range lines {
		if t < 0.5 {
			lines[i] = lipgloss.NewStyle().Foreground(fadeColor(m.light)).Render(ansi.Strip(line))
		} else {
			lines[i] = faint(line)
		}
	}
	return strings.Join(lines, "\n")
}

// panel is the box that lies over the bottom of the chat: the commands that
// fit what is typed, or the answers to a question of the agent, above the
// input. Like the centered dialogs it has a border of its own and leaves the
// faded frame of the chat to be seen around it. It is "" without either.
func (m *Model) panel() string {
	w, h := m.chatWidth(), m.chatHeight()
	suggested := m.suggestionLines(h - 6)
	if a := m.chat.asking; len(suggested) == 0 && (a == nil || a.sent) {
		return ""
	}
	panelWidth := max(1, w-4)
	panelInner := max(1, panelWidth-4)
	typing := m.inputView(panelInner, h-6-len(suggested))
	lines := make([]string, len(suggested))
	for i, line := range suggested {
		lines[i] = ansi.Truncate(strings.TrimPrefix(line, "  "), panelInner, "…")
	}
	lines = append(lines, m.chat.showLabels(typing))
	return box(true, m.light).Width(panelWidth).Render(strings.Join(lines, "\n"))
}

// panelTop is the row of the body panel begins at: a row of the box of the
// input stays below it.
func (m *Model) panelTop(panel string) int {
	return max(0, m.bodyHeight()-lipgloss.Height(panel)-1)
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

// fadeColor is the color of the line at a faded edge: a gray a few steps
// from the background of the terminal.
func fadeColor(light bool) color.Color {
	if light {
		return lipgloss.Color("250")
	}
	return lipgloss.Color("239")
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

// footerHint is what the footer says on its right: the keys that do
// something here, after what there is to notice.
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
	// Without the box of the input it says, softly, that a key brings it.
	if help == "" && m.screen == screenChat && !m.inputVisible() && m.chat.peer == "" {
		help = "type to write"
	}
	switch m.screen {
	case screenRequests:
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
		help = "↑↓ select · enter open · esc back"
		if m.config.opened {
			help = "↑↓ select · ←→ choose · enter do · esc back"
		}
		if m.config.editing != nil {
			help = "enter save · esc keep what was"
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
		if room := m.width - lipgloss.Width(right) - 1; room > 0 {
			left = ansi.Truncate(m.chatTitle(), room, "…")
		}
	}
	return left + strings.Repeat(" ", max(0, m.width-lipgloss.Width(left)-lipgloss.Width(right))) + right
}
