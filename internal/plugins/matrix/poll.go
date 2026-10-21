package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"ouroboros/plugin"
)

// pollVoter is an optional connection capability, implemented by the SDK.
type pollVoter interface {
	VotePollTo(ctx context.Context, room, expectedPeer, pollID string, optionIDs []string) error
}

func (g guardedConnection) VotePollTo(ctx context.Context, room, peer, pollID string, optionIDs []string) error {
	if err := g.check(); err != nil {
		return err
	}
	return votePollTo(ctx, g.connection, room, peer, pollID, optionIDs)
}

// The session wrapper embeds only connection, so optional capabilities need
// explicit forwarding rather than relying on method promotion.
func (s *sessionConnection) VotePollTo(ctx context.Context, room, peer, pollID string, optionIDs []string) error {
	return votePollTo(ctx, s.connection, room, peer, pollID, optionIDs)
}

func votePollTo(ctx context.Context, conn connection, room, peer, pollID string, optionIDs []string) error {
	voter, ok := conn.(pollVoter)
	if !ok {
		return errors.New("this Matrix connection does not vote in polls")
	}
	return voter.VotePollTo(ctx, room, peer, pollID, optionIDs)
}

const pollDescription = "Vote in an existing Matrix poll, end-to-end encrypted, using its poll ID and exact option IDs from the received poll. Owner-only. An empty option ID array withdraws the vote. Polls must have been received during the current connection; after restart, older polls are unavailable. Destination is a Matrix room ID; omit it only when exactly one allowed room talks to Ouro. With multiple rooms, choose a destination explicitly; the gateway never guesses. A successful result means the vote was sent, not that it was counted."

func votePoll(l *live) func(context.Context, plugin.GatewayPollVoteRequest) (string, error) {
	return func(ctx context.Context, request plugin.GatewayPollVoteRequest) (string, error) {
		if _, peer := plugin.OriginFrom(ctx); peer {
			return "", errors.New("voting in Matrix polls is owner-only")
		}
		if strings.TrimSpace(request.PollID) == "" {
			return "", errors.New("give a poll ID")
		}
		if request.OptionIDs == nil {
			return "", errors.New("give option IDs or an empty array to withdraw the vote")
		}
		if len(request.OptionIDs) > 20 {
			return "", errors.New("give at most 20 option IDs")
		}
		seen := make(map[string]bool, len(request.OptionIDs))
		for _, id := range request.OptionIDs {
			if strings.TrimSpace(id) == "" {
				return "", errors.New("option IDs must not be empty")
			}
			if seen[id] {
				return "", errors.New("option IDs must be unique")
			}
			seen[id] = true
		}
		conn, rooms, err := l.current()
		if err != nil {
			return "", err
		}
		var target *room
		for i := range rooms {
			if request.Destination == "" && len(rooms) == 1 || rooms[i].ID == request.Destination {
				target = &rooms[i]
			}
		}
		if target == nil {
			if len(rooms) == 0 {
				return "", errors.New("no Matrix room talks to Ouro yet: write to it from Matrix first")
			}
			ids := make([]string, len(rooms))
			for i, r := range rooms {
				ids[i] = r.ID
			}
			return "", errors.New("choose the room: one of " + strings.Join(ids, ", "))
		}
		if err := conn.VotePollTo(ctx, target.ID, target.Peer, request.PollID, request.OptionIDs); err != nil {
			return "", err
		}
		result, _ := json.Marshal(struct {
			Room      string   `json:"room"`
			To        string   `json:"to"`
			PollID    string   `json:"poll_id"`
			OptionIDs []string `json:"option_ids"`
			Sent      bool     `json:"sent"`
		}{target.ID, target.Peer, request.PollID, request.OptionIDs, true})
		return string(result), nil
	}
}
