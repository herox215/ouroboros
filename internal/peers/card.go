package peers

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"ouroboros/internal/a2a"
	"ouroboros/internal/discovery"
)

const (
	maxCard     = 300             // characters of the description of a peer
	cardTimeout = 2 * time.Second // for the card of a peer; nobody waits longer for it
)

// describe is what the node n says about itself in its agent card, or "" if
// it cannot be asked.
func describe(ctx context.Context, n discovery.Node) string {
	ctx, cancel := context.WithTimeout(ctx, cardTimeout)
	defer cancel()
	card, err := a2a.FetchCard(ctx, url(n.Addr()))
	if err != nil {
		return ""
	}
	return oneLine(card.Description)
}

// oneLine fits what a node says about itself into a line of a list.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxCard {
		s = string([]rune(s)[:maxCard-1]) + "…"
	}
	return s
}
