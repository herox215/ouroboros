package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/plugin"
)

type votingConnection struct {
	fakeConnection
	calls            int
	room, peer, poll string
	options          []string
	err              error
	ctx              context.Context
}

func (c *votingConnection) VotePollTo(ctx context.Context, room, peer, poll string, options []string) error {
	c.calls++
	c.ctx, c.room, c.peer, c.poll, c.options = ctx, room, peer, poll, slices.Clone(options)
	return c.err
}

var (
	_ pollVoter = (*sdkConnection)(nil)
	_ pollVoter = (*sessionConnection)(nil)
	_ pollVoter = guardedConnection{}
)

func pollFixture(t *testing.T) (store, *live, *conversations, *votingConnection) {
	t.Helper()
	s := store{t.TempDir()}
	if err := s.save(configuration{Enabled: true, Revision: "1"}); err != nil {
		t.Fatal(err)
	}
	conn := &votingConnection{}
	c := &conversations{entries: map[string]*conversation{
		"a": {Room: "!a:test", Identity: plugin.GatewayIdentity{Subject: "@owner:test\n!a:test"}},
		"b": {Room: "!b:test", Identity: plugin.GatewayIdentity{Subject: "@other:test\n!b:test"}},
	}}
	l := &live{}
	// Exercise the optional interface through the session wrapper too.
	l.set(guardedConnection{connection: &sessionConnection{connection: conn}, store: s, revision: "1"}, c)
	return s, l, c, conn
}

func TestGatewayPollVoteDestinationAndResult(t *testing.T) {
	s, l, c, conn := pollFixture(t)
	output := gatewayOutput(s, l, nil)
	for _, text := range []string{"option IDs", "end-to-end encrypted", "Destination", "multiple rooms", "not that it was counted"} {
		if !strings.Contains(output.PollDescription, text) {
			t.Errorf("poll description missing %q", text)
		}
	}
	ctx := context.Background()
	request := plugin.GatewayPollVoteRequest{PollID: "$poll", OptionIDs: []string{"one", "two"}}
	for _, destination := range []string{"", "!unknown:test"} {
		request.Destination = destination
		if result, err := output.VotePoll(ctx, request); err == nil || !strings.Contains(err.Error(), "choose the room") || result != "" {
			t.Fatalf("ambiguous/unknown destination: %q %v", result, err)
		}
	}
	if conn.calls != 0 {
		t.Fatal("guessed a room")
	}
	request.Destination = "!b:test"
	result, err := output.VotePoll(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Room      string   `json:"room"`
		To        string   `json:"to"`
		PollID    string   `json:"poll_id"`
		OptionIDs []string `json:"option_ids"`
		Sent      bool     `json:"sent"`
	}
	if err := json.Unmarshal([]byte(result), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Sent || got.Room != "!b:test" || got.To != "@other:test" || got.PollID != request.PollID || !slices.Equal(got.OptionIDs, request.OptionIDs) {
		t.Fatalf("result: %s", result)
	}
	if conn.calls != 1 || conn.ctx != ctx || conn.room != got.Room || conn.peer != got.To || conn.poll != got.PollID || !slices.Equal(conn.options, got.OptionIDs) {
		t.Fatalf("forwarding: %+v", conn)
	}
	request.OptionIDs = []string{}
	if _, err := output.VotePoll(ctx, request); err != nil || len(conn.options) != 0 {
		t.Fatalf("withdrawal: %+v, %v", conn, err)
	}
	delete(c.entries, "b")
	request.Destination = ""
	if _, err := output.VotePoll(ctx, request); err != nil || conn.room != "!a:test" || conn.peer != "@owner:test" {
		t.Fatalf("single room: %+v, %v", conn, err)
	}
	conn.err = errors.New("membership changed")
	if result, err := output.VotePoll(ctx, request); !errors.Is(err, conn.err) || result != "" {
		t.Fatalf("failed vote claimed success: %q %v", result, err)
	}
}

func TestGatewayPollVoteValidationAndOwner(t *testing.T) {
	s, l, _, conn := pollFixture(t)
	output := gatewayOutput(s, l, nil)
	for _, request := range []plugin.GatewayPollVoteRequest{
		{},
		{PollID: " \t", OptionIDs: []string{"one"}},
		{PollID: "$poll"},
		{PollID: "$poll", OptionIDs: []string{""}},
		{PollID: "$poll", OptionIDs: []string{" \t"}},
		{PollID: "$poll", OptionIDs: []string{"one", "one"}},
		{PollID: "$poll", OptionIDs: strings.Split("a b c d e f g h i j k l m n o p q r s t u", " ")},
	} {
		if result, err := output.VotePoll(context.Background(), request); err == nil || result != "" {
			t.Fatalf("accepted invalid vote: %+v, %q %v", request, result, err)
		}
	}
	peer := a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"})
	if _, err := output.VotePoll(peer, plugin.GatewayPollVoteRequest{}); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatalf("peer vote: %v", err)
	}
	if conn.calls != 0 {
		t.Fatalf("invalid vote reached transport: %d", conn.calls)
	}
}

func TestGatewayPollVoteUnavailable(t *testing.T) {
	s, l, c, _ := pollFixture(t)
	request := plugin.GatewayPollVoteRequest{PollID: "$poll", OptionIDs: []string{"one"}, Destination: "!a:test"}
	output := gatewayOutput(s, l, nil)
	l.set(guardedConnection{connection: &sessionConnection{connection: &fakeConnection{}}, store: s, revision: "1"}, c)
	if _, err := output.VotePoll(context.Background(), request); err == nil || !strings.Contains(err.Error(), "does not vote") {
		t.Fatalf("unsupported connection: %v", err)
	}
	c.entries = nil
	if _, err := output.VotePoll(context.Background(), request); err == nil || !strings.Contains(err.Error(), "no Matrix room") {
		t.Fatalf("no rooms: %v", err)
	}
	l.clear(c)
	if _, err := output.VotePoll(context.Background(), request); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("disconnected: %v", err)
	}
}

func TestPollVoteRevokedConfiguration(t *testing.T) {
	for _, settings := range []string{
		`{"enabled":true,"revision":"2"}`,
		`{"enabled":false,"revision":"1"}`,
		`{`,
	} {
		t.Run(settings, func(t *testing.T) {
			s, l, _, conn := pollFixture(t)
			output := gatewayOutput(s, l, nil)
			if err := os.WriteFile(s.path("settings.json"), []byte(settings), 0600); err != nil {
				t.Fatal(err)
			}
			request := plugin.GatewayPollVoteRequest{PollID: "$poll", OptionIDs: []string{"one"}, Destination: "!a:test"}
			if result, err := output.VotePoll(context.Background(), request); err == nil || !strings.Contains(err.Error(), "configuration changed") || result != "" || conn.calls != 0 {
				t.Fatalf("revoked vote: %q %v (%d calls)", result, err, conn.calls)
			}
		})
	}
}
