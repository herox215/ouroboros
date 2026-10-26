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
// around, scroll and quit (ctrl+c): /keys and ctrl+k list them.
type command struct {
	name string // without the slash
	help string
	run  func(m *Model) tea.Cmd
}

// own are the commands of the UI. Plugins add theirs, see bind.
var own = []command{
	{"new", "start a conversation of its own, outside the thread; the one shown goes on", (*Model).newConversation},
	{"cd", "show or change the project directory; --workspace resets it", (*Model).showDirectory},
	{"session", "list the conversations and switch to one", (*Model).openSessions},
	{"thread", "back to the thread, the one conversation about everything", (*Model).openThread},
	{"cancel", "stop the answer and the tasks on their way here", (*Model).cancel},
	{"jobs", "list the commands that run in the background, also for peers, and stop one", (*Model).openJobs},
	{"setup", "set up the node", (*Model).setup},
	{"model", "choose the model of the provider", (*Model).openModel},
	{"effort", "choose how much the model thinks before it answers", (*Model).openEffort},
	{"settings", "the configuration", (*Model).openSettings},
	{"appearance", "light, dark, or as the terminal is: the next of them", (*Model).nextAppearance},
	{"context", "show what the LLM is sent in this conversation", (*Model).openContext},
	{"debug", "turn the debug mode on or off: the log of the node then tells much more", (*Model).toggleDebug},
	{"suggestions", "review suggested changes of ouro", (*Model).openSuggestions},
	{"undo", "take back the last memory ouro kept by itself here", (*Model).undo},
	{"keys", "list the keys (ctrl+k does too)", (*Model).openKeys},
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
	text := strings.TrimSpace(strings.TrimPrefix(line, "/"))
	fields := strings.Fields(text)
	if len(fields) == 0 {
		m.setNotice("type a command after the slash: /help lists them", true)
		return nil
	}
	for _, c := range m.commands {
		if c.name != fields[0] {
			continue
		}
		if c.name == "cd" {
			return m.changeDirectory(strings.TrimSpace(strings.TrimPrefix(text, c.name)))
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
// down select a command, tab completes the input to it, enter runs it, and
// esc closes the list with the command typed, leaving the answer on its way
// alone. It reports whether the key was one of them.
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
	case "esc":
		m.input.Reset()
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
// selected marked. It takes at most maxSuggestions lines and shows those
// around the one selected, of which it says the first.
func (m *Model) suggestionLines() ([]string, int) {
	found, selected := m.suggestions()
	rows := min(maxSuggestions, len(found))
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
	return lines, top
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

// helpView lists the commands: a click on one runs it.
func (m *Model) helpView() (string, []hit) {
	width := 0
	for _, c := range m.commands {
		width = max(width, len(c.name))
	}
	lines := []string{boldStyle.Render("Commands"), ""}
	var hits []hit
	for _, c := range m.commands {
		hits = append(hits, onLine(len(lines), func() tea.Cmd {
			m.screen = screenChat
			return c.run(m)
		}))
		lines = append(lines, lipgloss.NewStyle().Foreground(accent).Width(width+3).Render("/"+c.name)+mutedStyle.Render(c.help))
	}
	lines = append(lines, "", mutedStyle.Render("Type them into the message input. A message that starts with a slash takes two: //etc"))
	return m.dialog(lines), hits
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
