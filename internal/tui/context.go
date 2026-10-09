package tui

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The context screen is what the command /context opens: what the next call
// of the LLM in the conversation is sent, message by message – the system
// prompt, the tools and the conversation so far. The arrow keys and pgup and
// pgdn scroll, esc goes back to the chat.
//
// / searches the context: what is typed is looked for as it is typed, every
// match is marked, and the view goes to the first one from where the search
// began. The arrow keys go from match to match, also once enter has kept the
// search; esc ends it.

// Message is a message of the context of a conversation: who it is of, like
// "system", "user" or "tool run", and its text.
type Message struct {
	Role string
	Text string
}

// contentsMsg is the context of a conversation, on the way to the context
// screen.
type contentsMsg struct {
	contextID string
	msgs      []Message
	err       error
}

// contextScreen is the state of the context screen.
type contextScreen struct {
	port viewport.Model // where the context is shown
	got  *contentsMsg   // the context that came in, to wrap again when the window changes

	lines   []string        // the lines of the port without colors: what is searched
	search  textinput.Model // what is searched for; a search is on while it is typed or not empty
	typing  bool            // the search is being typed
	from    int             // the line at the top when the search began
	matches []match         // where what is searched for is, in order
	at      int             // the match the view went to last
}

// match is where what is searched for is found: a span on every line it
// covers, as the context is wrapped.
type match []span

// span is the part of a line of the context a match covers, in cells.
type span struct{ line, from, to int }

var (
	matchStyle   = lipgloss.NewStyle().Reverse(true)
	currentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(warn).Bold(true) // the match the view went to
)

// openContext opens the context screen and loads the context of the
// conversation.
func (m *Model) openContext() tea.Cmd {
	m.screen = screenContext
	m.context.port = viewport.New()
	m.context.port.KeyMap = viewport.KeyMap{} // the keys are handled explicitly
	m.context.got = nil
	m.context.endSearch()
	m.context.size(m)
	contextID := m.chat.contextID
	return func() tea.Msg {
		msgs, err := m.backend.Contents(m.ctx, contextID)
		return contentsMsg{contextID, msgs, err}
	}
}

// sizeContents fits the viewport of the context screen into its box: the
// border and padding, and the title with the line below it, which is blank
// or shows the search. The context is wrapped to the width here and not by
// the viewport, which would measure all of it again on every step of
// scrolling.
func (x *contextScreen) size(m *Model) {
	x.port.SetWidth(max(1, m.width-4))
	x.port.SetHeight(max(1, m.bodyHeight()-4))
	x.render(m)
}

// contentsLoaded shows the context that came in, unless the conversation
// changed meanwhile.
func (x *contextScreen) loaded(m *Model, msg contentsMsg) {
	if msg.contextID != m.chat.contextID {
		return
	}
	x.got = &msg
	x.render(m)
}

// renderContents fills the viewport of the context screen with the context
// that came in, wrapped to its width.
func (x *contextScreen) render(m *Model) {
	msg := x.got
	if msg == nil {
		x.show(mutedStyle.Render("…"))
		return
	}
	var blocks []string
	if msg.err != nil {
		blocks = append(blocks, lipgloss.Wrap(errStyle.Render("✗ "+msg.err.Error()), x.port.Width(), ""))
	}
	if len(msg.msgs) == 0 {
		blocks = append(blocks, mutedStyle.Render("Nothing yet."))
	}
	blocks = append(blocks, renderContext(msg.msgs, x.port.Width(), m.light)...)
	x.show(strings.Join(blocks, "\n\n"))
}

// show puts content into the viewport, and looks for what is searched for in
// it anew: wrapped again, it is on other lines. The view stays where it is.
func (x *contextScreen) show(content string) {
	x.port.SetContent(content)
	x.lines = strings.Split(content, "\n")
	for i, l := range x.lines {
		x.lines[i] = strings.Replace(ansi.Strip(l), bar, " ", 1) // a match goes on across the bar, as across white space
	}
	x.matches = find(x.lines, x.search.Value())
	x.at = min(x.at, max(len(x.matches)-1, 0))
}

