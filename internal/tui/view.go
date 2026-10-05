package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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

func box(focused bool) lipgloss.Style {
	color := muted
	if focused {
		color = accent
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(color).Padding(0, 1)
}

func (m *Model) bodyHeight() int { return max(0, m.height-2) } // minus header and footer

// chatHeight is the height of the conversation box above the input box.
func (m *Model) chatHeight() int { return max(4, m.bodyHeight()-inputHeight-2) }

// layout sizes the widgets after the window changed.
func (m *Model) layout() {
	atBottom := m.history.AtBottom()
	inner := max(1, m.width-4) // border and padding
	m.input.SetWidth(inner)
	m.history.SetWidth(inner)
	m.history.SetHeight(max(1, m.chatHeight()-4)) // border, title and typing line
	m.renderHistory(atBottom)
}

// renderHistory fills the history viewport with the conversation.
func (m *Model) renderHistory(toBottom bool) {
	c := &m.chat
	if len(c.entries) == 0 {
		m.history.SetContent(mutedStyle.Render("No messages yet. Ask " + m.agentName() + " anything – it can ask the other nodes for you."))
		return
	}
	body := lipgloss.NewStyle().Width(m.history.Width()).PaddingLeft(2)
	blocks := make([]string, len(c.entries))
	for i, e := range c.entries {
		name, color := m.agentName(), peer
		if e.mine {
			name, color = "you", accent
		}
		head := boldStyle.Foreground(color).Render(name) + mutedStyle.Render(" · "+e.at.Format("15:04"))
		text := body.Render(e.text)
		if e.failed {
			text = body.Foreground(bad).Render("✗ " + e.text)
		}
		blocks[i] = head + "\n" + text
	}
	m.history.SetContent(strings.Join(blocks, "\n\n"))
	if toBottom {
		m.history.GotoBottom()
	}
}

func (m *Model) View() tea.View {
	v := tea.NewView("")
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "ouro"
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
	body := m.chatView()
	switch m.screen {
	case screenRequests:
		body = m.requestsView()
	case screenHelp:
		body = m.helpView()
	case screenPlugin:
		body = m.screenView()
	}
	v.SetContent(lipgloss.JoinVertical(lipgloss.Left, m.headerView(), body, m.footerView()))
	return v
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
	return box(true).Width(w).Height(h).MaxHeight(h).Render(strings.Join(lines, "\n"))
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

func (m *Model) headerView() string {
	s := m.status
	title := boldStyle.Foreground(accent).Render(" ouroboros ")
	var state string
	switch {
	case s.Running:
		state = lipgloss.NewStyle().Foreground(good).Render("● online") +
			mutedStyle.Render(fmt.Sprintf("  %s · %s · :%d · %s", s.ID, s.Name, s.Port, s.Model))
	case !s.Configured:
		state = lipgloss.NewStyle().Foreground(warn).Render("○ not configured") + mutedStyle.Render("  type /setup")
	default:
		state = lipgloss.NewStyle().Foreground(bad).Render("○ offline") +
			mutedStyle.Render(fmt.Sprintf("  %s · %s · type /start", s.ID, s.Name))
	}
	if m.unreadReq > 0 {
		state += lipgloss.NewStyle().Foreground(warn).Render(fmt.Sprintf("  ✉ %d", m.unreadReq))
	}
	return ansi.Truncate(title+" "+state, m.width, "…")
}

func (m *Model) chatView() string {
	w, h := m.width, m.chatHeight()
	inner := max(1, w-4)
	c := &m.chat

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
		title = ansi.Truncate(title+mutedStyle.Render(" · "+m.status.Model+" · your agent"), inner, "…")
		var typing string
		switch {
		case c.doing != "":
			typing = m.spinner.View() + mutedStyle.Render(" "+c.doing+"…")
		case c.pending:
			typing = m.spinner.View() + mutedStyle.Render(" "+m.agentName()+" is thinking…")
		}
		content = lipgloss.JoinVertical(lipgloss.Left, title, m.history.View(), typing)
	}
	chat := box(false).Width(w).Height(h).MaxHeight(h).Render(content)
	input := box(true).Width(w).Render(m.input.View())
	return lipgloss.JoinVertical(lipgloss.Left, chat, input)
}

func (m *Model) footerView() string {
	help := "enter send · alt+enter new line · /help commands · pgup/pgdn scroll · ctrl+c quit"
	if found := m.matching(m.input.Value()); len(found) > 0 {
		parts := make([]string, len(found))
		for i, c := range found {
			parts[i] = "/" + c.name + " " + c.help
		}
		help = strings.Join(parts, " · ")
	}
	switch m.screen {
	case screenRequests:
		help = "↑↓ select · ←→ accept or reject · enter decide · esc back · ctrl+c quit"
	case screenHelp:
		help = "esc back · ctrl+c quit"
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
