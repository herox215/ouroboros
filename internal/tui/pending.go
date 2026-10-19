package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// acceptedAllMsg reports each decision independently: a failed item must not
// prevent the remaining items from being accepted or hide work already taken.
type acceptedAllMsg struct {
	screen      *requestsScreen
	suggestions []plugin.Suggestion
	err         error
}

func (r *requestsScreen) acceptAll(m *Model) tea.Cmd {
	if r.acceptingAll || r.trust.waiting() == 0 {
		return nil
	}
	r.acceptingAll = true
	// Snapshot what the owner saw. Later arrivals need a decision of their own.
	trust := r.trust
	return func() tea.Msg {
		result := acceptedAllMsg{screen: r}
		var problems []error
		for _, suggestion := range trust.Suggestions {
			taken, err := m.backend.TakeSuggestion(suggestion.ID)
			if err != nil {
				problems = append(problems, fmt.Errorf("suggestion %s: %w", suggestion.ID, err))
			} else {
				result.suggestions = append(result.suggestions, taken)
			}
		}
		result.err = errors.Join(problems...)
		return result
	}
}

func (m *Model) acceptedAll(msg acceptedAllMsg) tea.Cmd {
	if msg.screen != nil {
		msg.screen.acceptingAll = false
	}
	var cmd tea.Cmd
	if len(msg.suggestions) > 0 {
		var text strings.Builder
		text.WriteString("Work through all of the following accepted suggestions together in this single conversation. Check each against the source, implement those that hold up with their tests, and report the outcome of each.\n")
		for i, s := range msg.suggestions {
			m.removeSuggestion(s.ID)
			fmt.Fprintf(&text, "\n%d. %s\n", i+1, s.Text)
			if len(s.Signals) > 0 {
				fmt.Fprintf(&text, "What was measured: %s\n", strings.Join(s.Signals, "; "))
			}
		}
		cmd = m.taken(takenMsg{suggestion: plugin.Suggestion{Text: text.String()}})
	} else {
		m.setNotice("all suggestions processed", false)
		cmd = m.refresh()
	}
	if msg.err != nil {
		m.setNotice(msg.err.Error(), true)
	}
	return cmd
}

// removeSuggestion updates the dialog immediately after the backend took an item.
func (m *Model) removeSuggestion(id string) {
	if id == "" {
		return
	}
	trust := m.suggestionRequests.trust
	trust.Suggestions = slices.DeleteFunc(slices.Clone(trust.Suggestions), func(s plugin.Suggestion) bool { return s.ID == id })
	m.suggestionRequests.apply(m, trust)
}
