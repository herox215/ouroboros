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

// describe is what the node n says about itself in its agent card. It reports
// false if n cannot be asked or does not prove to hold its public key: what
// was found at its address is then not the node the owner knows.
func describe(ctx context.Context, n discovery.Node) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, cardTimeout)
	defer cancel()
	card, err := a2a.FetchCard(ctx, url(n.Addr()), n.PublicKey)
	if err != nil {
		return "", false
	}
	return oneLine(card.Description), true
}

// key is the public key of a node that is known by its address only. Nothing
// vouches for it: it is the key of whoever answers at that address.
func key(ctx context.Context, n discovery.Node) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cardTimeout)
	defer cancel()
	return a2a.FetchKey(ctx, url(n.Addr()))
}

// oneLine fits what a node says about itself into a line of a list.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxCard {
		s = string([]rune(s)[:maxCard-1]) + "…"
	}
	return s
}
