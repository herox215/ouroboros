package tui

import (
	"fmt"
	"slices"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The model screen is what the command /model opens: the models of every
// access of the node, grouped by access, with the one it answers with marked.
// The arrow keys select one, enter makes it the model of the node – the
// agents answer with it from the next message they get, without a restart,
// also when it is one of another access – and esc goes back to the chat.
// p makes the one selected the model peers are answered with, whatever the
// owner is, and p on that one makes it the model of the node for them again.
// f makes it the last of the fallbacks of the node, or none of them any more.

// scope is what the model screen sets with enter: the model of the node, or
// that of one of its scopes alone.
type scope int

const (
	scopeNode scope = iota // the model of the node, which everything follows
	scopeSessions
	scopeThreads
)

// name is what the scope is called where the owner sees it.
func (s scope) name() string {
	switch s {
	case scopeSessions:
		return "sessions"
	case scopeThreads:
		return "the thread"
	}
	return "the node"
}

// modelsMsg is what the accesses of the node answered with, on the way to
// the model screen.
type modelsMsg struct {
	models    []Models
	fallbacks []Fallback
	err       error
}

// modelChangedMsg is what the model screen says once the node answers with
// another model: the conversation that was shown goes on, which the owner is
// told of, since that model has none of it in its context.
type modelChangedMsg struct{ done string }

// fallbacksMsg are the fallbacks of the node after f changed them, with
// what the owner is told of it.
type fallbacksMsg struct {
	list []Fallback
	done string
	err  error
}

// modelScreen is the state of the model screen.
type modelScreen struct {
	lists     []Models   // what the accesses offer, once /model asked for them
	choices   []choice   // the models of them all, to select from
	sel       int        // the one selected of choices
	busy      string     // what the screen waits for, like "listing models"
	err       error      // why the models are not there
	fallbacks []Fallback // what the node falls back on, as the list marks it
	scope     scope      // what enter sets: the model of the node, or of a scope
}

// choice is one model to select: a model of an access.
type choice struct {
	access int // of Model.models
	model  string
}

// switchedNote is what the owner is told once when the agents answer through
// another access than before.
const switchedNote = "the prompt cache starts anew there: the first requests may cost more or take longer"

// changedNote is what the owner is told once another model answers in the
// conversation that was shown: it goes on, but the model starts anew in it.
const changedNote = "the conversation goes on, the model starts without what was said before"

// openModel opens the model screen and lists the models of the accesses. A
// node that is not set up has no access to ask.
func (m *Model) openModel() tea.Cmd { return m.openModelScope(scopeNode) }

// openSessionModel opens the model screen for the sessions, and
// openThreadModel opens it for the thread: enter sets the model of that
// scope alone.
func (m *Model) openSessionModel() tea.Cmd { return m.openModelScope(scopeSessions) }
func (m *Model) openThreadModel() tea.Cmd  { return m.openModelScope(scopeThreads) }

// openModelScope opens the model screen for what enter is to set.
func (m *Model) openModelScope(s scope) tea.Cmd {
	if !m.status.Configured {
		m.setNotice("set up the node first: /setup", true)
		return nil
	}
	m.screen = screenModel
	m.picker.lists, m.picker.choices, m.picker.err, m.picker.busy = nil, nil, nil, "listing models"
	m.picker.scope, m.picker.sel = s, 0
	return func() tea.Msg { return m.picker.load(m) }
}

// loadModels asks the accesses of the node for their models.
func (s *modelScreen) load(m *Model) tea.Msg {
	models, err := m.backend.Models(m.ctx)
	if err != nil {
		return modelsMsg{err: err}
	}
	fallbacks, err := m.backend.Fallbacks()
	return modelsMsg{models, fallbacks, err}
}

// modelsLoaded takes the answer of the accesses: the lists, or why not.
func (s *modelScreen) loaded(m *Model, msg modelsMsg) {
	s.busy = ""
	s.lists, s.choices, s.err, s.sel = nil, nil, msg.err, 0
	if msg.err != nil {
		return
	}
	s.lists, s.fallbacks = msg.models, msg.fallbacks
	for i, l := range msg.models {
		for _, model := range l.Models {
			// The screen preselects what enter would repeat: the choice of
			// its scope, or the model of the node on the node's screen.
			if s.chosen(m, l.Access, model) || (s.scope == scopeNode && l.Access.Active && model == m.status.Model) {
				s.sel = len(s.choices)
			}
			s.choices = append(s.choices, choice{i, model})
		}
	}
}

// handleKey handles keys on the model screen: up and down select a model,
// enter makes the one selected the model of the screen's scope – that of the
// node, of the sessions or of the thread – and esc goes back to the chat. On
// the node's screen p makes it the model of the peers and f one of the
// fallbacks; a scope's screen has neither.
func (s *modelScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.screen = screenChat
	case "up", "k":
		s.sel = cycle(s.sel, -1, len(s.choices))
	case "down", "j":
		s.sel = cycle(s.sel, 1, len(s.choices))
	case "enter", "p", "f":
		if s.busy != "" || s.err != nil || s.sel >= len(s.choices) {
			return nil
		}
		picked := s.choices[s.sel]
		access := s.lists[picked.access].Access
		if s.scope != scopeNode {
			// Peers and fallbacks are those of the node, not of a scope.
			if msg.String() != "enter" {
				return nil
			}
			return s.pickForScope(m, access, picked.model)
		}
		if msg.String() == "p" {
			return s.pickForPeers(m, access, picked.model)
		}
		if msg.String() == "f" {
			return s.toggleFallback(m, access, picked.model)
		}
		changed := picked.model != m.status.Model
		return func() tea.Msg {
			switched, err := m.backend.SetModel(access.ID, picked.model)
			done := "model changed to " + picked.model
			if switched {
				done += " of " + access.Label + " · " + switchedNote
			}
			if err != nil {
				return actionMsg{done, err}
			}
			if changed {
				// The conversation goes on, but in the context of
				// another model: the node tells it so with the next
				// message, see the session's modelFor.
				return modelChangedMsg{done + " · " + changedNote}
			}
			return actionMsg{done, nil}
		}
	}
	return nil
}

