package matrix

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func seedVotePoll(c *Client, unstable bool, max ...int) {
	name := "m.poll.start"
	if unstable {
		name = "org.matrix.msc3381.poll.start"
	}
	content := pollStartFixture(unstable)
	key := "m.poll"
	if unstable {
		key = "org.matrix.msc3381.poll.start"
	}
	if len(max) > 0 {
		content[key].(map[string]any)["max_selections"] = max[0]
	}
	c.polls.update(pollFixture(name, "$p", "@alice:test", "!dm:test", time.Now().Add(-time.Minute).UnixMilli(), content))
}

func TestVotePollToRejectsInvalidInputs(t *testing.T) {
	for _, tc := range []struct {
		name, room, peer, poll string
		options                []string
		closed                 bool
	}{
		{name: "missing peer", room: "!dm:test", poll: "$p"},
		{name: "unknown", room: "!dm:test", peer: "@alice:test", poll: "$unknown"},
		{name: "wrong room", room: "!other:test", peer: "@alice:test", poll: "$p"},
		{name: "wrong creator", room: "!dm:test", peer: "@bob:test", poll: "$p"},
		{name: "unknown option", room: "!dm:test", peer: "@alice:test", poll: "$p", options: []string{"no"}},
		{name: "duplicate", room: "!dm:test", peer: "@alice:test", poll: "$p", options: []string{"a", "a"}},
		{name: "too many", room: "!dm:test", peer: "@alice:test", poll: "$p", options: []string{"a", "b"}},
		{name: "closed", room: "!dm:test", peer: "@alice:test", poll: "$p", closed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHomeserver(t)
			c := testClient(t, h.server.URL)
			crypt := &fakeCrypto{}
			c.crypto = crypt
			c.running = true
			if tc.name == "duplicate" {
				seedVotePoll(c, false, 2)
			} else {
				seedVotePoll(c, false)
			}
			if tc.closed {
				c.polls.update(pollUpdateFixture("end", "$end", "@alice:test", "!dm:test", "$p", time.Now().UnixMilli(), false, nil))
			}
			if err := c.VotePollTo(context.Background(), tc.room, tc.peer, tc.poll, tc.options); err == nil {
				t.Fatal("accepted invalid vote")
			}
			if crypt.encrypted != 0 {
				t.Fatal("shared keys for invalid vote")
			}
			if sent, _, _ := h.requests(); len(sent) != 0 {
				t.Fatal("published invalid vote")
			}
		})
	}
	// Restart cannot establish current open status from just a fetched start.
	h := newHomeserver(t)
	c := testClient(t, h.server.URL)
	if err := c.VotePollTo(context.Background(), "!dm:test", "@alice:test", "$p", nil); err == nil || !strings.Contains(err.Error(), "unknown Matrix poll") {
		t.Fatal(err)
	}
}

func TestVotePollToSecurityAndClosureRaces(t *testing.T) {
	for _, mode := range []string{"unsafe before", "unsafe during", "peer before", "peer during", "closed during", "encryption failure", "stopped", "close while running"} {
		t.Run(mode, func(t *testing.T) {
			h := newHomeserver(t)
			c := testClient(t, h.server.URL)
			crypt := &fakeCrypto{}
			c.crypto = crypt
			c.running = true
			seedVotePoll(c, false)
			c.allowed["@bob:test"] = true
			crowded := func() {
				h.room("!dm:test", map[string]event.Membership{"@alice:test": event.MembershipJoin, "@eve:test": event.MembershipInvite})
			}
			replaced := func() { h.room("!dm:test", map[string]event.Membership{"@bob:test": event.MembershipJoin}) }
			switch mode {
			case "unsafe before":
				crowded()
			case "peer before":
				replaced()
			case "unsafe during":
				crypt.sealing = func() error { crowded(); return nil }
			case "peer during":
				crypt.sealing = func() error { replaced(); return nil }
			case "closed during":
				crypt.sealing = func() error {
					c.polls.update(pollUpdateFixture("end", "$end", "@alice:test", "!dm:test", "$p", time.Now().UnixMilli(), false, nil))
					return nil
				}
			case "encryption failure":
				crypt.sealing = func() error { return errors.New("secret") }
			case "stopped":
				c.mu.Lock()
				c.running = false
				c.mu.Unlock()
			case "close while running":
				if err := c.Close(); err == nil {
					t.Fatal("closed live crypto")
				}
				crypt.sealing = func() error { return errors.New("stop") }
			}
			if err := c.VotePollTo(context.Background(), "!dm:test", "@alice:test", "$p", []string{"a"}); err == nil {
				t.Fatal("unsafe vote accepted")
			}
			if sent, _, _ := h.requests(); len(sent) != 0 {
				t.Fatal("unsafe ciphertext published")
			}
			if (mode == "unsafe before" || mode == "peer before" || mode == "stopped") && crypt.encrypted != 0 {
				t.Fatal("encrypted before validation")
			}
		})
	}
}

