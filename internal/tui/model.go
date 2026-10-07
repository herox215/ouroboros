package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The model screen is what the command /model opens: the models of every
// access of the node, grouped by access, with the one it answers with marked.
// The arrow keys select one, enter makes it the model of the node – the
// agents answer with it from the next message they get, without a restart,
// also when it is one of another access – and esc goes back to the chat.

// modelsMsg is what the accesses of the node answered with, on the way to
// the model screen.
type modelsMsg struct {
	models []Models
	err    error
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
	m.models, m.choices, m.modelErr, m.modelBusy = nil, nil, nil, "listing models"
	m.modelSel = 0
	return m.loadModels
}

// loadModels asks the accesses of the node for their models.
func (m *Model) loadModels() tea.Msg {
	models, err := m.backend.Models(m.ctx)
	return modelsMsg{models, err}
}

// modelsLoaded takes the answer of the accesses: the lists, or why not.
func (m *Model) modelsLoaded(models []Models, err error) {
	m.modelBusy = ""
	m.models, m.choices, m.modelErr, m.modelSel = nil, nil, err, 0
	if err != nil {
		return
	}
	m.models = models
	for i, l := range models {
		for _, model := range l.Models {
			if l.Access.Active && model == m.status.Model {
				m.modelSel = len(m.choices)
			}
			m.choices = append(m.choices, choice{i, model})
		}
	}
}

// handleModelKey handles keys on the model screen: up and down select a
// model, enter makes the one selected the model of the node, esc goes back
// to the chat.
func (m *Model) handleModelKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.screen = screenChat
	case "up", "k":
		m.modelSel = max(0, m.modelSel-1)
	case "down", "j":
		m.modelSel = min(max(len(m.choices)-1, 0), m.modelSel+1)
	case "enter":
		if m.modelBusy != "" || m.modelErr != nil || m.modelSel >= len(m.choices) {
			return nil
		}
		picked := m.choices[m.modelSel]
		access := m.models[picked.access].Access
		return func() tea.Msg {
			switched, err := m.backend.SetModel(access.ID, picked.model)
			done := "model changed to " + picked.model
			if switched {
				done += " of " + access.Label + " · " + switchedNote
			}
			return actionMsg{done, err}
		}
	}
	return nil
}

// modelView shows the models of the accesses, the one in use marked and the
// one selected ready to be set with enter.
func (m *Model) modelView() string {
	w, h := m.width, m.bodyHeight()
	lines := []string{boldStyle.Render("Model") + mutedStyle.Render("  "+m.modelText()), ""}
	switch {
	case m.modelBusy != "":
		lines = append(lines, m.spinner.View()+mutedStyle.Render(" "+m.modelBusy+"…"))
	case m.modelErr != nil:
		lines = append(lines, errStyle.Render("✗ "+m.modelErr.Error()))
	default:
		lines = append(lines, m.modelList(w, max(1, h-5))...)
	}
	return box(true, m.light).Width(w).Height(h).MaxHeight(h).Render(strings.Join(lines, "\n"))
}

// modelList shows the models around the one selected, in at most height
// lines, each below the access it belongs to: what the access is called and
// what tells it apart, and why it did not list its models, if it did not.
func (m *Model) modelList(width, height int) []string {
	type line struct {
		text string
		head string // of a model: the line of its access, "" for another line
	}
	var all []line
	at, n := 0, 0
	for i, l := range m.models {
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
			if n == m.modelSel {
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
	return append(lines, "", mutedStyle.Render(fmt.Sprintf("%d of %d", min(m.modelSel+1, len(m.choices)), len(m.choices))))
}
