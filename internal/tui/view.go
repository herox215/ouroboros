package tui

import (
	"cmp"
	"fmt"
	"image/color"
	"strings"

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
	if m.trust.waiting() == 0 || m.screen == screenRequests {
		return 0
	}
	return 1
}

// pendingView is the line below the footer: how much waits for a decision,
// where to decide, and what it is. The command comes before what waits, so
// that a narrow window cuts off the latter.
func (m *Model) pendingView() string {
	var what []string
	for _, r := range m.trust.Incoming {
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
	for _, p := range m.trust.Proposals {
		what = append(what, "your agent proposes to change "+p.Field)
	}
	line := lipgloss.NewStyle().Foreground(warn).Bold(true).Render(fmt.Sprintf("● %d pending", m.trust.waiting())) +
		mutedStyle.Render(" · type ") + lipgloss.NewStyle().Foreground(accent).Render("/pending") +
		mutedStyle.Render(" to decide · "+strings.Join(what, ", "))
	return ansi.Truncate(" "+line, m.width, "…")
}

// chatHeight is the height of the conversation box above the input box.
func (m *Model) chatHeight() int { return max(4, m.bodyHeight()-inputHeight-2) }

// layout sizes the widgets after the window changed.
func (m *Model) layout() {
	atBottom := m.history.AtBottom()
	m.selected, m.pressed = nil, nil // its lines are wrapped anew
	inner := max(1, m.width-4)       // border and padding
	m.input.SetWidth(inner)
	m.history.SetWidth(inner)
	m.history.SetHeight(max(1, m.chatHeight()-4)) // border, title and typing line
	m.renderHistory(atBottom)
	m.sizeContents()
}

// renderHistory fills the history viewport with the conversation, the loop of
// every answer included.
func (m *Model) renderHistory(toBottom bool) {
	c := m.chat
	if len(c.entries) == 0 {
		m.history.SetContent(lipgloss.Wrap(mutedStyle.Render("No messages yet. Ask "+m.agentName()+" anything – it can ask the other nodes for you."), m.history.Width(), ""))
		return
	}
	body := lipgloss.NewStyle().Width(m.history.Width()).PaddingLeft(2)
	// What the agent says is markdown, and shown formatted.
	render := func(s string) string {
		return "  " + strings.ReplaceAll(renderMarkdown(s, m.history.Width()-2), "\n", "\n  ")
	}
	summaries := c.summaries()
	var blocks []string
	words := false // the last block is the words of a round of an answer that is in
	for i, e := range c.entries {
		// A round of the loop is its words; the calls of all the rounds of
		// an answer are one line below its last words, also while the
		// answer is on its way: what the agent waits for is below the
		// input meanwhile, and what every call was is on the context
		// screen (/context).
		if len(e.calls) > 0 {
			if e.text != "" {
				blocks, words = append(blocks, render(e.text)), true
			}
			if used := summaries[i]; used != "" {
				// The line goes right below the last words of the answer.
				line := mutedStyle.Render("  ↳ " + used)
				if words {
					blocks[len(blocks)-1] += "\n" + line
				} else {
					blocks = append(blocks, line)
				}
				words = false
			}
			continue
		}
		words = false
		head := m.entryHead(e)
		text := render(e.text)
		if e.mine {
			text = body.Render(e.text)
		}
		if e.failed {
			text = body.Foreground(bad).Render("✗ " + e.text)
		}
		blocks = append(blocks, head+"\n"+text)
	}
	// What waits for the answer on its way to take it comes last.
	for i, text := range c.queued {
		state := " · queued"
		if i == len(c.queued)-1 {
			state += ", ↑ takes it back"
		}
		head := boldStyle.Foreground(accent).Render("you") + mutedStyle.Render(state)
		blocks = append(blocks, head+"\n"+body.Faint(true).Render(text))
	}
	// The heads and the lines of the calls are wrapped here and not by the
	// viewport, which would measure all of the history on every step of
	// scrolling.
	m.history.SetContent(lipgloss.Wrap(strings.Join(blocks, "\n\n"), m.history.Width(), ""))
	if toBottom {
		m.stopGlide(&m.history) // the bottom is where it goes now
		m.history.GotoBottom()
	}
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
			summaries[last] = summary(calls)
		}
		calls, last = nil, -1
	}
	for i, e := range c.entries {
		if e.mine {
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
func summary(calls []call) string {
	failed := 0
	for _, c := range calls {
		if strings.Contains(c.text, " ✗ ") {
			failed++
		}
	}
	line := "Used 1 tool"
	if len(calls) != 1 {
		line = fmt.Sprintf("Used %d tools", len(calls))
	}
	if failed > 0 {
		line += fmt.Sprintf(", %d failed", failed)
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
	if m.showSplash {
		v.SetContent(m.splashView())
		return v
	}
	var body string
	switch m.screen {
	case screenRequests:
		body = m.requestsView()
	case screenHelp:
		body = m.helpView()
	case screenPlugin:
		body = m.screenView()
	case screenModel:
		body = m.modelView()
	case screenContext:
		body = m.contextView()
	case screenSettings:
		body = m.settingsView()
	case screenSessions:
		body = m.sessionsView()
	default:
		body = m.chatView()
	}
	parts := []string{m.headerView(), body, m.footerView()}
	if m.pendingRows() > 0 {
		parts = append(parts, m.pendingView())
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
func (m *Model) requestsView() string {
	w, h := m.width, m.bodyHeight()
	lines := []string{boldStyle.Render("Connection requests"), ""}
	if len(m.trust.Incoming) == 0 {
		lines = append(lines, mutedStyle.Render("No pending requests."))
	}
	selected := func(i int) string {
		if i == m.reqSel {
			return lipgloss.NewStyle().Foreground(accent).Render("› ")
		}
		return "  "
	}
	// decide is what can be done with the entry i, if it is selected.
	decide := func(i int) []string {
		if i != m.reqSel {
			return nil
		}
		return []string{"    " + choices([]string{"accept", "reject"}, m.choice)}
	}
	for i, r := range m.trust.Incoming {
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
	if len(m.trust.Outgoing) > 0 {
		lines = append(lines, "", mutedStyle.Render("Waiting for the other side:"))
		for _, r := range m.trust.Outgoing {
			lines = append(lines, "  "+mutedStyle.Render(r.Name+" ("+shortKey(r.PublicKey)+")"))
		}
	}
	if len(m.trust.Proposals) > 0 {
		lines = append(lines, "", boldStyle.Render("Changes of the configuration your agent proposes"), "")
	}
	for i, p := range m.trust.Proposals {
		// Now and then are what the node says. The reason is what the agent
		// says, and it is shown as that.
		lines = append(lines,
			selected(len(m.trust.Incoming)+i)+boldStyle.Render(p.Field)+"  "+mutedStyle.Render("· "+p.At.Format("15:04")),
			"    "+mutedStyle.Render("now:  ")+p.Now,
			"    "+mutedStyle.Render("then: ")+p.Then,
			"    "+mutedStyle.Render("your agent says: "+p.Reason))
		if p.Stale {
			lines = append(lines, "    "+errStyle.Render("the configuration was changed since: accepting drops this without applying it"))
		}
		lines = append(lines, decide(len(m.trust.Incoming)+i)...)
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

// splashView shows the ouroboros animation centered on the screen.
func (m *Model) splashView() string {
	return drawSplash(m.width, m.height, m.splashElapsed, m.splashElapsed+m.skyShift, m.light)
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
	if m.status.Provider == "" {
		return m.status.Model
	}
	return m.status.Provider + " / " + m.status.Model
}

func (m *Model) headerView() string {
	s := m.status
	title := boldStyle.Foreground(accent).Render(" ouroboros ")
	if m.profileOf > 1 { // of how many profiles of the node this is the one shown
		title += mutedStyle.Render(fmt.Sprintf("[%d/%d]", m.profileAt, m.profileOf))
	}
	var state string
	switch {
	case s.Running:
		state = lipgloss.NewStyle().Foreground(good).Render("● online") +
			mutedStyle.Render(fmt.Sprintf("  %s · %s · :%d · %s", s.ID, s.Name, s.Port, m.modelText()))
	case !s.Configured:
		state = lipgloss.NewStyle().Foreground(warn).Render("○ not configured") + mutedStyle.Render("  type /setup")
	default:
		state = lipgloss.NewStyle().Foreground(bad).Render("○ offline") +
			mutedStyle.Render(fmt.Sprintf("  %s · %s · type /start", s.ID, s.Name))
	}
	if m.unreadReq > 0 {
		state += lipgloss.NewStyle().Foreground(warn).Render(fmt.Sprintf("  ✉ %d", m.unreadReq))
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

func (m *Model) chatView() string {
	w, h := m.width, m.chatHeight()
	inner := max(1, w-4)
	c := m.chat
	// The commands that fit what is typed are listed between the
	// conversation and the input, in rows the conversation gives up.
	suggested := m.suggestionLines(h - 3)
	h -= len(suggested)

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
		title := boldStyle.Foreground(peer).Render(m.agentName())
		if c.peer != "" {
			title = boldStyle.Foreground(accent).Render(c.peer) + mutedStyle.Render(" with ") + title + mutedStyle.Render("  · only to read")
		}
		if metrics := callMetrics(c.cache); metrics != "" {
			title += mutedStyle.Render("  · " + metrics)
		}
		if c.context > 0 {
			title += mutedStyle.Render("  · context " + contextSize(c.context))
		}
		if c.cache.Tokens > 0 {
			title += mutedStyle.Render("  · cached " + cacheRate(c.cache))
		}
		title = ansi.Truncate(title, inner, "…")
		history := m.highlight(strings.Split(m.history.View(), "\n"))
		history = history[min(len(suggested), len(history)):] // the latest stays in view
		content = lipgloss.JoinVertical(lipgloss.Left, title, strings.Join(history, "\n"), m.waitView(inner))
	}
	chat := box(false, m.light).Width(w).Height(h).MaxHeight(h).Render(content)
	input := box(false, m.light).Width(w).Render(m.input.View())
	parts := append([]string{chat}, suggested...)
	return lipgloss.JoinVertical(lipgloss.Left, append(parts, input)...)
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

func (m *Model) footerView() string {
	help := "enter send · alt+enter new line · /help commands · ctrl+s sessions · pgup/pgdn scroll · ctrl+c quit"
	if found, _ := m.suggestions(); len(found) > 0 {
		help = "↑↓ select · tab complete · enter run · ctrl+c quit"
	}
	switch m.screen {
	case screenRequests:
		help = "↑↓ select · ←→ accept or reject · enter decide · esc back · ctrl+c quit"
	case screenHelp:
		help = "esc back · ctrl+c quit"
	case screenModel:
		help = "↑↓ choose · enter set · esc back · ctrl+c quit"
	case screenSessions:
		help = "↑↓ select · enter show · d delete · tab yours/peers · esc back · ctrl+c quit"
	case screenSettings:
		help = "↑↓ select · enter open · esc back · ctrl+c quit"
		if m.opened {
			help = "↑↓ select · ←→ choose · enter do · esc categories · ctrl+c quit"
		}
		if m.editing != nil {
			help = "enter save · esc keep what was · ctrl+c quit"
		}
	case screenPlugin:
		help = "esc back · ctrl+c quit"
		if len(m.screens[m.open].Actions) > 0 {
			help = "←→ select · enter run · " + help
		}
	}
	line := mutedStyle.Render(help)
	if m.notice != "" {
		style := lipgloss.NewStyle().Foreground(accent)
		if m.noticeErr {
			style = errStyle
		}
		line = style.Render(m.notice) + mutedStyle.Render("  │  ") + line
	}
	return ansi.Truncate(" "+line, m.width, "…")
}
