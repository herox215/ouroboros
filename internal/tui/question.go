package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The agent may ask the owner a question on its way to an answer, with
// answers to choose from, and waits for theirs: see plugin.SessionAsked. The
// question goes into the conversation like what the agent says, and above
// the composer the owner answers it, as the GUI asks for a decision: the
// arrow keys select an answer, enter gives it. Below the answers the
// composer is there to answer in own words instead: with it selected, what
// is sent from it is the answer. Typing selects it. esc stops the answer on
// its way, as ever.

// asking is a question the agent asks the owner in a conversation, while it
// waits for the answer.
type asking struct {
	question    string
	options     []string // the answers to choose from
	recommended string   // the one of them the agent would choose, "" for none
	sel         int      // the answer selected, or len(options) for own words
	sent        bool     // the answer chosen is on its way to the node
}

// own reports whether the owner answers in own words: the input is shown.
func (a *asking) own() bool { return a.sel == len(a.options) }

// write selects the row to answer in own words, as typing does. Without a
// question it does nothing.
func (a *asking) write() {
	if a != nil {
		a.sel = len(a.options)
	}
}

// unsent offers the question again, whose answer the node did not take: it
// is back in the input, to send again.
func (a *asking) unsent() {
	if a != nil {
		a.sent = false
		a.write()
	}
}

// ask takes a question of the agent, or with Done that the wait for its
// answer is over. The question is part of the conversation, and so is what
// the owner answered.
func (c *conversation) ask(e Event) {
	if !e.Done {
		c.add(entry{text: e.Text, at: e.At, question: true})
		c.asking = &asking{question: e.Text, options: e.Options, recommended: e.Recommended,
			sel: max(0, slices.Index(e.Options, e.Recommended))}
		return
	}
	c.asking = nil
	if e.Text != "" {
		c.add(entry{mine: true, text: e.Text, at: e.At})
	}
}

// handleQuestionKey handles the keys of a question the agent asks in the
// conversation shown: up and down select an answer, enter gives the one
// selected. With the row for own words selected the keys are the input's,
// but for up in an empty input, which goes back to the answers. It reports
// whether the key was one of them.
func (m *Model) handleQuestionKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	a := m.chat.asking
	if a == nil || a.sent {
		return nil, false
	}
	switch msg.String() {
	case "up":
		if a.own() && m.input.Value() != "" {
			return nil, false
		}
		a.sel = cycle(a.sel, -1, len(a.options)+1)
	case "down":
		if a.own() {
			return nil, false
		}
		a.sel = cycle(a.sel, 1, len(a.options)+1)
	case "enter":
		if a.own() {
			return nil, false // what is written is sent, and answers
		}
		return m.choose(a.options[a.sel]), true
	default:
		return nil, false
	}
	return nil, true
}

// choose answers the question of the conversation shown with option, one of
// the answers or what the owner wrote: what they say while the agent waits
// for an answer is that answer.
func (m *Model) choose(option string) tea.Cmd {
	c := m.chat
	c.asking.sent = true
	id := c.contextID
	return func() tea.Msg {
		return askedMsg{id, option, m.backend.Steer(plugin.WithMessageSource(m.ctx, plugin.MessageSource{Interface: "tui"}), id, option)}
	}
}

// questionLines is the question of the agent above the composer, w cells
// wide, as the GUI asks for a decision: the question, its answers to choose
// from as buttons, the one selected standing out, and what to do. Below the
// answers the composer is there to answer in own words: typing selects it,
// and so does up from the first answer. Answers that do not fit are left
// out. A click on an answer gives it, as in the GUI, and one on what to do
// below them selects the composer: the hits say where.
func (m *Model) questionLines(a *asking, w int) ([]string, []hit) {
	most := min(m.panelHeight(), max(4, m.height*panelShare/100))
	if most == 0 {
		return nil, nil
	}
	var lines []string
	if most >= 4 {
		lines = append(lines, boldStyle.Foreground(accent).Render("YOUR DECISION"))
	}
	question := strings.Split(lipgloss.Wrap(a.question, w, ""), "\n")
	var buttons []string
	var hits []hit // of the rows of buttons, from the first
	row := ""
	selectedRow := 0
	for i, name := range a.options {
		label := name
		if name == a.recommended {
			label += " · Recommended"
		}
		style := lipgloss.NewStyle().Foreground(ui.text).Background(ui.hover)
		switch {
		case i == a.sel && !a.sent:
			style = boldStyle.Foreground(ui.background).Background(accent)
		case name == a.recommended:
			style = style.Foreground(accent)
		}
		button := style.Render(" " + ansi.Truncate(label, max(1, w-2), "…") + " ")
		if row != "" && lipgloss.Width(row)+1+lipgloss.Width(button) > w {
			buttons, row = append(buttons, row), ""
		}
		if row != "" {
			row += " "
		}
		if i == a.sel {
			selectedRow = len(buttons)
		}
		hits = append(hits, hit{line: len(buttons), from: lipgloss.Width(row), to: lipgloss.Width(row) + lipgloss.Width(button), do: func() tea.Cmd {
			if a.sent || m.chat.asking != a {
				return nil
			}
			return m.choose(name)
		}})
		row += button
	}
	if row != "" {
		buttons = append(buttons, row)
	}
	hint := mutedStyle.Render("Choose an answer or write your own below.")
	switch {
	case a.sent:
		hint = mutedStyle.Render("Answer delivered – the assistant goes on …")
	case a.own() && len(a.options) > 0:
		hint = boldStyle.Foreground(accent).Render("› ") + mutedStyle.Render("Write your own answer below · ↑ back to the answers")
	case len(a.options) == 0:
		hint = mutedStyle.Render("Write your answer below.")
	}
	// Keep the selected answer visible while navigating a long list. The
	// question and hint each get a row before any extra text or spacing.
	buttonRoom := max(0, most-len(lines)-2)
	first := max(0, min(selectedRow-buttonRoom/2, len(buttons)-buttonRoom))
	count := min(len(buttons), buttonRoom)
	buttons = buttons[first : first+count]
	hits = shifted(hits, first, 0, count)
	room := max(0, most-len(lines)-1-len(buttons))
	if len(question) > room {
		question = question[:room]
		if room > 0 {
			question[room-1] = ansi.Truncate(question[room-1], max(0, w-1), "") + "…"
		}
	}
	lines = append(lines, question...)
	if len(buttons) > 0 {
		hits = shifted(hits, 0, len(lines), len(buttons))
		lines = append(lines, buttons...)
		hits = append(hits, onLine(len(lines), func() tea.Cmd { a.write(); return nil }))
	}
	return append(lines, hint), hits
}
