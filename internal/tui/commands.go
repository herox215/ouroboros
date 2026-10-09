package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A command is what the owner types into the message input with a slash in
// front, like /new. It runs in the UI and never goes to the agent: only the
// owner runs commands. Everything the UI does is a command; keys only move
// around, scroll and quit (ctrl+c).
type command struct {
	name string // without the slash
	help string
	run  func(m *Model) tea.Cmd
}

// own are the commands of the UI. Plugins add theirs, see bind.
var own = []command{
	{"new", "start a new conversation; the one shown goes on", (*Model).newConversation},
	{"session", "list the conversations and switch to one", (*Model).openSessions},
	{"cancel", "stop the answer on its way in this conversation", (*Model).cancel},
	{"start", "start the node", (*Model).start},
	{"stop", "stop the node", (*Model).stop},
	{"setup", "set up the node", (*Model).setup},
	{"model", "choose the model of the provider", (*Model).openModel},
	{"effort", "choose how much the model thinks before it answers", (*Model).openEffort},
	{"settings", "the configuration", (*Model).openSettings},
	{"context", "show what the LLM is sent in this conversation", (*Model).openContext},
	{"debug", "turn the debug mode on or off: the log of the node then tells much more", (*Model).toggleDebug},
	{"pending", "decide on what is pending: connection requests and proposals", (*Model).openRequests},
	{"help", "list the commands", func(m *Model) tea.Cmd { m.screen = screenHelp; return nil }},
	{"quit", "quit (ctrl+c does too)", (*Model).leave},
}

// ownNames are the names of the commands of the UI.
func ownNames() []string {
	names := make([]string, len(own))
	for i, c := range own {
		names[i] = c.name
	}
	return names
}

// isCommandLine reports whether what the owner typed is a command rather than
// a message. A message that starts with a slash is typed with two.
func isCommandLine(text string) bool {
	return strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "//")
}

// runCommand runs the command line the owner typed. The input keeps a
// command it does not know, to be corrected.
func (m *Model) runCommand(line string) tea.Cmd {
	fields := strings.Fields(strings.TrimPrefix(line, "/"))
	if len(fields) == 0 {
		m.setNotice("type a command after the slash: /help lists them", true)
		return nil
	}
	for _, c := range m.commands {
		if c.name != fields[0] {
			continue
		}
		if len(fields) > 1 {
			m.setNotice("/"+c.name+" takes no arguments", true)
			return nil
		}
		m.input.Reset()
		return c.run(m)
	}
	m.setNotice("unknown command /"+fields[0]+": /help lists them", true)
	return nil
}

// matching are the commands that start with what the owner typed after the
// slash, while the first word is not complete yet.
func (m *Model) matching(text string) []command {
	typed := strings.TrimPrefix(text, "/")
	if !isCommandLine(text) || strings.ContainsAny(typed, " \n") {
		return nil
	}
	var found []command
	for _, c := range m.commands {
		if strings.HasPrefix(c.name, typed) {
			found = append(found, c)
		}
	}
	return found
}

// maxSuggestions is how many of the commands that fit are listed at once; the
// list scrolls to the one selected.
const maxSuggestions = 8

// suggestions are the commands the list above the input offers while one is
// typed, and which of them is selected.
func (m *Model) suggestions() ([]command, int) {
	found := m.matching(m.input.Value())
	if m.suggest >= len(found) {
		m.suggest = 0
	}
	return found, m.suggest
}

// handleSuggestionKey handles the keys of the list of suggestions: up and
// down select a command, tab completes the input to it, and enter runs it.
// It reports whether the key was one of them.
func (m *Model) handleSuggestionKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	found, selected := m.suggestions()
	if len(found) == 0 {
		return nil, false
	}
	switch msg.String() {
	case "up":
		m.suggest = (selected + len(found) - 1) % len(found)
	case "down":
		m.suggest = (selected + 1) % len(found)
	case "tab":
		m.input.SetValue("/" + found[selected].name)
		m.suggest = 0
	case "enter":
		m.remember("/" + found[selected].name)
		m.input.Reset()
		m.suggest = 0
		return found[selected].run(m), true
	default:
		return nil, false
	}
	return nil, true
}

// suggestionLines is the list of suggestions, a command a line, the one
// selected marked. It takes at most rows lines and shows those around the
// one selected.
func (m *Model) suggestionLines(rows int) []string {
	found, selected := m.suggestions()
	rows = min(rows, maxSuggestions, len(found))
	if rows <= 0 {
		return nil
	}
	top := max(0, selected-rows+1)
	width := 0
	for _, c := range found {
		width = max(width, len(c.name))
	}
	lines := make([]string, rows)
	for i := range lines {
		c := found[top+i]
		mark, name := "  ", lipgloss.NewStyle().Foreground(accent)
		if top+i == selected {
			mark, name = boldStyle.Foreground(accent).Render("› "), boldStyle.Foreground(accent)
		}
		lines[i] = ansi.Truncate("  "+mark+name.Width(width+3).Render("/"+c.name)+mutedStyle.Render(c.help), m.width, "…")
	}
	return lines
}

func (m *Model) start() tea.Cmd {
	switch {
	case !m.status.Configured:
		return m.setup()
	case m.status.Running:
		m.setNotice("the node is running", false)
		return nil
	}
	m.setNotice("starting node…", false)
	return func() tea.Msg { return actionMsg{"node started", m.backend.Start()} }
}

func (m *Model) stop() tea.Cmd {
	if !m.status.Running {
		m.setNotice("the node is not running", false)
		return nil
	}
	m.setNotice("stopping node…", false)
	return func() tea.Msg { return actionMsg{"node stopped", m.backend.Stop()} }
}

// toggleDebug turns the debug mode of the node on or off. The header shows
// it once the status is read anew.
func (m *Model) toggleDebug() tea.Cmd {
	on, done := !m.status.Debug, "debug mode off"
	if on {
		done = "debug mode on: the log of the node (ouro.log) tells much more"
	}
	return func() tea.Msg { return actionMsg{done, m.backend.SetDebug(on)} }
}

// helpView lists the commands.
func (m *Model) helpView() string {
	w, h := m.width, m.bodyHeight()
	width := 0
	for _, c := range m.commands {
		width = max(width, len(c.name))
	}
	lines := []string{boldStyle.Render("Commands"), ""}
	for _, c := range m.commands {
		lines = append(lines, lipgloss.NewStyle().Foreground(accent).Width(width+3).Render("/"+c.name)+mutedStyle.Render(c.help))
	}
	lines = append(lines, "", mutedStyle.Render("Type them into the message input. A message that starts with a slash takes two: //etc"))
	return box(true, m.light).Width(w).Height(h).MaxHeight(h).Render(strings.Join(lines, "\n"))
}

// choices shows names in a row, the one at selected marked.
func choices(names []string, selected int) string {
	parts := make([]string, len(names))
	for i, n := range names {
		if i == selected {
			parts[i] = boldStyle.Foreground(accent).Render("[" + n + "]")
		} else {
			parts[i] = mutedStyle.Render(" " + n + " ")
		}
	}
	return strings.Join(parts, " ")
}