func TestVotePollToConcurrentTrackerAndLifecycle(t *testing.T) {
	h := newHomeserver(t)
	c := testClient(t, h.server.URL)
	c.crypto = &fakeCrypto{}
	c.running = true
	seedVotePoll(c, false)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_ = c.VotePollTo(context.Background(), "!dm:test", "@alice:test", "$p", []string{"a"})
			} else {
				c.polls.snapshot("!dm:test", "@alice:test")
				c.mu.Lock()
				c.running = false
				c.mu.Unlock()
				_ = c.Close()
				c.polls.update(pollUpdateFixture("end", fmt.Sprintf("$end%d", i), "@alice:test", "!dm:test", "$p", time.Now().UnixMilli(), false, nil))
			}
		}(i)
	}
	wg.Wait()
	if err := c.VotePollTo(context.Background(), "!dm:test", "@alice:test", "$p", nil); err == nil {
		t.Fatal("vote after stop/closure")
	}
}

func TestRunPollCanVoteInCallback(t *testing.T) {
	live := time.Now().Add(time.Second).UnixMilli()
	crypt := &fakeCrypto{change: map[id.EventID]func(*event.Event){"$p": func(e *event.Event) {
		e.Type = event.NewEventType("m.poll.start")
		e.Timestamp = time.Now().UnixMilli()
		e.Content = event.Content{Raw: pollStartFixture(false)}
	}}}
	useCrypto(t, crypt, nil)
	h := newHomeserver(t, joined(nil), joined(map[string][]map[string]any{"!dm:test": {encryptedEvent("$p", "@alice:test", live)}}))
	c := testClient(t, h.server.URL)
	got, _ := runUntilIdle(t, h, c, func(ctx context.Context, m Message) {
		if err := c.VotePollTo(ctx, m.RoomID, m.Sender, "$p", []string{"b"}); err != nil {
			t.Error(err)
		}
	})
	if len(got) != 1 || pollCount(c.polls.snapshot("!dm:test", "@alice:test")[0], 1) != 1 {
		t.Fatal("Run and sender do not share tracker")
	}
}

// Configuration reloads construct independent clients; a replacement's
// allowlist/tracker must neither authorize the old queued vote nor inherit it.
func TestVotePollToConcurrentConfigurations(t *testing.T) {
	h := newHomeserver(t)
	old := testClient(t, h.server.URL)
	cfg := old.cfg
	cfg.AllowedUsers = []string{"@bob:test"}
	replacement, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	originalCrypto, newCrypto := &fakeCrypto{}, &fakeCrypto{}
	old.crypto = originalCrypto
	old.running = true
	replacement.crypto = newCrypto
	replacement.running = true
	seedVotePoll(old, false)
	seedVotePoll(replacement, false) // Even a retained poll cannot override config.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := old.VotePollTo(context.Background(), "!dm:test", "@alice:test", "$p", []string{"a"}); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := replacement.VotePollTo(context.Background(), "!dm:test", "@alice:test", "$p", []string{"a"}); err == nil {
				t.Error("replacement ignored its allowlist")
			}
		}()
	}
	wg.Wait()
	if originalCrypto.encrypted != 8 || newCrypto.encrypted != 0 {
		t.Fatal("configuration leaked between transports")
	}
	fresh, err := New(old.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.VotePollTo(context.Background(), "!dm:test", "@alice:test", "$p", nil); err == nil || !strings.Contains(err.Error(), "unknown Matrix poll") {
		t.Fatal("new client inherited tracker", err)
	}
}
