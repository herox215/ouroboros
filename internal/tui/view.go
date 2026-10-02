package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	inputHeight  = 3  // text rows of the message input
	sidebarWidth = 32 // maximum width of the node list
)

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

func (m *Model) sidebarWidth() int { return min(sidebarWidth, m.width/3) }
func (m *Model) chatWidth() int    { return m.width - m.sidebarWidth() }
func (m *Model) bodyHeight() int   { return max(0, m.height-2) } // minus header and footer

// chatHeight is the height of the conversation box above the input box.
func (m *Model) chatHeight() int { return max(4, m.bodyHeight()-inputHeight-2) }

// layout sizes the widgets after the window changed.
func (m *Model) layout() {
	atBottom := m.history.AtBottom()
	inner := max(1, m.chatWidth()-4) // border and padding
	m.input.SetWidth(inner)
	m.history.SetWidth(inner)
	m.history.SetHeight(max(1, m.chatHeight()-4)) // border, title and typing line
	m.renderHistory(atBottom)
}

// renderHistory fills the history viewport with the selected conversation.
func (m *Model) renderHistory(toBottom bool) {
	c := m.current()
	if c == nil {
		m.history.SetContent("")
		return
	}
	if len(c.entries) == 0 {
		m.history.SetContent(mutedStyle.Render("No messages yet. Say hello to " + c.node.Name + "."))
		return
	}
	body := lipgloss.NewStyle().Width(m.history.Width()).PaddingLeft(2)
	blocks := make([]string, len(c.entries))
	for i, e := range c.entries {
		name, color := c.node.Name, peer
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
	if m.showSplash {
		v.SetContent(m.splashView())
		return v
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, m.sidebarView(), m.chatView())
	v.SetContent(lipgloss.JoinVertical(lipgloss.Left, m.headerView(), body, m.footerView()))
	return v
}

// splashView shows the ouroboros animation centered on the screen.
func (m *Model) splashView() string {
	art := lipgloss.NewStyle().Foreground(accent).Render(logoText(splashLogo(m.splashElapsed), m.width))
	title := boldStyle.Foreground(accent).Render("ouroboros")
	content := lipgloss.JoinVertical(lipgloss.Center, art, "", title)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
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
		state = lipgloss.NewStyle().Foreground(warn).Render("○ not configured") + mutedStyle.Render("  press c to set up")
	default:
		state = lipgloss.NewStyle().Foreground(bad).Render("○ offline") +
			mutedStyle.Render(fmt.Sprintf("  %s · %s · press s to start", s.ID, s.Name))
	}
	return ansi.Truncate(title+" "+state, m.width, "…")
}

func (m *Model) sidebarView() string {
	w, h := m.sidebarWidth(), m.bodyHeight()
	inner := max(1, w-4)
	title := boldStyle.Render("Nodes")
	if m.discovering {
		title += " " + m.spinner.View()
	}
	lines := []string{title, ""}
	if len(m.order) == 0 {
		lines = append(lines, mutedStyle.Render("no nodes found"), mutedStyle.Render("press r to search"))
	}

	// Each node takes two lines; scroll so the selection stays visible.
	visible := max(1, (h-4)/2)
	first := max(0, min(m.selected-visible+1, len(m.order)-visible))
	for i := first; i < min(len(m.order), first+visible); i++ {
		lines = append(lines, m.nodeLines(m.convs[m.order[i]], i == m.selected, inner)...)
	}
	content := strings.Join(lines, "\n")
	return box(m.focus == focusList).Width(w).Height(h).MaxHeight(h).Render(content)
}

func (m *Model) nodeLines(c *conversation, selected bool, width int) []string {
	marker, nameStyle := "  ", lipgloss.NewStyle()
	if selected {
		marker, nameStyle = lipgloss.NewStyle().Foreground(accent).Render("› "), boldStyle
	}
	name := c.node.Name
	if c.node.ID == m.status.ID {
		name += " (you)"
	}
	var badge string
	switch {
	case c.pending:
		badge = m.spinner.View()
	case c.unread > 0:
		badge = lipgloss.NewStyle().Foreground(accent).Render(fmt.Sprintf("●%d", c.unread))
	case !c.online:
		badge = mutedStyle.Render("offline")
	}
	if !c.online {
		nameStyle = nameStyle.Foreground(muted)
	}
	room := max(1, width-2-lipgloss.Width(badge)-1)
	left := marker + nameStyle.Render(ansi.Truncate(name, room, "…"))
	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(badge))
	detail := "  " + mutedStyle.Render(ansi.Truncate(c.node.Model+" · "+c.node.ID, width-2, "…"))
	return []string{left + strings.Repeat(" ", gap) + badge, detail}
}

func (m *Model) chatView() string {
	w, h := m.chatWidth(), m.chatHeight()
	inner := max(1, w-4)
	c := m.current()

	var content string
	if c == nil {
		if inner >= logoWidth() && h-2 >= len(logoLines)+2 { // border, blank line and hint
			content = lipgloss.JoinVertical(lipgloss.Left,
				lipgloss.NewStyle().Foreground(accent).Render(logoText(logoLines, inner)),
				"",
				mutedStyle.Render("Select a node to chat with it."))
		} else {
			content = mutedStyle.Render("Select a node to chat with it.")
		}
	} else {
		title := boldStyle.Foreground(peer).Render(c.node.Name)
		info := " · " + c.node.Model + " · " + c.node.Addr()
		if !c.online {
			info += " · offline"
		}
		title = ansi.Truncate(title+mutedStyle.Render(info), inner, "…")
		var typing string
		if c.pending {
			typing = m.spinner.View() + mutedStyle.Render(" "+c.node.Name+" is thinking…")
		}
		content = lipgloss.JoinVertical(lipgloss.Left, title, m.history.View(), typing)
	}
	chat := box(false).Width(w).Height(h).MaxHeight(h).Render(content)
	input := box(m.focus == focusInput).Width(w).Render(m.input.View())
	return lipgloss.JoinVertical(lipgloss.Left, chat, input)
}

func (m *Model) footerView() string {
	toggle := "start"
	if m.status.Running {
		toggle = "stop"
	}
	help := "↑↓ select · enter chat · n new chat · s " + toggle + " node · c setup · r refresh · q quit"
	if m.focus == focusInput {
		help = "enter send · alt+enter new line · esc nodes · pgup/pgdn scroll · ctrl+c quit"
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
