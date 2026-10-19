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

// modelsMsg is what the accesses of the node answered with, on the way to
// the model screen.
type modelsMsg struct {
	models    []Models
	fallbacks []Fallback
	err       error
}

// modelChangedMsg is what the model screen says once the node answers with
// another model: the conversation that was shown is not continued there.
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
}

// choice is one model to select: a model of an access.
type choice struct {
	access int // of Model.models
	model  string
}

// switchedNote is what the owner is told once when the agents answer through
// another access than before.
const switchedNote = "the prompt cache starts anew there: the first requests may cost more or take longer"

// openModel opens the model screen and lists the models of the accesses. A
// node that is not set up has no access to ask.
func (m *Model) openModel() tea.Cmd {
	if !m.status.Configured {
		m.setNotice("set up the node first: /setup", true)
		return nil
	}
	m.screen = screenModel
	m.picker.lists, m.picker.choices, m.picker.err, m.picker.busy = nil, nil, nil, "listing models"
	m.picker.sel = 0
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
			if l.Access.Active && model == m.status.Model {
				s.sel = len(s.choices)
			}
			s.choices = append(s.choices, choice{i, model})
		}
	}
}

// handleModelKey handles keys on the model screen: up and down select a
// model, enter makes the one selected the model of the node, esc goes back
// to the chat.
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
				// The model that answered before has none of the
				// conversation cached: it goes on in a new one.
				return modelChangedMsg{done + " · new conversation"}
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

// forPeers reports whether model of access is what the owner chose to
// answer peers with.
func (s *modelScreen) forPeers(m *Model, access plugin.SetupAccess, model string) bool {
	return access.ID == m.status.PeerAccess && model == m.status.PeerModel
}

// modelView shows, in a dialog, the models of the accesses, the one in use
// marked and the one selected ready to be set with enter.
func (s *modelScreen) view(m *Model) string {
	w, h := m.dialogSize()
	lines := []string{boldStyle.Render("Model") + mutedStyle.Render("  "+m.modelText()), ""}
	switch {
	case s.busy != "":
		lines = append(lines, m.spinner.View()+mutedStyle.Render(" "+s.busy+"…"))
	case s.err != nil:
		lines = append(lines, errStyle.Render("✗ "+s.err.Error()))
	default:
		lines = append(lines, s.list(m, w, max(1, h-6))...) // border, title and count
	}
	return m.dialog(lines)
}

// modelList shows the models around the one selected, in at most height
// lines, each below the access it belongs to: what the access is called and
// what tells it apart, and why it did not list its models, if it did not.
func (s *modelScreen) list(m *Model, width, height int) []string {
	type line struct {
		text string
		head string // of a model: the line of its access, "" for another line
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
			all = append(all, line{})
		}
		all = append(all, line{text: head})
		if l.Err != "" {
			why := "✗ " + l.Err
			if len(l.Models) > 0 {
				why += " – the models it listed the last time:"
			}
			all = append(all, line{text: errStyle.Render(ansi.Truncate(why, max(0, width-6), "…"))})
		}
		for _, model := range l.Models {
			name := ansi.Truncate(model, max(0, width-14), "…")
			if l.Access.Active && model == m.status.Model {
				name += mutedStyle.Render("  in use")
			}
			if s.forPeers(m, l.Access, model) {
				name += mutedStyle.Render("  for peers")
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
			all = append(all, line{name, head})
			n++
		}
	}
	first := max(0, min(at-height/2, len(all)-height))
	shown := all[first:min(len(all), first+height)]
	lines := make([]string, len(shown))
	for i, l := range shown {
		lines[i] = l.text
	}
	// The access of the models at the top stays in sight.
	if len(shown) > 1 && shown[0].head != "" {
		lines[0] = shown[0].head
	}
	return append(lines, "", mutedStyle.Render(fmt.Sprintf("%d of %d", min(s.sel+1, len(s.choices)), len(s.choices))))
}
