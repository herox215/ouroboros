package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"ouroboros/plugin"
)

// effortScreen is the slider of the effort, for /effort: how much the model
// thinks before it answers the owner in the session shown. What is chosen
// is also the effort of the node, which every session has that has none of
// its own, like a new one.
type effortScreen struct {
	// choices are the efforts to choose from, "" first: those the model
	// takes once known says that models.dev lists it, else plugin.Efforts.
	choices []string
	known   bool
	at      int       // of choices, the one the slider is at
	net     neuralNet // the mesh above the slider, see mesh.go
}

// effortGap is how far apart the names of the efforts are on their line.
const effortGap = 4

// width is how wide the line of the names of the efforts is.
func (s *effortScreen) width() int {
	w := effortGap * (len(s.choices) - 1)
	for _, effort := range s.choices {
		w += len(effortName(effort))
	}
	return w
}

// efforts say what each effort does, by its name.
var efforts = map[string]string{
	"auto":    "the model thinks as much as it does by itself",
	"none":    "no thinking: the fastest and cheapest answers",
	"minimal": "hardly any thinking, for the simplest questions",
	"low":     "a little thinking, for simple questions",
	"medium":  "thinking that fits most questions",
	"high":    "thorough thinking, for hard questions",
	"xhigh":   "very thorough thinking, for the hardest questions",
	"max":     "as much thinking as the model can: the slowest and most expensive answers",
}

// effortName is what an effort is called where it is shown: "" is "auto".
func effortName(effort string) string {
	if effort == "" {
		return "auto"
	}
	return effort
}

// openEffort opens the slider at the effort of the session shown.
func (m *Model) openEffort() tea.Cmd {
	if !m.status.Configured {
		m.setNotice("set up the node first: /setup", true)
		return nil
	}
	if m.chat.peer != "" {
		m.setNotice("the conversation of a peer is answered with the model's own effort", true)
		return nil
	}
	s := &m.effort
	s.choices, s.known = plugin.Efforts, false
	if takes, known := m.backend.Efforts(); known {
		s.choices, s.known = append([]string{""}, takes...), true
	}
	s.at = nearest(s.choices, m.chatEffort())
	s.net = neuralNet{w: s.width()}
	s.net.growTo(s.choices[s.at], m.ticks)
	m.screen = screenEffort
	return nil
}

// nearest is where in choices effort is, or else the most of them that is
// not more than it, in the order of plugin.AllEfforts.
func nearest(choices []string, effort string) int {
	if i := slices.Index(choices, effort); i >= 0 {
		return i
	}
	at, want := 0, slices.Index(plugin.AllEfforts, effort)
	for i, c := range choices {
		if n := slices.Index(plugin.AllEfforts, c); n >= 0 && n <= want {
			at = i
		}
	}
	return at
}

func (s *effortScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "left", "h":
		s.at = cycle(s.at, -1, len(s.choices))
		s.net.growTo(s.choices[s.at], m.ticks)
	case "right", "l":
		s.at = cycle(s.at, 1, len(s.choices))
		s.net.growTo(s.choices[s.at], m.ticks)
	case "esc":
		m.screen = screenChat
	case "enter":
		m.screen = screenChat
		effort, c := s.choices[s.at], m.chat
		c.effort, c.efforted = effort, true // as the node will tell
		return func() tea.Msg {
			err := m.backend.SetSessionEffort(m.ctx, c.contextID, effort)
			if err == nil {
				err = m.backend.SetEffort(effort)
			}
			return actionMsg{"effort " + effortName(effort) + " for this session", err}
		}
	}
	return nil
}

// view shows, in a dialog, the efforts on a line, the slider below them at
// the one it is at, the mesh of that one above them, and what it does.
func (s *effortScreen) view(m *Model) string {
	var names []string
	at := 0 // where the name the slider is at begins
	for i, effort := range s.choices {
		name := effortName(effort)
		if i == s.at {
			at = lipgloss.Width(strings.Join(names, strings.Repeat(" ", effortGap)))
			if i > 0 {
				at += effortGap
			}
			name = boldStyle.Foreground(accent).Render(name)
		} else {
			name = mutedStyle.Render(name)
		}
		names = append(names, name)
	}
	row := strings.Join(names, strings.Repeat(" ", effortGap))
	knob := at + len(effortName(s.choices[s.at]))/2
	track := lipgloss.NewStyle().Foreground(accent).Render(strings.Repeat("━", knob)+"●") +
		mutedStyle.Render(strings.Repeat("─", max(0, lipgloss.Width(row)-knob-1)))
	slider := append(s.net.view(s.choices[s.at], m.ticks), row, track)
	lines := []string{
		boldStyle.Render("Effort"), "",
		mutedStyle.Render("How much the model thinks before it answers you in this session."), "",
	}
	lines = append(lines, m.centered(slider...)...)
	lines = append(lines, "")
	lines = append(lines, m.centered(efforts[effortName(s.choices[s.at])])...)
	lines = append(lines, "",
		mutedStyle.Render(s.about(m.status.Model)),
		mutedStyle.Render("The less effort, the smaller the pictures the model is shown, unless it asks for more."),
		mutedStyle.Render("Other sessions keep theirs; a new one starts with the last chosen."),
		mutedStyle.Render("Peers are answered with the model's own effort."),
	)
	return m.dialog(lines)
}

// about says where the choices come from, for model.
func (s *effortScreen) about(model string) string {
	switch {
	case !s.known:
		return "models.dev does not list " + model + ": if it does not know an effort, it takes the nearest it knows, and the header shows which, like max → high."
	case len(s.choices) == 1:
		return "models.dev lists no effort for " + model + ": it thinks as it does by itself."
	}
	return "These are the efforts " + model + " takes, as models.dev lists them."
}