// handleContextKey handles keys on the context screen.
func (x *contextScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	if x.typing {
		return x.handleSearchKey(m, msg)
	}
	if x.searching() {
		switch msg.String() {
		case "up":
			return x.step(m, -1)
		case "down":
			return x.step(m, 1)
		case "esc":
			x.endSearch()
			return nil
		}
	}
	switch msg.String() {
	case "/":
		return x.startSearch(m)
	case "esc":
		m.stopGlide(&x.port) // the screen it was for is left
		m.screen = screenChat
	case "up", "k":
		return m.scrollBy(&x.port, -1)
	case "down", "j":
		return m.scrollBy(&x.port, 1)
	case "pgup":
		return m.scrollBy(&x.port, -x.port.Height())
	case "pgdown":
		return m.scrollBy(&x.port, x.port.Height())
	case "home":
		return m.scrollBy(&x.port, -topLine(&x.port))
	case "end":
		return m.scrollBy(&x.port, topLine(&x.port))
	}
	return nil
}

// searching reports whether a search is on: it is typed, or was kept.
func (x *contextScreen) searching() bool { return x.typing || x.search.Value() != "" }

// startSearch lets the owner type what to search for, from the line at the
// top on.
func (x *contextScreen) startSearch(m *Model) tea.Cmd {
	x.search = textinput.New()
	x.search.Prompt = "/"
	x.search.SetStyles(textinput.DefaultStyles(!m.light))
	x.typing, x.from, x.matches, x.at = true, x.port.YOffset(), nil, 0
	return x.search.Focus()
}

// endSearch ends the search: nothing is marked any more.
func (x *contextScreen) endSearch() {
	x.search = textinput.Model{}
	x.typing, x.matches, x.at = false, nil, 0
}

// handleSearchKey handles keys while the search is typed: enter keeps it,
// and one of nothing but spaces ends it, as esc does.
func (x *contextScreen) handleSearchKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		x.endSearch()
		return nil
	case "enter":
		x.typing = false
		x.search.Blur()
		if strings.TrimSpace(x.search.Value()) == "" {
			x.endSearch()
		}
		return nil
	case "up":
		return x.step(m, -1)
	case "down":
		return x.step(m, 1)
	}
	return x.typed(m, msg)
}

// typed hands msg to the search while it is typed, a key or what is pasted,
// and looks anew when that changed it: the view goes to the first match from
// the line the search began at, or to the first of all when there is none
// below.
func (x *contextScreen) typed(m *Model, msg tea.Msg) tea.Cmd {
	was := x.search.Value()
	var cmd tea.Cmd
	x.search, cmd = x.search.Update(msg)
	if x.search.Value() == was {
		return cmd
	}
	x.matches, x.at = find(x.lines, x.search.Value()), 0
	for i, mt := range x.matches {
		if mt[0].line >= x.from {
			x.at = i
			break
		}
	}
	return tea.Batch(cmd, x.goTo(m))
}

// step goes by one match, down or up, around the end; nothing when there is
// none.
func (x *contextScreen) step(m *Model, by int) tea.Cmd {
	if len(x.matches) == 0 {
		return nil
	}
	x.at = (x.at + by + len(x.matches)) % len(x.matches)
	return x.goTo(m)
}

// goTo brings the match the search is at into sight, a third of the view
// below its top, so that what comes before it can be read too. A match in
// sight already, or on the way there, is left where it is.
func (x *contextScreen) goTo(m *Model) tea.Cmd {
	if len(x.matches) == 0 {
		return nil
	}
	line, top := x.matches[x.at][0].line, x.port.YOffset()
	if g := m.glide; g != nil && g.view == &x.port {
		top = g.target
	}
	if line >= top && line < top+x.port.Height() {
		return nil
	}
	return m.glideTo(&x.port, min(max(line-x.port.Height()/3, 0), topLine(&x.port)))
}