// pickForPeers makes model of access what peers are answered with, or, if
// it is that already, lets them be answered with the model of the node
// again.
func (s *modelScreen) pickForPeers(m *Model, access plugin.SetupAccess, model string) tea.Cmd {
	if s.forPeers(m, access, model) {
		return func() tea.Msg {
			return actionMsg{"peers are answered with the model of the node again", m.backend.SetPeerModel("", "")}
		}
	}
	return func() tea.Msg {
		return actionMsg{"peers are answered with " + model + " of " + access.Label, m.backend.SetPeerModel(access.ID, model)}
	}
}

// pickForScope makes model of access what the scope is answered with, or, if
// it is that already, lets the scope be answered with the model of the node
// again.
func (s *modelScreen) pickForScope(m *Model, access plugin.SetupAccess, model string) tea.Cmd {
	if s.chosen(m, access, model) {
		again := s.scope.name() + " are answered with the model of the node again"
		set := m.backend.SetSessionModel
		if s.scope == scopeThreads {
			again = "the thread is answered with the model of the node again"
			set = m.backend.SetThreadModel
		}
		return func() tea.Msg { return actionMsg{again, set("", "")} }
	}
	done := s.scope.name() + " are answered with " + model + " of " + access.Label
	if s.scope == scopeThreads {
		done = "the thread is answered with " + model + " of " + access.Label
		return func() tea.Msg { return actionMsg{done, m.backend.SetThreadModel(access.ID, model)} }
	}
	return func() tea.Msg { return actionMsg{done, m.backend.SetSessionModel(access.ID, model)} }
}

// chosen reports whether model of access is what the owner chose for the
// scope the screen sets with enter.
func (s *modelScreen) chosen(m *Model, access plugin.SetupAccess, model string) bool {
	switch s.scope {
	case scopeSessions:
		return m.status.SessionAccess == access.ID && m.status.SessionModel == model
	case scopeThreads:
		return m.status.ThreadAccess == access.ID && m.status.ThreadModel == model
	}
	return false
}

// toggleFallback makes model of access the last of the fallbacks of the
// node, or none of them any more if it is one.
func (s *modelScreen) toggleFallback(m *Model, access plugin.SetupAccess, model string) tea.Cmd {
	return func() tea.Msg {
		list, err := m.backend.ToggleFallback(access.ID, model)
		done := model + " of " + access.Label + " is no fallback any more"
		if at := slices.Index(list, Fallback{Access: access.ID, Model: model}); at >= 0 {
			done = fmt.Sprintf("%s of %s is fallback %d: it answers you when what is before it cannot for now", model, access.Label, at+1)
		}
		return fallbacksMsg{list, done, err}
	}
}

// fallbacksSet takes the fallbacks as they are after f, or why they are as
// before.
func (s *modelScreen) fallbacksSet(m *Model, msg fallbacksMsg) {
	if msg.err != nil {
		m.setNotice(msg.err.Error(), true)
		return
	}
	s.fallbacks = msg.list
	m.setNotice(msg.done, false)
}

