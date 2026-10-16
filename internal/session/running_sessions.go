package session

import (
	"context"
	"slices"
	"strings"
)

// RunningSessions returns sorted titles of live owner sessions with an answer,
// delegated work, or a question awaiting the owner. This optional desktop
// capability does not load archives or include peer conversations.
func (s *Service) RunningSessions(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	titles, err := func() ([]string, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var titles []string
		for _, se := range s.sessions {
			if se.cancel == nil && len(se.tasks) == 0 && se.asked == nil {
				continue
			}
			title := se.title
			if title == "" && len(se.events) > 0 {
				title = se.events[0].Text
			}
			if len(title) > maxTitle {
				title = strings.ToValidUTF8(title[:maxTitle], "")
			}
			if title == "" {
				title = "Unbenannte Sitzung"
			}
			titles = append(titles, title)
		}
		return titles, nil
	}()
	if err != nil {
		return nil, err
	}
	slices.Sort(titles)
	return titles, ctx.Err()
}
