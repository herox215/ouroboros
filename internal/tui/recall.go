package tui

import tea "charm.land/bubbletea/v2"

// What the owner entered into the input, messages and commands alike, up
// brings back, the latest first, and down the other way: below the latest
// is what was typed before up was pressed, and below that an empty input,
// from which up brings it back again. A line that was brought back and
// changed is what is typed from then on. In an input of several lines, up
// and down move between them first, and up and down select a command while
// one is typed. The lines are kept while the UI runs, for every
// conversation.

// recall is where up and down are among the lines that were entered.
type recall struct {
	back  int    // how many lines back: 0 for what is typed, -1 for the empty input below it
	typed string // what was typed when up or down left it
	shown string // what up or down put into the input
}

// remember keeps line, which the owner entered, for up and down: with the
// paths of its pictures, which stay the same in every conversation. The
// same line twice in a row is kept once.
func (m *Model) remember(line string) {
	line = m.chat.expand(line)
	if n := len(m.entered); n == 0 || m.entered[n-1] != line {
		m.entered = append(m.entered, line)
	}
	m.recall = recall{}
}

// handleRecallKey handles up and down where they bring back what was
// entered. It reports whether the key was one of them.
func (m *Model) handleRecallKey(msg tea.KeyPressMsg) bool {
	up := msg.String() == "up"
	if !up && msg.String() != "down" {
		return false
	}
	r := &m.recall
	if r.back != 0 && m.input.Value() != r.shown {
		r.back = 0 // what was brought back was changed: it is typed now
	}
	if r.back == 0 {
		if found, _ := m.suggestions(); len(found) > 0 {
			return false // they select a command
		}
		if m.chat.TakesBack(m.input.Value()) {
			return false // up takes back what is queued
		}
	}
	line, info := m.input.Line(), m.input.LineInfo()
	if up && (line > 0 || info.RowOffset > 0) || !up && (line < m.input.LineCount()-1 || info.RowOffset < info.Height-1) {
		return false // the cursor moves to the next line of the input
	}
	if r.back == 0 {
		r.typed = m.input.Value()
	}
	back := r.back - 1
	if up {
		back = r.back + 1
	}
	if back > len(m.entered) || back < 0 && (r.back < 0 || r.typed == "") {
		return true // there is nothing older, or nothing below
	}
	r.back, r.shown = back, r.typed
	if back > 0 {
		r.shown = m.chat.relabel(m.entered[len(m.entered)-back])
	} else if back < 0 {
		r.shown = ""
	}
	m.input.SetValue(r.shown)
	return true
}
