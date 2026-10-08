package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"ouroboros/plugin"
)

// ownUI is who holds a command of the UI, as a problem names it.
const ownUI = "the terminal UI"

// body is what the screen of a plugin shows.
type body struct {
	text   string
	err    error
	loaded bool // otherwise it is still on its way
}

// bind gives the contributions of the plugins their commands, next to own,
// the commands of the UI. It returns the screens that got one, each with the
// actions that can run, and the commands that got theirs.
//
// A name has one meaning. The UI keeps its own, and of two contributions
// that claim the same name the first one gets it, where the screens come
// before the commands. The others are left out, and so is a contribution
// without a name or without what it does, and an action without either.
// Each of them is named in problems, which the UI shows when it opens: a
// plugin that claims a name in vain does not stop the UI, and its user
// learns what is missing.
func bind(own []string, screens []plugin.Screen, slashes []plugin.Slash) (bound []plugin.Screen, commands []plugin.Slash, problems []string) {
	taken := map[string]string{}
	for _, name := range own {
		taken[name] = ownUI
	}
	// claim gives name to who unless it is taken, and reports whether it did.
	claim := func(name, who string, complete bool) bool {
		holder, isTaken := taken[name]
		switch {
		case !isCommand(name) || !complete:
			problems = append(problems, fmt.Sprintf("%s lacks a command or what it does: left out", who))
		case isTaken:
			problems = append(problems, fmt.Sprintf("/%s is taken by %s: %s is left out", name, holder, who))
		default:
			taken[name] = who
			return true
		}
		return false
	}

	for _, s := range screens {
		who := fmt.Sprintf("the screen %q", s.Title)
		if !claim(s.Command, who, s.Body != nil) {
			continue
		}
		var actions []plugin.Action
		for _, a := range s.Actions {
			if a.Name == "" || a.Run == nil {
				problems = append(problems, fmt.Sprintf("an action on %s lacks a name or what it does: left out", who))
				continue
			}
			actions = append(actions, a)
		}
		s.Actions = actions
		bound = append(bound, s)
	}
	for _, c := range slashes {
		if claim(c.Name, fmt.Sprintf("%q", c.Help), c.Run != nil) {
			commands = append(commands, c)
		}
	}
	return bound, commands, problems
}

// isCommand reports whether name can be typed as a command: one word,
// without the slash.
func isCommand(name string) bool {
	return name != "" && !strings.HasPrefix(name, "/") && !strings.ContainsFunc(name, unicode.IsSpace)
}

// pluginScreen is the state of the screens the plugins contributed.
type pluginScreen struct {
	screens []plugin.Screen // of the plugins, as far as they got their command
	open    int             // the one that is shown
	body    body            // what it shows
}

// openScreen opens the screen of a plugin with the index i.
func (m *Model) openScreen(i int) tea.Cmd {
	m.screen, m.plugins.open, m.plugins.body, m.choice = screenPlugin, i, body{}, 0
	return m.plugins.load(m)
}

// handleScreenKey handles keys on the screen of a plugin.
func (p *pluginScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	actions := p.screens[p.open].Actions
	switch msg.String() {
	case "esc":
		m.screen = screenChat
	case "left", "h":
		m.choice = max(0, m.choice-1)
	case "right", "l":
		m.choice = min(max(0, len(actions)-1), m.choice+1)
	case "enter":
		if m.choice < len(actions) {
			return m.run(actions[m.choice].Run)
		}
	}
	return nil
}

// run runs what a plugin contributed. What it returns becomes the notice,
// and what the UI shows is loaded anew.
func (m *Model) run(do func(ctx context.Context) (string, error)) tea.Cmd {
	return func() tea.Msg {
		done, err := do(m.ctx)
		return actionMsg{done, err}
	}
}

// loadBody asks the plugin for what its screen, the one that is open, shows.
func (p *pluginScreen) load(m *Model) tea.Cmd {
	open, load := p.open, p.screens[p.open].Body
	return func() tea.Msg {
		text, err := load(m.ctx)
		return bodyMsg{open, body{text, err, true}}
	}
}

// screenView shows the screen of a plugin that is open, with its actions
// below what it shows.
func (p *pluginScreen) view(m *Model) string {
	w, h := m.width, m.bodyHeight()
	s := p.screens[p.open]
	text := p.body.text
	switch {
	case !p.body.loaded:
		text = mutedStyle.Render("…")
	case p.body.err != nil:
		text = errStyle.Render("✗ " + p.body.err.Error())
	}
	// The text is cut off to what the box has room for, so that its bottom
	// border stays: the box takes its border and padding, the actions, which
	// stay in sight below the text, a blank line and their own.
	rows, below := max(0, h-2), ""
	if len(s.Actions) > 0 {
		names := make([]string, len(s.Actions))
		for i, a := range s.Actions {
			names[i] = a.Name
		}
		rows, below = max(0, h-4), "\n\n"+choices(names, m.choice)
	}
	content := lipgloss.NewStyle().Width(max(1, w-4)).Height(rows).MaxHeight(rows).Render(boldStyle.Render(s.Title)+"\n\n"+text) + below
	return box(true, m.light).Width(w).Height(h).MaxHeight(h).Render(content)
}
