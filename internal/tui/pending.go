package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// acceptedAllMsg reports each decision independently: a failed item must not
// prevent the remaining items from being accepted or hide work already taken.
type acceptedAllMsg struct {
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
		var result acceptedAllMsg
		var problems []error
		for _, incoming := range trust.Incoming {
			if err := m.backend.Accept(m.ctx, incoming.PublicKey); err != nil {
				problems = append(problems, fmt.Errorf("connection %s: %w", shortKey(incoming.PublicKey), err))
			}
		}
		for _, proposal := range trust.Proposals {
			if err := m.backend.AcceptProposal(proposal.ID); err != nil {
				problems = append(problems, fmt.Errorf("proposal %s: %w", proposal.Field, err))
			}
		}
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
	m.requests.acceptingAll = false
	var cmd tea.Cmd
	if len(msg.suggestions) > 0 {
		var text strings.Builder
		text.WriteString("Work through all of the following accepted suggestions together in this single conversation. Check each against the source, implement those that hold up with their tests, and report the outcome of each.\n")
		for i, s := range msg.suggestions {
			fmt.Fprintf(&text, "\n%d. %s\n", i+1, s.Text)
			if len(s.Signals) > 0 {
				fmt.Fprintf(&text, "What was measured: %s\n", strings.Join(s.Signals, "; "))
			}
		}
		cmd = m.taken(takenMsg{suggestion: plugin.Suggestion{Text: text.String()}})
	} else {
		m.setNotice("all pending decisions processed", false)
		cmd = m.refresh()
	}
	if msg.err != nil {
		m.setNotice(msg.err.Error(), true)
	}
	return cmd
}
