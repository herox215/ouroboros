package tui

import (
	"cmp"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The settings screen is what the command /settings opens: the configuration
// of the node in one place, by categories, in a dialog over the chat like the
// others. It starts with the list of them: ↑ ↓ select one, enter opens it,
// and esc goes back to the chat. A category shows each of its things on a
// line of its own with what can be done with it: ↑ ↓ select a line, ← →
// what to do with it, enter does it, and esc goes back one level. A text,
// like the name, is typed into a line below the list: enter saves it, esc
// keeps what was.
//
// A tool shows its settings below its line, each by its name, and its
// settings open on a level of their own: every setting on a line, with what
// it is set to, and what holds while it is not set. The lists of tools of
// single peers open the same way, from their category.
//
// Every row takes the same lines, what can be done with it included, and the
// window follows the selection by the line: what is read stands still while
// the selection moves, and only what leaves the window moves it.
//
// Every change is saved at once, after the node checked it, and the footer
// says from when it holds. What needs ouro to start anew says so on its line.
// The keys of the node and of the API are never shown: the setup changes the
// key, and /setup, /model and /effort are a line here like everything else.

type (
	// Settings is the configuration as the settings screen shows it.
	Settings = plugin.Settings
	// Setting is one change made on the settings screen.
	Setting = plugin.Setting
)

// settingsMsg is the configuration, on the way to the settings screen.
type settingsMsg struct {
	settings  Settings
	fallbacks []Fallback
	gateways  []plugin.GatewaySettings
	err       error
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
	note    string // what Info explains
	warn    bool   // the explanation is a warning
	// details are more lines below the value, like the settings of a tool:
	// each a name and what it is set to.
	details []detail
	sub     bool // it belongs to the row above it, like an entry of a list
	actions []action
}

// detail is a line below a row: a name, and what it is set to; muted is
// what holds while nothing is set.
type detail struct {
	name, value string
	muted       bool
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
	submit func(string) tea.Cmd
}

// settingsScreen is the state of the settings screen.
type settingsScreen struct {
	gateways     []plugin.GatewaySettings
	gateway      string
	gatewayDraft plugin.GatewayChange
	values       Settings        // the configuration
	fallbacks    []Fallback      // of the model, which /model changes
	err          error           // why it is not there
	rows         []row           // what the categories show of it
	category     int             // the category selected, of sections
	opened       bool            // its rows are shown, not the list of categories
	peer         string          // the ID of the peer whose own list of tools is open
	tool         string          // the tool whose settings are open
	back         []int           // the row selected on each level above the one open
	sel          int             // the row selected, of those shown
	top          int             // the line of what is shown the screen starts at
	infoTop      int             // the first line of the explanation shown
	info         *row            // the explanation opened with Info
	editing      *edit           // the text that is typed, if one is
	input        textinput.Model // where it is typed
	// refused are the settings of the tools that were refused while the
	// UI is open, by who, peer and name: a tool that is allowed again gets
	// them back, rather than going everywhere its settings kept it from.
	refused map[string]string
}

// reload asks for the configuration of the node anew.
func (c *settingsScreen) reload(m *Model) tea.Cmd {
	return func() tea.Msg { return c.load(m) }
}

// openSettings opens the settings screen.
func (m *Model) openSettings() tea.Cmd {
	m.screen = screenSettings
	c := &m.config
	c.category, c.opened, c.peer, c.tool, c.back = 0, false, "", "", nil
	c.sel, m.choice, c.top = 0, 0, 0
	c.editing, c.info = nil, nil
	c.input.SetValue("")
	c.gateway = ""
	c.gatewayDraft = plugin.GatewayChange{}
	return c.reload(m)
}

// load reads the configuration for the settings screen.
func (c *settingsScreen) load(m *Model) tea.Msg {
	s, err := m.backend.Settings()
	fallbacks, _ := m.backend.Fallbacks() // none that can be read are none
	gateways, gatewayErr := m.backend.Gateways(m.ctx)
	if err == nil {
		err = gatewayErr
	}
	return settingsMsg{settings: s, fallbacks: fallbacks, gateways: gateways, err: err}
}

// loaded takes the configuration that was read.
func (c *settingsScreen) loaded(m *Model, msg settingsMsg) {
	c.gateways = msg.gateways
	c.values, c.fallbacks, c.err, c.rows = msg.settings, msg.fallbacks, msg.err, nil
	if msg.err == nil {
		c.rows = rows(msg.settings)
	}
	// A peer or a tool that is no more closes what showed it.
	if c.peer != "" && c.peerList() == nil {
		c.peer, c.tool = "", ""
		c.back = c.back[:min(len(c.back), 1)]
	}
	if c.tool != "" && c.toolState() == nil {
		c.tool = ""
		c.back = c.back[:max(len(c.back)-1, 0)]
	}
	c.sel = min(c.sel, max(len(c.shown(m))-1, 0))
	m.choice = min(m.choice, max(len(c.selectedRow(m).actions)-1, 0))
}

// section is the category selected.
func (c *settingsScreen) section() string { return sections[c.category] }

// shown are the rows of what is open: the category selected, the list of a
// single peer in it, or the settings of a tool in either.
func (c *settingsScreen) shown(m *Model) []row {
	out := c.page(m)
	for i := range out {
		r := out[i]
		if r.note != "" {
			out[i].actions = append(slices.Clone(r.actions), action{"Info", func(m *Model) tea.Cmd {
				m.config.info, m.config.infoTop = &r, 0
				return nil
			}})
		}
	}
	return out
}

func (c *settingsScreen) page(m *Model) []row {
	switch {
	case c.gateway != "":
		return c.gatewayPage(m)
	case c.tool != "":
		if t := c.toolState(); t != nil {
			return toolPage(c.section(), c.audience(), c.peer, *t)
		}
		return nil
	case c.peer != "":
		if p := c.peerList(); p != nil {
			return toolRows(c.section(), plugin.ForPeers, p.ID, p.List)
		}
		return nil
	}
	if c.section() == gatewaysSection {
		return c.gatewayRows(m)
	}
	var out []row
	for _, r := range c.rows {
		if r.section == c.section() {
			out = append(out, r)
		}
	}
	if c.section() == nodeSection {
		out = append(out, modelRows(m, c.fallbacks)...)
	}
	return out
}

// audience is whom the list of tools open is for.
func (c *settingsScreen) audience() plugin.Audience {
	if c.section() == ownerSection {
		return plugin.ForOwner
	}
	return plugin.ForPeers
}

// peerList is the peer whose list is open, nil if none is.
func (c *settingsScreen) peerList() *plugin.PeerTools {
	at := slices.IndexFunc(c.values.Peers, func(p plugin.PeerTools) bool { return p.ID == c.peer })
	if c.peer == "" || at < 0 {
		return nil
	}
	return &c.values.Peers[at]
}

// toolState is the tool whose settings are open, nil if none is.
func (c *settingsScreen) toolState() *plugin.ToolState {
	list := &c.values.Tools[c.audience()]
	if c.peer != "" {
		p := c.peerList()
		if p == nil {
			return nil
		}
		list = &p.List
	}
	at := slices.IndexFunc(list.Tools, func(t plugin.ToolState) bool { return t.Name == c.tool })
	if c.tool == "" || at < 0 {
		return nil
	}
	return &list.Tools[at]
}

// selectedRow is the row that is selected, an empty one if there is none.
func (c *settingsScreen) selectedRow(m *Model) row {
	if shown := c.shown(m); c.sel < len(shown) {
		return shown[c.sel]
	}
	return row{}
}

// enter goes a level down, as to the settings of a tool, from the row
// selected.
func (c *settingsScreen) enter(m *Model, peer, tool string) tea.Cmd {
	c.back = append(c.back, c.sel)
	if peer != "" {
		c.peer = peer
	}
	c.tool = tool
	c.sel, m.choice, c.top = 0, 0, 0
	return nil
}

// leave goes a level up, to the row that was selected there.
func (c *settingsScreen) leave(m *Model) {
	switch {
	case c.gateway != "":
		c.gateway = ""
		c.gatewayDraft = plugin.GatewayChange{}
		c.input.SetValue("")
	case c.tool != "":
		c.tool = ""
	case c.peer != "":
		c.peer = ""
	default:
		c.opened, c.top, c.back = false, 0, nil
		return
	}
	c.sel, m.choice, c.top = 0, 0, 0
	if n := len(c.back); n > 0 {
		c.sel, c.back = c.back[n-1], c.back[:n-1]
	}
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
	c.editing = &edit{label: label, change: change}
	c.input = textinput.New()
	c.input.SetValue(value)
	c.input.CursorEnd()
	w, _ := m.settingsSize()
	c.input.SetWidth(max(10, w-8))
	return c.input.Focus()
}

// handleKey handles keys on the settings screen.
func (c *settingsScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	if c.info != nil {
		switch msg.String() {
		case "esc", "enter":
			c.info = nil
		case "up", "k":
			c.infoTop = max(0, c.infoTop-1)
		case "down", "j":
			c.infoTop++
		}
		return nil
	}
	if c.editing != nil {
		switch msg.String() {
		case "esc":
			c.clearGatewaySecrets()
			if c.gateway != "" {
				c.input.SetValue("")
			}
			c.editing = nil
			m.setNotice("nothing changed", false)
			return nil
		case "enter":
			value, editing := c.input.Value(), c.editing
			if c.gateway != "" || c.input.EchoMode == textinput.EchoPassword {
				c.input.SetValue("")
			}
			c.editing = nil
			if editing.submit != nil {
				return editing.submit(value)
			}
			return c.change(m, editing.change(value))
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
			m.choice = 0
			c.category = cycle(c.category, -1, len(sections))
		case "down", "j":
			m.choice = 0
			c.category = cycle(c.category, 1, len(sections))
		case "left", "h":
			m.choice = 0
		case "right", "l":
			m.choice = 1
		case "enter":
			if m.choice == 1 {
				c.infoTop = 0
				c.info = &row{label: c.section(), note: contents[c.section()] + ". " + about[c.section()]}
				return nil
			}
			if len(c.rows) > 0 {
				c.opened, c.sel, m.choice, c.top, c.back = true, 0, 0, 0, nil
			}
		}
		return nil
	}
	switch msg.String() {
	case "esc":
		c.leave(m)
	case "up", "k":
		c.sel, m.choice = cycle(c.sel, -1, len(c.shown(m))), 0
	case "down", "j":
		c.sel, m.choice = cycle(c.sel, 1, len(c.shown(m))), 0
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
	singleSection    = "Tools of single peers"
	protectedSection = "Protected paths"
	gatewaysSection  = "Gateways"
)

// sections are the categories in the order the settings screen lists them.
var sections = []string{nodeSection, agentsSection, pluginsSection, ownerSection, peersSection, singleSection, gatewaysSection, protectedSection}

// contents is what the list of categories says is in each.
var contents = map[string]string{
	gatewaysSection:  "external messaging access for this profile",
	nodeSection:      "the name, the description, the LLM, the model, the effort",
	agentsSection:    "the context provider, the debug mode",
	pluginsSection:   "which plugins are turned on",
	ownerSection:     "what your agent may do when you ask it",
	peersSection:     "what your agent may do when a peer asks it",
	singleSection:    "what it may do for one peer: also bash and more",
	protectedSection: "the paths no tool for files touches",
}

// about is what an open category says of its lines.
var about = map[string]string{
	gatewaysSection:  "Allowlisted users are delegated this profile’s agent tools, INCLUDING permission answers, not just chat. Empty denies all.",
	nodeSection:      "The access and the models are changed with the setup and /model, the effort with /effort.",
	agentsSection:    "A change holds from the next message, for you and for peers.",
	pluginsSection:   "Plugins are part of the build. Turning one on or off holds once ouro starts anew: quit and start ouro, /stop and /start the node.",
	ownerSection:     "What your agent may do when you ask it. A change holds from the next message.",
	peersSection:     "What your agent may do when a connected peer asks it. A change holds from the next message.",
	singleSection:    "A peer with a list of its own gets the tools of that list instead of those for peers: also trusted ones like bash, by their name.",
	protectedSection: "For you and for peers alike. A change holds from the next call of a tool.",
}

// rows are the lines of the categories for s, but those of the models,
// which come from the status of the node: see modelRows.
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
	improves, note := "proposes them", "your agent says what it would build into ouro, and builds it when you ask"
	toggle := set("build", Setting{Kind: plugin.SetImproves, On: true})
	if s.Improves {
		improves, note = "builds them, the default", "where ouro lacks what a task needs, your agent builds it when that is the better way, and tells you"
		toggle = set("propose", Setting{Kind: plugin.SetImproves})
	}
	add(agentsSection, "Changes of ouro", improves, note, toggle)

	if s.PluginsKept != "" {
		out = append(out, row{section: pluginsSection, label: "Turned off", value: "none: all run",
			note: "the plugins turned off cannot be, ouro could not start without them: " + s.PluginsKept, warn: true})
	}
	for _, p := range s.Plugins {
		out = append(out, pluginRow(p))
	}

	for who, section := range []string{ownerSection, peersSection} {
		out = append(out, toolRows(section, plugin.Audience(who), "", s.Tools[who])...)
	}
	out = append(out, peerRows(s)...)

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

// onOff says whether a mode of the node is on, and what the action that
// changes it does.
func onOff(on bool) (value, action string) {
	if on {
		return "on", "turn off"
	}
	return "off", "turn on"
}

// modelRows are the lines of the node that its status says: the model for
// peers, the fallbacks, the effort, the debug mode and the yolo mode. /model,
// /effort, /debug and /yolo change them, and the status says them as it is
// read anew.
func modelRows(m *Model, fallbacks []Fallback) []row {
	peers := "the same as yours"
	if m.status.PeerModel != "" {
		peers = m.status.PeerModel
	}
	names := make([]string, len(fallbacks))
	for i, f := range fallbacks {
		names[i] = f.Model
	}
	fallback := "none"
	if len(names) > 0 {
		fallback = strings.Join(names, " → ")
	}
	debug, turnDebug := onOff(m.status.Debug)
	yolo, turnYolo := onOff(m.status.Yolo)
	model := action{"open /model", (*Model).openModel}
	var looks []action
	for _, a := range appearances {
		looks = append(looks, action{a, func(m *Model) tea.Cmd { return m.setAppearance(a) }})
	}
	sessions, threads := "the same as yours", "the same as yours"
	if m.status.SessionModel != "" {
		sessions = m.status.SessionModel + " · " + m.status.SessionAccess
	}
	if m.status.ThreadModel != "" {
		threads = m.status.ThreadModel + " · " + m.status.ThreadAccess
	}
	return []row{
		{section: nodeSection, label: "Model for peers", value: peers, note: "what your agent answers peers with; p on /model chooses it", actions: []action{model}},
		{section: nodeSection, label: "Fallbacks", value: fallback, note: "what your agent goes on with when the model cannot answer for now; f on /model adds or removes one", actions: []action{model}},
		{section: nodeSection, label: "Effort", value: effortName(m.status.Effort), note: "how much the model thinks before it answers you", actions: []action{{"open /effort", (*Model).openEffort}}},
		{section: nodeSection, label: "Debug mode", value: debug, note: "the log of the node (ouro.log) tells much more of every call of the LLM", actions: []action{{turnDebug, (*Model).toggleDebug}}},
		{section: nodeSection, label: "Yolo mode", value: yolo, note: "no rule of tools.ask asks you before a tool call; questions to you still wait for you", actions: []action{{turnYolo, (*Model).toggleYolo}}},
		{section: nodeSection, label: "Appearance", value: m.appearanceText(), note: "how this terminal UI looks, in the colors of the GUI: light, dark, or system, as the background of the terminal is; for all profiles, also the next time; /appearance changes it too", actions: looks},
		{section: nodeSection, label: "Model for sessions", value: sessions, note: "what your agent answers your sessions with; enter opens the models, enter there sets the one chosen, and on the chosen one makes it the model of the node again", actions: []action{{"open the models", (*Model).openSessionModel}}},
		{section: nodeSection, label: "Model for the thread", value: threads, note: "what your agent answers the thread with; enter opens the models, enter there sets the one chosen, and on the chosen one makes it the model of the node again", actions: []action{{"open the models", (*Model).openThreadModel}}},
	}
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

// peerRows are the lines of the peers that may have a list of tools of their
// own, and the line that gives one to a peer by its ID.
func peerRows(s Settings) []row {
	var out []row
	for _, p := range s.Peers {
		id := p.ID
		r := row{section: singleSection, label: cmp.Or(p.Name, id), value: "the tools for peers", note: "node " + id}
		if p.Name == "" {
			r.note = "no contact: a list in the configuration"
		}
		if p.Own {
			allowed := 0
			for _, t := range p.List.Tools {
				if t.Allowed {
					allowed++
				}
			}
			r.value = "a list of its own: " + count(allowed, "tool")
			r.actions = []action{
				{"open", func(m *Model) tea.Cmd { return m.config.enter(m, id, "") }},
				{"remove the list", func(m *Model) tea.Cmd { return m.config.change(m, Setting{Kind: plugin.SetPeerList, Peer: id}) }},
			}
		} else {
			r.actions = []action{{"give it a list", func(m *Model) tea.Cmd {
				return m.config.change(m, Setting{Kind: plugin.SetPeerList, Peer: id, On: true})
			}}}
		}
		out = append(out, r)
	}
	out = append(out, row{section: singleSection, label: "Add a peer", note: "by the ID of its node, 12 hex digits as list_peers shows them; it starts with the tools for peers",
		actions: []action{{"by its ID", func(m *Model) tea.Cmd {
			return m.config.startEdit(m, "the ID of the node of a peer", "", func(v string) Setting {
				return Setting{Kind: plugin.SetPeerList, Peer: strings.TrimSpace(v), On: true}
			})
		}}}})
	return out
}

// toolRows are the lines of a list of tools: the one for who, or the own
// list of the peer if it is not "".
func toolRows(section string, who plugin.Audience, peer string, l plugin.ToolList) []row {
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
	if peer != "" {
		all.note = "every tool but those for your agent alone and the trusted ones, also those of plugins turned on later"
	}
	all.actions = []action{{"turn on", func(m *Model) tea.Cmd {
		return m.config.change(m, Setting{Kind: plugin.SetAllTools, For: who, Peer: peer, On: true})
	}}}
	if l.All {
		all.actions = []action{{"turn off", func(m *Model) tea.Cmd {
			return m.config.change(m, Setting{Kind: plugin.SetAllTools, For: who, Peer: peer})
		}}}
	}
	out := []row{all}
	for _, t := range l.Tools {
		r := row{section: section, label: t.Name, note: firstSentence(t.Description)}
		switch {
		case who == plugin.ForPeers && t.OwnerOnly:
			r.value = "for your agent alone"
		case who == plugin.ForPeers && t.Trusted:
			r.value = "for a peer with a list of its own alone"
		case t.Allowed && !t.Listed:
			r.value = "allowed, as all tools are"
		case t.Allowed:
			r.value = "allowed"
		default:
			r.value = "not allowed"
		}
		if t.Allowed {
			r.details = optionDetails(t)
		}
		if who == plugin.ForPeers && (t.OwnerOnly || t.Trusted) {
			out = append(out, r)
			continue
		}
		name, settings := t.Name, t.Settings
		key := string(rune(who)) + "/" + peer + "/" + name
		toggle := action{"allow", func(m *Model) tea.Cmd {
			if kept := m.config.refused[key]; kept != "" {
				delete(m.config.refused, key)
				return m.config.change(m, Setting{Kind: plugin.SetToolSettings, For: who, Peer: peer, Name: name, Value: kept})
			}
			return m.config.change(m, Setting{Kind: plugin.SetTool, For: who, Peer: peer, Name: name, On: true})
		}}
		if t.Allowed {
			toggle = action{"refuse", func(m *Model) tea.Cmd {
				if settings != "" {
					if m.config.refused == nil {
						m.config.refused = map[string]string{}
					}
					m.config.refused[key] = settings
				}
				return m.config.change(m, Setting{Kind: plugin.SetTool, For: who, Peer: peer, Name: name})
			}}
		}
		r.actions = []action{toggle}
		if t.Configurable && t.Allowed {
			r.actions = append(r.actions, action{"settings", func(m *Model) tea.Cmd { return m.config.enter(m, "", name) }})
		}
		out = append(out, r)
	}
	return out
}

// optionDetails are the lines that show the settings of t, each by its name.
func optionDetails(t plugin.ToolState) []detail {
	var out []detail
	for _, o := range t.Options {
		out = append(out, detail{o.Name, optionValue(o), !o.Set})
	}
	if len(t.Options) == 0 && t.Settings != "" {
		out = append(out, detail{"settings", t.Settings, false})
	}
	return out
}

// optionValue is what a setting of a tool is set to, as it is shown: what
// holds while it is not set says so.
func optionValue(o plugin.ToolOption) string {
	if !o.Set {
		return cmp.Or(o.Unset, "not set") + " (default)"
	}
	switch o.Kind {
	case plugin.OptionList:
		if len(o.List) == 0 {
			return "none"
		}
		return strings.Join(o.List, ", ")
	case plugin.OptionSwitch:
		if o.Value == "true" {
			return "yes"
		}
		return "no"
	}
	return o.Value
}

// toolPage are the lines of the settings of the tool t in a list of tools:
// a line for every setting, below a list one for every entry, and one for
// all of them as JSON.
func toolPage(section string, who plugin.Audience, peer string, t plugin.ToolState) []row {
	name := t.Name
	// with saves the settings of t with key set to value, or without it
	// for nil.
	with := func(m *Model, key string, value any) tea.Cmd {
		return m.config.change(m, setting(t, who, peer, key, value))
	}
	var out []row
	for _, o := range t.Options {
		key := o.Name
		r := row{section: section, label: key, value: optionValue(o), note: o.About}
		reset := action{"reset", func(m *Model) tea.Cmd { return with(m, key, nil) }}
		switch o.Kind {
		case plugin.OptionSwitch:
			on := o.Value == "true"
			name := "turn on"
			if on {
				name = "turn off"
			}
			r.actions = []action{{name, func(m *Model) tea.Cmd { return with(m, key, !on) }}}
		case plugin.OptionList:
			list := slices.Clone(o.List)
			r.actions = []action{{"add", func(m *Model) tea.Cmd {
				return m.config.startEdit(m, "an entry of "+key+" of "+name, "", func(v string) Setting {
					return setting(t, who, peer, key, append(slices.Clone(list), strings.TrimSpace(v)))
				})
			}}}
			if !o.Set {
				r.actions = append(r.actions, action{"set to none", func(m *Model) tea.Cmd { return with(m, key, []string{}) }})
			}
		default:
			value := o.Value
			r.actions = []action{{"change", func(m *Model) tea.Cmd {
				return m.config.startEdit(m, key+" of "+name, value, func(v string) Setting {
					var typed any = strings.TrimSpace(v)
					if n, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && o.Kind == plugin.OptionNumber {
						typed = n // what is no number goes as typed: the node says why it is wrong
					}
					return setting(t, who, peer, key, typed)
				})
			}}}
		}
		if o.Set {
			r.actions = append(r.actions, reset)
		}
		out = append(out, r)
		if o.Kind == plugin.OptionList {
			for i, entry := range o.List {
				rest := slices.Delete(slices.Clone(o.List), i, i+1)
				out = append(out, row{section: section, label: entry, sub: true,
					actions: []action{{"remove", func(m *Model) tea.Cmd { return with(m, key, rest) }}}})
			}
		}
	}
	settings := cmp.Or(t.Settings, "{}")
	out = append(out, row{section: section, label: "As JSON", value: settings, note: "all its settings at once, as the configuration keeps them",
		actions: []action{{"change", func(m *Model) tea.Cmd {
			return m.config.startEdit(m, "the settings of "+name, settings, func(v string) Setting {
				return Setting{Kind: plugin.SetToolSettings, For: who, Peer: peer, Name: name, Value: v}
			})
		}}}})
	return out
}

// setting is the change of the settings of the tool t to key set to value,
// or to key not set for nil.
func setting(t plugin.ToolState, who plugin.Audience, peer, key string, value any) Setting {
	settings := map[string]any{}
	_ = json.Unmarshal([]byte(t.Settings), &settings) // "" is none
	if value == nil {
		delete(settings, key)
	} else {
		settings[key] = value
	}
	v := ""
	if len(settings) > 0 {
		b, _ := json.Marshal(settings) // of JSON and texts: cannot fail
		v = string(b)
	}
	return Setting{Kind: plugin.SetToolSettings, For: who, Peer: peer, Name: t.Name, Value: v}
}

// count is n things, like "1 tool" or "3 tools".
func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return strconv.Itoa(n) + " " + thing + "s"
}

// firstSentence is the first sentence of text.
func firstSentence(text string) string {
	if i := strings.Index(text, ". "); i >= 0 {
		return text[:i+1]
	}
	return text
}

// settingsWidth is how wide the settings dialog is at most.
const settingsWidth = 100

// settingsSize is how wide and how high the settings dialog may be, with its
// border and padding.
func (m *Model) settingsSize() (width, height int) {
	return max(1, min(settingsWidth, m.width-4)), max(1, m.bodyHeight()-2)
}

// title is the path of what is open, without explanatory text.
func (c *settingsScreen) title() []string {
	path := []string{"Settings"}
	if c.opened {
		path = append(path, c.section())
	}
	if p := c.peerList(); p != nil {
		path = append(path, cmp.Or(p.Name, p.ID))
	}
	if c.tool != "" {
		path = append(path, c.tool)
	}
	if c.gateway != "" {
		for _, g := range c.gateways {
			if g.Name == c.gateway {
				path = append(path, cmp.Or(g.Title, g.Name))
			}
		}
	}
	for i, p := range path {
		path[i] = boldStyle.Render(p)
	}
	return []string{strings.Join(path, mutedStyle.Render(" › ")), ""}
}

// view shows, in a dialog over the chat, the list of categories or the
// lines of what is open around the one selected, with what can be done with
// it, and the text that is typed. It moves the window and keeps it (top),
// which the key handler cannot: only here is known how many lines a row
// takes.
func (c *settingsScreen) view(m *Model) (string, []hit) {
	w, h := m.settingsSize()
	inner := max(10, w-4)
	head := c.title()
	var lines []string
	// A click on a line selects what it is of, and on what is selected
	// does the action chosen; one on an action chooses it and does it.
	var hits []hit
	enter := func() tea.Cmd { return c.handleKey(m, enterKey) }
	act := func(names []string) {
		hits = append(hits, choiceHits(len(lines), 4, names, func(j int) tea.Cmd {
			m.choice = j
			return enter()
		})...)
	}
	at, height := 0, 1 // where what is selected is, and how many lines it takes
	selected := lipgloss.NewStyle().Foreground(accent)
	if c.info != nil {
		style := mutedStyle
		if c.info.warn {
			style = lipgloss.NewStyle().Foreground(warn)
		}
		text := wrapped(c.info.note, "", inner, style)
		room := max(1, h-6)
		c.infoTop = min(c.infoTop, max(0, len(text)-room))
		lines = []string{boldStyle.Render(ansi.Truncate(c.info.label+" · Info", inner, "…")), ""}
		lines = append(lines, text[c.infoTop:min(len(text), c.infoTop+room)]...)
		body := lipgloss.NewStyle().Width(inner).MaxHeight(max(1, h-2)).Render(strings.Join(lines, "\n"))
		return box(true, m.light).Width(w).Render(body), nil
	}
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
				marker, name = selected.Render("› "), selected.Bold(true).Width(nameWidth+2).Render(s)
			}
			hits = append(hits, onLine(len(lines), choosing(i == c.category, func() { c.category, m.choice = i, 0 }, func() tea.Cmd {
				m.choice = 0 // the name opens it
				return enter()
			})))
			lines = append(lines, ansi.Truncate(marker+name, inner, "…"))
			doing := ""
			if i == c.category {
				act([]string{"open", "Info"})
				doing = choices([]string{"open", "Info"}, m.choice)
				height = 2
			}
			lines = append(lines, "    "+doing)
		}
	default:
		shown := c.shown(m)
		labelWidth := 0
		for _, r := range shown {
			if !r.sub {
				labelWidth = max(labelWidth, ansi.StringWidth(r.label))
			}
		}
		labelWidth = min(labelWidth, 28)
		for i, r := range shown {
			if i == c.sel {
				at = len(lines)
			}
			indent, width := "", labelWidth
			if r.sub {
				indent, width = "  · ", max(labelWidth-4, 1)
			}
			style := lipgloss.NewStyle()
			marker := "  "
			if i == c.sel {
				marker, style = selected.Render("› "), selected.Bold(true)
			}
			label := style.Width(width + 2).Render(ansi.Truncate(r.label, width, "…"))
			if r.sub {
				label = style.Render(ansi.Truncate(r.label, max(inner-6, 1), "…"))
			}
			from := len(lines)
			lines = append(lines, ansi.Truncate(marker+mutedStyle.Render(indent)+label+r.value, inner, "…"))
			lines = append(lines, details(r.details, inner)...)
			for l := from; l < len(lines); l++ {
				hits = append(hits, onLine(l, choosing(i == c.sel, func() { c.sel, m.choice = i, 0 }, enter)))
			}
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
					act(names)
					doing = choices(names, m.choice)
				}
				height = len(lines) + 1 - at
			}
			lines = append(lines, "    "+doing)
		}
	}
	var bottom []string
	if c.editing != nil {
		bottom = []string{"", boldStyle.Render(c.editing.label) + mutedStyle.Render("  enter saves · esc keeps what was"), c.input.View()}
	}
	room := max(1, h-2-len(head)-len(bottom)) // the border
	// What does not fit says so below it, on a line of its own, and above
	// it on the empty line below the head.
	scrolls := len(lines) > room && room > 1
	if scrolls {
		room--
	}
	// The window follows what is selected by the line and moves only when
	// it would leave it, so that what is read stands still while selecting.
	top := c.top
	if top > at || top+room < at+height {
		top = at
	}
	top = max(0, min(top, max(0, len(lines)-room)))
	c.top = top
	below := len(lines) - top - room
	hits = shifted(hits, top, len(head), room)
	lines = lines[top:min(len(lines), top+room)]
	if scrolls {
		more := ""
		if below > 0 {
			more = mutedStyle.Render("  ↓ more")
		}
		lines = append(lines, more)
		if top > 0 {
			head[len(head)-1] = mutedStyle.Render("  ↑ more")
		}
	}
	all := append(append(head, lines...), bottom...)
	for i, l := range all {
		all[i] = ansi.Truncate(l, inner, "…")
	}
	body := lipgloss.NewStyle().Width(inner).MaxHeight(max(1, h-2)).Render(strings.Join(all, "\n"))
	if c.editing != nil {
		hits = nil // what is typed is kept or not by its keys
	}
	return box(true, m.light).Width(w).Render(body), hits
}

// details are the lines that show what the settings of a row are set to,
// below its note: the names in a column, what holds unset muted.
func details(ds []detail, width int) []string {
	nameWidth := 0
	for _, d := range ds {
		nameWidth = max(nameWidth, ansi.StringWidth(d.name))
	}
	var out []string
	for _, d := range ds {
		value := d.value
		if d.muted {
			value = mutedStyle.Render(value)
		}
		out = append(out, ansi.Truncate("    "+mutedStyle.Render("• ")+lipgloss.NewStyle().Width(nameWidth+2).Render(d.name)+value, width, "…"))
	}
	return out
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