// text is what the screen says beside its title: the model of the node, or
// the one the scope is answered with, "the same as yours" if the scope has
// none of its own.
func (s *modelScreen) text(m *Model) string {
	if s.scope == scopeNode {
		return m.modelText()
	}
	access, model := m.status.SessionAccess, m.status.SessionModel
	if s.scope == scopeThreads {
		access, model = m.status.ThreadAccess, m.status.ThreadModel
	}
	if model == "" {
		return "the same as yours: " + m.modelText()
	}
	for _, l := range s.lists {
		if l.Access.ID == access {
			return model + " · " + l.Access.Label
		}
	}
	return model
}

// forPeers reports whether model of access is what the owner chose to
// answer peers with.
func (s *modelScreen) forPeers(m *Model, access plugin.SetupAccess, model string) bool {
	return access.ID == m.status.PeerAccess && model == m.status.PeerModel
}

// modelView shows, in a dialog, the models of the accesses, the one in use
// marked and the one selected ready to be set with enter.
func (s *modelScreen) view(m *Model) (string, []hit) {
	w, h := m.dialogSize()
	title := "Model"
	switch s.scope {
	case scopeSessions:
		title = "Model for sessions"
	case scopeThreads:
		title = "Model for the thread"
	}
	lines := []string{boldStyle.Render(title) + mutedStyle.Render("  "+s.text(m)), ""}
	var hits []hit
	switch {
	case s.busy != "":
		lines = append(lines, m.spinner.View()+mutedStyle.Render(" "+s.busy+"…"))
	case s.err != nil:
		lines = append(lines, errStyle.Render("✗ "+s.err.Error()))
	default:
		list, choices := s.list(m, w, max(1, h-6)) // border, title and count
		// A click on a model selects it, and on the one selected sets it.
		for i, n := range choices {
			if n >= 0 {
				hits = append(hits, onLine(len(lines)+i, choosing(n == s.sel, func() { s.sel = n }, func() tea.Cmd { return s.handleKey(m, enterKey) })))
			}
		}
		lines = append(lines, list...)
	}
	return m.dialog(lines), hits
}

// modelList shows the models around the one selected, in at most height
// lines, each below the access it belongs to: what the access is called and
// what tells it apart, and why it did not list its models, if it did not;
// and of each line the model of choices it shows, -1 for none.
func (s *modelScreen) list(m *Model, width, height int) ([]string, []int) {
	type line struct {
		text   string
		head   string // of a model: the line of its access, "" for another line
		choice int
	}
	var all []line
	at, n := 0, 0
	for i, l := range s.lists {
		head := boldStyle.Render(l.Access.Label)
		if l.Access.About != "" {
			head += mutedStyle.Render(" · " + l.Access.About)
		}
		head = ansi.Truncate(head, max(0, width-4), "…")
		if i > 0 {
			all = append(all, line{choice: -1})
		}
		all = append(all, line{text: head, choice: -1})
		if l.Err != "" {
			why := "✗ " + l.Err
			if len(l.Models) > 0 {
				why += " – the models it listed the last time:"
			}
			all = append(all, line{text: errStyle.Render(ansi.Truncate(why, max(0, width-6), "…")), choice: -1})
		}
		for _, model := range l.Models {
			name := ansi.Truncate(model, max(0, width-14), "…")
			if l.Access.Active && model == m.status.Model {
				name += mutedStyle.Render("  in use")
			}
			if s.forPeers(m, l.Access, model) {
				name += mutedStyle.Render("  for peers")
			}
			if l.Access.ID == m.status.SessionAccess && model == m.status.SessionModel {
				name += mutedStyle.Render("  for sessions")
			}
			if l.Access.ID == m.status.ThreadAccess && model == m.status.ThreadModel {
				name += mutedStyle.Render("  for the thread")
			}
			if at := slices.Index(s.fallbacks, Fallback{Access: l.Access.ID, Model: model}); at >= 0 {
				name += mutedStyle.Render(fmt.Sprintf("  fallback %d", at+1))
			}
			if n == s.sel {
				at = len(all)
				name = lipgloss.NewStyle().Foreground(accent).Render("▸ " + name)
			} else {
				name = "  " + name
			}
			all = append(all, line{name, head, n})
			n++
		}
	}
	first := max(0, min(at-height/2, len(all)-height))
	shown := all[first:min(len(all), first+height)]
	lines := make([]string, len(shown))
	choices := make([]int, len(shown))
	for i, l := range shown {
		lines[i], choices[i] = l.text, l.choice
	}
	// The access of the models at the top stays in sight.
	if len(shown) > 1 && shown[0].head != "" {
		lines[0], choices[0] = shown[0].head, -1
	}
	return append(lines, "", mutedStyle.Render(fmt.Sprintf("%d of %d", min(s.sel+1, len(s.choices)), len(s.choices)))), choices
}
