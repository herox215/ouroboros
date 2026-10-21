//go:build goolm

package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestSDKVotePollCiphertextRoundTrip(t *testing.T) {
	for _, unstable := range []bool{false, true} {
		t.Run(fmt.Sprint(unstable), func(t *testing.T) {
			m := newMock(t)
			bot := m.client(t, "@bot:test", "BOT")
			alice := m.client(t, "@alice:test", "ALICE")
			setupCrypto(t, bot)
			receiver := setupCrypto(t, alice)
			bot.running = true
			seedVotePoll(bot, unstable, 2)
			for i, selections := range [][]string{{"a", "b"}, {}} {
				if err := bot.VotePollTo(context.Background(), "!dm:test", "@alice:test", "$p", selections); err != nil {
					t.Fatal(err)
				}
				m.deliver(alice)
				m.mu.Lock()
				if len(m.sent) != i+1 {
					t.Fatal("missing ciphertext")
				}
				wire := m.sent[i]
				m.mu.Unlock()
				if wire.Type != event.EventEncrypted || wire.Content.AsEncrypted().Algorithm != "m.megolm.v1.aes-sha2" {
					t.Fatal("not encrypted")
				}
				plain, err := receiver.decrypt(context.Background(), wire)
				if err != nil {
					t.Fatal(err)
				}
				name := "m.poll.response"
				if unstable {
					name = "org.matrix.msc3381.poll.response"
				}
				if plain.Type.Type != name {
					t.Fatalf("wrong payload type %s", plain.Type.Type)
				}
				raw, err := json.Marshal(&plain.Content)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				var relation struct {
					Type string `json:"rel_type"`
					ID   string `json:"event_id"`
				}
				if err := json.Unmarshal(fields["m.relates_to"], &relation); err != nil || relation.Type != "m.reference" || relation.ID != "$p" {
					t.Fatalf("relation %s", raw)
				}
				selected := fields["m.selections"]
				if unstable {
					var response struct {
						Answers json.RawMessage `json:"answers"`
					}
					if err := json.Unmarshal(fields[name], &response); err != nil {
						t.Fatal(err)
					}
					selected = response.Answers
				}
				var got []string
				if err := json.Unmarshal(selected, &got); err != nil || !reflect.DeepEqual(got, selections) {
					t.Fatalf("selections %s want %v (%v)", selected, selections, err)
				}
				if plain.Mautrix.TrustSource == nil || plain.Mautrix.TrustSource.UserID != "@bot:test" {
					t.Fatal("unauthenticated payload")
				}
				snap := bot.polls.snapshot("!dm:test", "@alice:test")[0]
				want := 1
				if i == 1 {
					want = 0
				}
				if pollCount(snap, 0) != want || pollCount(snap, 1) != want {
					t.Fatalf("own vote/withdrawal not recorded: %v", snap)
				}
			}
		})
	}
}
