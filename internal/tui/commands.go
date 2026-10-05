package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
	{"new", "start a new conversation", func(m *Model) tea.Cmd { m.newConversation(); return nil }},
	{"start", "start the node", (*Model).start},
	{"stop", "stop the node", (*Model).stop},
	{"setup", "set up the node", (*Model).setup},
	{"requests", "decide on connection requests and proposals", (*Model).openRequests},
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
	return box(true).Width(w).Height(h).MaxHeight(h).Render(strings.Join(lines, "\n"))
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
