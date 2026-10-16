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
// question goes into the conversation like what the agent says, and where
// the input is the owner answers it: the arrow keys select an answer, enter
// gives it. Below the answers is a row to answer in own words instead: with
// it selected the input is there to write in, and what is sent from it is
// the answer. Typing selects it. esc stops the answer on its way, as ever.

// ownWords is the row below the answers of a question: with it selected the
// owner writes what they answer.
const ownWords = "Chat about it"

// asking is a question the agent asks the owner in a conversation, while it
// waits for the answer.
type asking struct {
	question    string
	options     []string // the answers to choose from
	recommended string   // the one of them the agent would choose, "" for none
	sel         int      // the row selected: one of options, or len(options) for ownWords
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
		c.add(entry{text: e.Text, at: e.At})
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

// choose answers the question of the conversation shown with option: what
// the owner says while the agent waits for an answer is that answer.
func (m *Model) choose(option string) tea.Cmd {
	c := m.chat
	c.asking.sent = true
	id := c.contextID
	return func() tea.Msg {
		return askedMsg{id, option, m.backend.Steer(plugin.WithMessageSource(m.ctx, plugin.MessageSource{Interface: "tui"}), id, option)}
	}
}

// inputView is what the box of the input shows, width cells wide: the
// input, or while the agent asks in the conversation shown, the question on
// a line, the answers to choose from and the row for own words, with the
// input below it once that is selected. rows is how many rows there are for
// it beyond those of the input: answers that do not fit are left out around
// the one selected.
func (m *Model) inputView(width, rows int) string {
	a := m.chat.asking
	if a == nil || a.sent {
		return m.input.View()
	}
	// A question without answers to choose from is answered in own words:
	// there is no row to select for that.
	var names []string
	if len(a.options) > 0 {
		names = append(slices.Clone(a.options), ownWords)
	}
	list := make([]string, len(names))
	for i, name := range names {
		mark, style := "  ", lipgloss.NewStyle()
		if i == a.sel {
			mark, style = boldStyle.Foreground(accent).Render("› "), boldStyle.Foreground(accent)
		}
		line := mark + style.Render(name)
		switch {
		case i == len(a.options):
			line += mutedStyle.Render("  write your own answer")
		case name == a.recommended:
			line += mutedStyle.Render("  recommended")
		}
		list[i] = ansi.Truncate(line, width, "…")
	}
	fixed := 1 // the question
	if a.own() {
		fixed += inputHeight
	}
	shown := min(len(list), max(1, rows+inputHeight-fixed))
	top := max(0, min(a.sel-shown+1, len(list)-shown))
	lines := []string{boldStyle.Foreground(accent).Render("? ") + boldStyle.Render(short(a.question, max(1, width-2)))}
	lines = append(lines, list[top:top+shown]...)
	if a.own() {
		lines = append(lines, m.input.View())
	}
	return strings.Join(lines, "\n")
}

// givenUp is how many bottom rows of the history the panel covers: with
// commands that fit what is typed, or answers to a question of the agent.
func (m *Model) givenUp() int {
	panel := m.panel()
	if panel == "" {
		return 0
	}
	// The history begins below the top border of the chat.
	return min(m.history.Height(), max(0, m.history.Height()+1-m.panelTop(panel)))
}