// find looks for what is searched for in the lines, without regard to case.
// Any white space between its words matches any white space, so that words
// are found across the end of a line the context is wrapped at too, and
// white space around it is not searched for at all. A span of a match leaves
// out the white space at the ends of its line.
func find(lines []string, search string) []match {
	words := strings.Fields(search)
	if len(words) == 0 {
		return nil
	}
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	text := strings.Join(lines, "\n")
	re := regexp.MustCompile(`(?i)` + strings.Join(words, `[\s\p{Z}]+`))
	var found []match
	line, start := 0, 0 // the line a match begins on, and where it begins in text
	for _, loc := range re.FindAllStringIndex(text, -1) {
		for start+len(lines[line])+1 <= loc[0] { // the next line begins at the match or before it
			start += len(lines[line]) + 1
			line++
		}
		var mt match
		for i, at := line, start; ; i, at = i+1, at+len(lines[i])+1 {
			l := lines[i]
			from, to := max(loc[0]-at, 0), min(loc[1]-at, len(l))
			from = to - len(strings.TrimLeftFunc(l[from:to], unicode.IsSpace))
			to = from + len(strings.TrimRightFunc(l[from:to], unicode.IsSpace))
			if from < to {
				mt = append(mt, span{i, ansi.StringWidth(l[:from]), ansi.StringWidth(l[:to])})
			}
			if loc[1] <= at+len(l) { // the match ends on this line
				break
			}
		}
		found = append(found, mt)
	}
	return found
}

// marked marks the matches on lines, the lines in sight from the top of the
// viewport on: the one the search is at differently from the others.
func (x *contextScreen) marked(lines []string) []string {
	type mark struct {
		from, to int
		style    lipgloss.Style
	}
	top := x.port.YOffset()
	marks := map[int][]mark{}
	for i, mt := range x.matches {
		style := matchStyle
		if i == x.at {
			style = currentStyle
		}
		for _, s := range mt {
			if j := s.line - top; j >= 0 && j < len(lines) {
				marks[j] = append(marks[j], mark{s.from, s.to, style})
			}
		}
	}
	for i, ms := range marks {
		l, done := lines[i], 0
		var b strings.Builder
		for _, mk := range ms {
			b.WriteString(ansi.Cut(l, done, mk.from) + mk.style.Render(ansi.Strip(ansi.Cut(l, mk.from, mk.to))))
			done = mk.to
		}
		lines[i] = b.String() + ansi.Cut(l, done, ansi.StringWidth(l))
	}
	return lines
}

// searchView is the line below the title while a search is on: what is
// searched for, and how many matches there are.
func (x *contextScreen) searchView() string {
	if !x.searching() {
		return ""
	}
	found := "no match"
	if len(x.matches) > 0 {
		found = fmt.Sprintf("%d/%d", x.at+1, len(x.matches))
	}
	return x.search.View() + mutedStyle.Render("  "+found)
}

// contextView shows the context of the conversation, with how long it was
// when the LLM was last called.
func (x *contextScreen) view(m *Model) string {
	w, h := m.width, m.bodyHeight()
	title := boldStyle.Render("Context")
	if m.chat.context > 0 {
		title += mutedStyle.Render("  " + contextSize(m.chat.context) + " at the last call")
	}
	if m.chat.cache.Tokens > 0 {
		title += mutedStyle.Render(", cached " + cacheRate(m.chat.cache))
	}
	if metrics := callMetrics(m.chat.cache); metrics != "" {
		title += mutedStyle.Render("  · " + metrics)
	}
	title += mutedStyle.Render(fmt.Sprintf("  · %.0f%%", x.port.ScrollPercent()*100))
	search := ansi.Truncate(x.searchView(), x.port.Width(), "…")
	// The context fades out at the edges of the box, as the history of the
	// chat does. The matches are marked on the faded lines, so that one at an
	// edge stands out still.
	lines := m.fadeEdges(strings.Split(x.port.View(), "\n"), x.port.YOffset() > 0, !x.port.AtBottom())
	body := strings.Join(x.marked(lines), "\n")
	return box(true, m.light).Width(w).Height(h).MaxHeight(h).Render(title + "\n" + search + "\n" + body)
}
