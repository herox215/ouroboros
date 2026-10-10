package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The settings screen is what the command /settings opens: the configuration
// of the node in one place, by categories. It starts with the list of them:
// ↑ ↓ select one, enter opens it, and esc goes back to the chat. A category
// shows each of its things on a line of its own with what can be done with it:
// ↑ ↓ select a line, ← → what to do with it, enter does it, and esc goes back
// to the categories. A text, like the name, is typed into a line below the
// list: enter saves it, esc keeps what was.
//
// Every row takes the same lines, what can be done with it included, and the
// window follows the selection by the line: what is read stands still while
// the selection moves, and only what leaves the window moves it.
//
// Every change is saved at once, after the node checked it, and the footer
// says from when it holds. What needs ouro to start anew says so on its line.
// The keys of the node and of the API are never shown: the setup changes the
// key, and /setup and /model are a line here like everything else.

type (
	// Settings is the configuration as the settings screen shows it.
	Settings = plugin.Settings
	// Setting is one change made on the settings screen.
	Setting = plugin.Setting
)

// settingsMsg is the configuration, on the way to the settings screen.
type settingsMsg struct {
	settings Settings
	err      error
}

// changedMsg is how a change on the settings screen went.
type changedMsg struct {
	done string
	err  error
}

// row is a line of the settings screen.
type row struct {
	section string // the category it is in
	label   string
	value   string
	note    string // what it means, muted below it
	warn    bool   // the note is a warning
	actions []action
}

// action is what can be done with a row.
type action struct {
	name string
	run  func(m *Model) tea.Cmd
}

// edit is a text that is typed on the settings screen.
type edit struct {
	label  string
	change func(value string) Setting
}

// openSettings opens the settings screen.
// settingsScreen is the state of the settings screen.
type settingsScreen struct {
	values   Settings        // the configuration
	err      error           // why it is not there
	rows     []row           // what the screen shows of it
	category int             // the category selected, of sections
	opened   bool            // its rows are shown, not the list of categories
	sel      int             // the row selected, of those of the category
	top      int             // the line of a category the screen starts at
	editing  *edit           // the text that is typed, if one is
	input    textinput.Model // where it is typed
}

// reload asks for the configuration of the node anew.
func (c *settingsScreen) reload(m *Model) tea.Cmd {
	return func() tea.Msg { return c.load(m) }
}

func (m *Model) openSettings() tea.Cmd {
	m.screen = screenSettings
	m.config.category, m.config.opened = 0, false
	m.config.sel, m.choice, m.config.top = 0, 0, 0
	m.config.editing = nil
	return m.config.reload(m)
}

// loadSettings reads the configuration for the settings screen.
func (c *settingsScreen) load(m *Model) tea.Msg {
	s, err := m.backend.Settings()
	return settingsMsg{s, err}
}

// settingsLoaded takes the configuration that was read.
func (c *settingsScreen) loaded(m *Model, msg settingsMsg) {
	c.values, c.err, c.rows = msg.settings, msg.err, nil
	if msg.err == nil {
		c.rows = rows(msg.settings)
	}
	c.sel = min(c.sel, max(len(c.shown(m))-1, 0))
	m.choice = min(m.choice, max(len(c.selectedRow(m).actions)-1, 0))
}

// shown are the rows of the category selected.
func (c *settingsScreen) shown(m *Model) []row {
	var out []row
	for _, r := range c.rows {
		if r.section == sections[c.category] {
			out = append(out, r)
		}
	}
	return out
}

// selectedRow is the row that is selected, an empty one if there is none.
func (c *settingsScreen) selectedRow(m *Model) row {
	if shown := c.shown(m); c.sel < len(shown) {
		return shown[c.sel]
	}
	return row{}
}

// change makes the change s and reads the configuration anew.
func (c *settingsScreen) change(m *Model, s Setting) tea.Cmd {
	return func() tea.Msg {
		done, err := m.backend.ChangeSetting(s)
		return changedMsg{done, err}
	}
}

// changed shows how a change went, and reads the configuration anew.
func (c *settingsScreen) changed(m *Model, msg changedMsg) tea.Cmd {
	if msg.err != nil {
		m.setNotice(msg.err.Error(), true)
	} else {
		m.setNotice(msg.done, false)
	}
	return tea.Batch(c.reload(m), m.refresh())
}

// startEdit lets the owner type the text of label, starting from value.
func (c *settingsScreen) startEdit(m *Model, label, value string, change func(string) Setting) tea.Cmd {
	c.editing = &edit{label, change}
	c.input = textinput.New()
	c.input.SetValue(value)
	c.input.CursorEnd()
	c.input.SetWidth(max(10, m.width-8))
	return c.input.Focus()
}

