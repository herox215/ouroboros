package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The model screen is what the command /model opens: the models the provider
// of the node offers, with the one it answers with marked. The arrow keys
// select one, enter saves it as the model of the node – the agents answer
// with it from the next message they get, without a restart – and esc goes
// back to the chat.

// modelsMsg is what the provider of the node answered with, on the way to
// the model screen.
type modelsMsg struct {
	models Models
	err    error
}

// openModel opens the model screen and lists the models of the provider. A
// node that is not set up has no provider to ask.
func (m *Model) openModel() tea.Cmd {
	if !m.status.Configured {
		m.setNotice("set up the node first: /setup", true)
		return nil
	}
	m.screen = screenModel
	m.models, m.modelErr, m.modelBusy = Models{}, nil, "listing models"
	m.modelSel = 0
	return m.loadModels
}

// loadModels asks the provider of the node for its models.
func (m *Model) loadModels() tea.Msg {
	models, err := m.backend.Models(m.ctx)
	return modelsMsg{models, err}
}

// modelsLoaded takes the answer of the provider: the list, or why not.
func (m *Model) modelsLoaded(models Models, err error) {
	m.modelBusy = ""
	if err != nil {
		m.models, m.modelErr = Models{}, err
		return
	}
	m.modelErr = nil
	m.models = models
	m.modelSel = max(0, slices.Index(models.Chat, models.Default))
}

// handleModelKey handles keys on the model screen: up and down select a
// model, enter saves the one selected, esc goes back to the chat.
func (m *Model) handleModelKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.screen = screenChat
	case "up", "k":
		m.modelSel = max(0, m.modelSel-1)
	case "down", "j":
		m.modelSel = min(max(len(m.models.Chat)-1, 0), m.modelSel+1)
	case "enter":
		if m.modelBusy != "" || m.modelErr != nil || m.modelSel >= len(m.models.Chat) {
			return nil
		}
		model := m.models.Chat[m.modelSel]
		return func() tea.Msg { return actionMsg{"model changed to " + model, m.backend.SetModel(model)} }
	}
	return nil
}

// modelView shows the models of the provider, the one in use marked and the
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
		lines = append(lines, m.modelList(w)...)
	}
	return box(true, m.light).Width(w).Height(h).MaxHeight(h).Render(strings.Join(lines, "\n"))
}

// modelList shows a few of the models around the one selected.
func (m *Model) modelList(width int) []string {
	const shown = 8
	all := m.models.Chat
	first := max(0, min(m.modelSel-shown/2, len(all)-shown))
	var lines []string
	for i := first; i < min(len(all), first+shown); i++ {
		name := ansi.Truncate(all[i], max(0, width-14), "…")
		if all[i] == m.status.Model {
			name += mutedStyle.Render("  in use")
		}
		if i == m.modelSel {
			lines = append(lines, lipgloss.NewStyle().Foreground(accent).Render("▸ "+name))
		} else {
			lines = append(lines, "  "+name)
		}
	}
	return append(lines, "", mutedStyle.Render(fmt.Sprintf("%d of %d", m.modelSel+1, len(all))))
}