// handleSettingsKey handles keys on the settings screen.
func (c *settingsScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	if c.editing != nil {
		switch msg.String() {
		case "esc":
			c.editing = nil
			m.setNotice("nothing changed", false)
			return nil
		case "enter":
			s := c.editing.change(c.input.Value())
			c.editing = nil
			return c.change(m, s)
		}
		var cmd tea.Cmd
		c.input, cmd = c.input.Update(msg)
		return cmd
	}
	if !c.opened {
		switch msg.String() {
		case "esc":
			m.screen = screenChat
		case "up", "k":
			c.category = max(0, c.category-1)
		case "down", "j":
			c.category = min(len(sections)-1, c.category+1)
		case "enter":
			if len(c.rows) > 0 {
				c.opened, c.sel, m.choice, c.top = true, 0, 0, 0
			}
		}
		return nil
	}
	switch msg.String() {
	case "esc":
		c.opened, c.top = false, 0
	case "up", "k":
		c.sel, m.choice = max(0, c.sel-1), 0
	case "down", "j":
		c.sel, m.choice = min(max(len(c.shown(m))-1, 0), c.sel+1), 0
	case "left", "h":
		m.choice = max(0, m.choice-1)
	case "right", "l":
		m.choice = min(max(len(c.selectedRow(m).actions)-1, 0), m.choice+1)
	case "enter":
		if actions := c.selectedRow(m).actions; m.choice < len(actions) {
			return actions[m.choice].run(m)
		}
	}
	return nil
}

// The categories of the settings screen.
const (
	nodeSection      = "The node"
	agentsSection    = "The agents"
	pluginsSection   = "Plugins"
	ownerSection     = "Tools of your agent"
	peersSection     = "Tools for peers"
	protectedSection = "Protected paths"
)

// sections are the categories in the order the settings screen lists them.
var sections = []string{nodeSection, agentsSection, pluginsSection, ownerSection, peersSection, protectedSection}

// contents is what the list of categories says is in each.
var contents = map[string]string{
	nodeSection:      "the name, the description, the access to the LLM, the model",
	agentsSection:    "the context provider, bash without a sandbox",
	pluginsSection:   "which plugins are turned on",
	ownerSection:     "what your agent may do when you ask it",
	peersSection:     "what your agent may do when a peer asks it",
	protectedSection: "the paths no tool for files touches",
}

// about is what an open category says of its lines.
var about = map[string]string{
	nodeSection:      "The access and the model are changed with the setup and /model.",
	agentsSection:    "A change holds from the next message, for you and for peers.",
	pluginsSection:   "Plugins are part of the build. Turning one on or off holds once ouro starts anew: quit and start ouro, /stop and /start the node.",
	ownerSection:     "What your agent may do when you ask it. A change holds from the next message.",
	peersSection:     "What your agent may do when a connected peer asks it. A change holds from the next message.",
	protectedSection: "For you and for peers alike. A change holds from the next call of a tool.",
}

// rows are the lines of the settings screen for s.
func rows(s Settings) []row {
	var out []row
	add := func(section, label, value, note string, actions ...action) {
		out = append(out, row{section: section, label: label, value: value, note: note, actions: actions})
	}
	editing := func(label, value string, change func(string) Setting) action {
		return action{"change", func(m *Model) tea.Cmd { return m.config.startEdit(m, label, value, change) }}
	}
	set := func(name string, s Setting) action {
		return action{name, func(m *Model) tea.Cmd { return m.config.change(m, s) }}
	}

	add(nodeSection, "Name", s.Name, "what the node is called on the network, and your agent too",
		editing("the name of the node", s.Name, func(v string) Setting { return Setting{Kind: plugin.SetName, Value: v} }))
	about := "public: anyone reads it in the agent card; empty is the default"
	if s.DescriptionDefault {
		about = "the default; public: anyone reads it in the agent card"
	}
	add(nodeSection, "Description", s.Description, about,
		editing("the description of the node", s.Description, func(v string) Setting { return Setting{Kind: plugin.SetDescription, Value: v} }))
	access := s.Access
	if s.About != "" {
		access += " · " + s.About
	}
	add(nodeSection, "Access", access, "how the LLM is reached; what it signs in with is never shown",
		action{"open /setup", (*Model).setup})
	add(nodeSection, "Model", s.Model, "", action{"open /model", (*Model).openModel})
	add(nodeSection, "Node ID", s.ID, "the fingerprint of the key of the node, which nothing here changes")

	provider, choices := "none", []action{set("none", Setting{Kind: plugin.SetContextProvider})}
	if s.ContextProvider != "" {
		provider = s.ContextProvider
	}
	for _, p := range s.ContextProviders {
		choices = append(choices, set(p, Setting{Kind: plugin.SetContextProvider, Value: p}))
	}
	add(agentsSection, "Context provider", provider,
		"what prepares the context of every call of the LLM; none sends the conversation as the memory keeps it", choices...)
	allowed, note := "no", "bash runs its commands in a sandbox: without network, and only where the tools for files may write"
	toggle := set("allow", Setting{Kind: plugin.SetBashWithoutSandbox, On: true})
	if s.BashWithoutSandbox {
		allowed, note = "yes", "bash runs its commands as you: with network, the secrets of the node and the source of the core in reach"
		toggle = set("refuse", Setting{Kind: plugin.SetBashWithoutSandbox})
	}
	if s.Sandbox != "" {
		note += "; there is no sandbox here (" + s.Sandbox + "), so without yes bash runs nothing"
	}
	add(agentsSection, "Bash without a sandbox", allowed, note, toggle)

	if s.PluginsKept != "" {
		out = append(out, row{section: pluginsSection, label: "Turned off", value: "none: all run",
			note: "the plugins turned off cannot be, ouro could not start without them: " + s.PluginsKept, warn: true})
	}
	for _, p := range s.Plugins {
		out = append(out, pluginRow(p))
	}

	for who, section := range []string{ownerSection, peersSection} {
		out = append(out, toolRows(section, plugin.Audience(who), s.Tools[who])...)
	}

	for i, p := range s.Protected {
		rest := append(append([]string{}, s.Protected[:i]...), s.Protected[i+1:]...)
		add(protectedSection, p, "", "", set("remove", Setting{Kind: plugin.SetProtected, Value: strings.Join(rest, "\n")}))
	}
	add(protectedSection, "Add a path", "", "absolute, or starting with ~", editing("a path to protect", "", func(v string) Setting {
		return Setting{Kind: plugin.SetProtected, Value: strings.Join(append(append([]string{}, s.Protected...), v), "\n")}
	}))
	if s.ProtectedDefault {
		add(protectedSection, "The default", "in use", "the usual places of keys and credentials")
	} else {
		add(protectedSection, "The default", "not in use", "the usual places of keys and credentials",
			set("use it", Setting{Kind: plugin.SetProtected, On: true}))
	}
	return out
}

// pluginRow is the line of a plugin.
func pluginRow(p plugin.PluginState) row {
	r := row{section: pluginsSection, label: p.Name}
	turn := func(name string, on bool) action {
		return action{name, func(m *Model) tea.Cmd {
			return m.config.change(m, Setting{Kind: plugin.SetPlugin, Name: p.Name, On: on})
		}}
	}
	switch {
	case p.Missing:
		r.value, r.note = "off", "not part of this build"
		r.actions = []action{turn("forget", true)}
	case p.Fixed != "":
		r.value, r.note = "on", p.Fixed
	case p.On:
		r.value, r.actions = "on", []action{turn("turn off", false)}
	default:
		r.value, r.actions = "off", []action{turn("turn on", true)}
	}
	if !p.Missing && p.On != p.Running {
		r.note, r.warn = "runs until ouro starts anew", true
		if !p.Running {
			r.note = "does not run until ouro starts anew"
		}
	}
	return r
}

// toolRows are the lines of a list of tools.
func toolRows(section string, who plugin.Audience, l plugin.ToolList) []row {
	all := row{section: section, label: "All tools", value: "no"}
	if l.All {
		all.value = "yes"
	}
	if l.Unset {
		all.value += ", the default"
	}
	all.note = "every tool, also those of plugins turned on later"
	if who == plugin.ForPeers {
		all.note = "every tool but those for your agent alone, also those of plugins turned on later"
	}
	all.actions = []action{{"turn on", func(m *Model) tea.Cmd {
		return m.config.change(m, Setting{Kind: plugin.SetAllTools, For: who, On: true})
	}}}
	if l.All {
		all.actions = []action{{"turn off", func(m *Model) tea.Cmd { return m.config.change(m, Setting{Kind: plugin.SetAllTools, For: who}) }}}
	}
	out := []row{all}
	for _, t := range l.Tools {
		r := row{section: section, label: t.Name, note: firstSentence(t.Description)}
		switch {
		case who == plugin.ForPeers && t.OwnerOnly:
			r.value = "for your agent alone"
		case t.Allowed && !t.Listed:
			r.value = "allowed, as all tools are"
		case t.Allowed:
			r.value = "allowed"
		default:
			r.value = "not allowed"
		}
		if t.Settings != "" {
			r.note = "settings " + t.Settings + " · " + r.note
		}
		if who == plugin.ForPeers && t.OwnerOnly {
			out = append(out, r)
			continue
		}
		name := t.Name
		toggle := action{"allow", func(m *Model) tea.Cmd {
			return m.config.change(m, Setting{Kind: plugin.SetTool, For: who, Name: name, On: true})
		}}
		if t.Allowed {
			toggle = action{"refuse", func(m *Model) tea.Cmd { return m.config.change(m, Setting{Kind: plugin.SetTool, For: who, Name: name}) }}
		}
		r.actions = []action{toggle}
		if t.Configurable {
			value := t.Settings
			if value == "" {
				value = "{}"
			}
			r.actions = append(r.actions, action{"settings", func(m *Model) tea.Cmd {
				return m.config.startEdit(m, "the settings of "+name, value, func(v string) Setting {
					return Setting{Kind: plugin.SetToolSettings, For: who, Name: name, Value: v}
				})
			}})
		}
		out = append(out, r)
	}
	return out
}

// firstSentence is the first sentence of text.
func firstSentence(text string) string {
	if i := strings.Index(text, ". "); i >= 0 {
		return text[:i+1]
	}
	return text
}

// settingsView shows the list of categories, or the lines of the one that
// is open around the one selected, with what can be done with it, and the
// text that is typed. It moves the window and keeps it (settingsTop), which
// the key handler cannot: only here is known how many lines a row takes.
func (c *settingsScreen) view(m *Model) string {
	w, h := m.width, m.bodyHeight()
	head := []string{boldStyle.Render("Settings"), ""}
	var lines []string
	at, height := 0, 1 // where what is selected is, and how many lines it takes
	inner := max(10, w-4)
	selected := lipgloss.NewStyle().Foreground(accent)
	switch {
	case c.err != nil:
		lines = append(lines, errStyle.Render("✗ "+c.err.Error()))
	case len(c.rows) == 0:
		lines = append(lines, m.spinner.View()+mutedStyle.Render(" reading the configuration…"))
	case !c.opened:
		nameWidth := 0
		for _, s := range sections {
			nameWidth = max(nameWidth, ansi.StringWidth(s))
		}
		for i, s := range sections {
			marker, name := "  ", lipgloss.NewStyle().Width(nameWidth+2).Render(s)
			if i == c.category {
				at = len(lines)
				marker, name = selected.Render("› "), selected.Width(nameWidth+2).Render(s)
			}
			lines = append(lines, ansi.Truncate(marker+name+mutedStyle.Render(contents[s]), inner, "…"))
		}
	default:
		section := sections[c.category]
		head = []string{boldStyle.Render("Settings") + mutedStyle.Render(" › ") + boldStyle.Render(section)}
		head = append(append(head, wrapped(about[section], "", inner, mutedStyle)...), "")
		shown := c.shown(m)
		labelWidth := 0
		for _, r := range shown {
			labelWidth = max(labelWidth, ansi.StringWidth(r.label))
		}
		labelWidth = min(labelWidth, 28)
		for i, r := range shown {
			if i == c.sel {
				at = len(lines)
			}
			marker, label := "  ", lipgloss.NewStyle().Width(labelWidth+2).Render(ansi.Truncate(r.label, labelWidth, "…"))
			if i == c.sel {
				marker = selected.Render("› ")
				label = selected.Width(labelWidth + 2).Render(ansi.Truncate(r.label, labelWidth, "…"))
			}
			lines = append(lines, ansi.Truncate(marker+label+r.value, inner, "…"))
			style := mutedStyle
			if r.warn {
				style = lipgloss.NewStyle().Foreground(warn)
			}
			lines = append(lines, wrapped(r.note, "    ", inner, style)...)
			// What can be done with a line holds a line of its own on every
			// row, empty on those not selected: the rows below it stay where
			// they are while the selection moves.
			doing := ""
			if i == c.sel {
				names := make([]string, len(r.actions))
				for j, a := range r.actions {
					names[j] = a.name
				}
				if len(names) > 0 {
					doing = choices(names, m.choice)
				}
				height = len(lines) + 1 - at
			}
			lines = append(lines, "    "+doing)
		}
	}
	room := max(1, h-2-len(head)) // the border
	var bottom []string
	if c.editing != nil {
		bottom = []string{"", boldStyle.Render(c.editing.label) + mutedStyle.Render("  enter saves · esc keeps what was"), c.input.View()}
		room = max(1, room-len(bottom))
	}
	// The window follows what is selected by the line and moves only when
	// it would leave it, so that what is read stands still while selecting.
	top := c.top
	if top > at || top+room < at+height {
		top = at
	}
	top = max(0, min(top, max(0, len(lines)-room)))
	c.top = top
	lines = lines[top:min(len(lines), top+room)]
	all := append(append(head, lines...), bottom...)
	return box(true, m.light).Width(w).Height(h).MaxHeight(h).Render(strings.Join(all, "\n"))
}

// wrapped is text broken into lines of width, each after indent, in style.
func wrapped(text, indent string, width int, style lipgloss.Style) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(ansi.Wordwrap(text, max(1, width-len(indent)), ""), "\n") {
		out = append(out, style.Render(indent+line))
	}
	return out
}
