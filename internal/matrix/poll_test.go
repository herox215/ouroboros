package matrix

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func pollFixture(kind, eid, sender, room string, ts int64, content map[string]any) *event.Event {
	return &event.Event{Type: event.NewEventType(kind), ID: id.EventID(eid), Sender: id.UserID(sender), RoomID: id.RoomID(room), Timestamp: ts, Content: event.Content{Raw: content}}
}
func pollStartFixture(unstable bool) map[string]any {
	if unstable {
		return map[string]any{"org.matrix.msc3381.poll.start": map[string]any{
			"question": map[string]any{"org.matrix.msc1767.text": "Lunch?"}, "kind": "org.matrix.msc3381.poll.disclosed",
			"answers": []any{map[string]any{"id": "a", "org.matrix.msc1767.text": "Pizza"}, map[string]any{"id": "b", "org.matrix.msc1767.text": "Soup"}},
		}}
	}
	return map[string]any{"m.poll": map[string]any{
		"question": map[string]any{"m.text": []any{map[string]any{"body": "Lunch?"}}}, "kind": "m.disclosed",
		"answers": []any{map[string]any{"m.id": "a", "m.text": []any{map[string]any{"body": "Pizza"}}}, map[string]any{"m.id": "b", "m.text": []any{map[string]any{"body": "Soup"}}}},
	}}
}
func pollUpdateFixture(action, eid, sender, room, target string, ts int64, unstable bool, selections any) *event.Event {
	prefix := "m.poll."
	if unstable {
		prefix = "org.matrix.msc3381.poll."
	}
	c := map[string]any{"m.relates_to": map[string]any{"rel_type": "m.reference", "event_id": target}}
	if action == "response" {
		if unstable {
			c[prefix+action] = map[string]any{"answers": selections}
		} else {
			c["m.selections"] = selections
		}
	} else if unstable {
		c[prefix+action] = map[string]any{}
	}
	return pollFixture(prefix+action, eid, sender, room, ts, c)
}
func pollCount(s map[string]any, index int) int {
	return s["options"].([]map[string]any)[index]["votes"].(int)
}

func TestPollAliasesAndLifecycle(t *testing.T) {
	for _, unstable := range []bool{false, true} {
		t.Run(fmt.Sprint(unstable), func(t *testing.T) {
			var tracker pollTracker
			prefix := "m.poll."
			if unstable {
				prefix = "org.matrix.msc3381.poll."
			}
			start := pollFixture(prefix+"start", "$p", "@a:x", "!r:x", 10, pollStartFixture(unstable))
			if !pollEvent(start) || tracker.update(start) != "Poll: Lunch?\n1. Pizza\n2. Soup" {
				t.Fatal("start not rendered")
			}
			if tracker.update(start) != "" {
				t.Fatal("duplicate rendered")
			}
			response := pollUpdateFixture("response", "$v", "@v:x", "!r:x", "$p", 20, !unstable, []string{"a"})
			if !pollEvent(response) || tracker.update(response) != "" {
				t.Fatal("response rendered")
			}
			s := tracker.snapshot("!r:x", "@a:x")[0]
			if pollCount(s, 0) != 1 || s["voters"] != 1 || s["max_selections"] != 1 {
				t.Fatalf("vote missing: %+v", s)
			}
			tracker.update(pollUpdateFixture("end", "$e", "@a:x", "!r:x", "$p", 30, unstable, nil))
			tracker.update(pollUpdateFixture("response", "$late", "@v:x", "!r:x", "$p", 31, unstable, []string{"b"}))
			s = tracker.snapshot("!r:x", "@a:x")[0]
			if s["closed"] != true || s["ended_at"] != int64(30) || pollCount(s, 0) != 1 {
				t.Fatalf("closure: %+v", s)
			}
		})
	}
	if pollEvent(nil) || pollEvent(pollFixture("m.poll.start.extra", "", "", "", 0, nil)) {
		t.Fatal("overbroad classifier")
	}
}

func TestPollPrivacyIsolationAndCopies(t *testing.T) {
	var tracker pollTracker
	c := pollStartFixture(false)
	c["m.poll"].(map[string]any)["kind"] = "unknown.kind"
	tracker.update(pollFixture("m.poll.start", "$p", "@a:x", "!r:x", 10, c))
	for _, e := range []*event.Event{
		pollUpdateFixture("response", "$wrongroom", "@v:x", "!other:x", "$p", 20, false, []string{"a"}),
		pollUpdateFixture("response", "$wrongpoll", "@v:x", "!r:x", "$other", 20, false, []string{"a"}),
		pollUpdateFixture("end", "$wrongcreator", "@v:x", "!r:x", "$p", 25, false, nil),
		pollUpdateFixture("response", "$v", "@v:x", "!r:x", "$p", 20, false, []string{"b"}),
	} {
		tracker.update(e)
	}
	if len(tracker.snapshot("!other:x", "@a:x")) != 0 || len(tracker.snapshot("!r:x", "@v:x")) != 0 {
		t.Fatal("scope leak")
	}
	s := tracker.snapshot("!r:x", "@a:x")[0]
	if s["closed"] != false || s["kind"] != "undisclosed" {
		t.Fatal(s)
	}
	for _, key := range []string{"voters", "limited"} {
		if _, ok := s[key]; ok {
			t.Fatal("participation leaked")
		}
	}
	options := s["options"].([]map[string]any)
	if _, ok := options[0]["votes"]; ok {
		t.Fatal("hidden votes leaked")
	}
	s["question"] = "changed"
	options[0]["text"] = "changed"
	tracker.update(pollUpdateFixture("end", "$end", "@a:x", "!r:x", "$p", 30, true, nil))
	next := tracker.snapshot("!r:x", "@a:x")[0]
	if next["question"] != "Lunch?" || pollCount(next, 1) != 1 || next["options"].([]map[string]any)[0]["text"] != "Pizza" {
		t.Fatal("mutated or lost state")
	}
	encoded, _ := json.Marshal(next)
	if strings.Contains(string(encoded), "@v:x") {
		t.Fatal("voter identity exposed")
	}
	if _, ok := options[1]["votes"]; ok {
		t.Fatal("previous snapshot mutated")
	}
}

func TestPollStaleInvalidAndLateEnd(t *testing.T) {
	var tracker pollTracker
	tracker.update(pollFixture("m.poll.start", "$p", "@a:x", "!r:x", 10, pollStartFixture(false)))
	vote := func(eid string, ts int64, choices any) {
		tracker.update(pollUpdateFixture("response", eid, "@v:x", "!r:x", "$p", ts, false, choices))
	}
	vote("$new", 30, []string{"b"})
	vote("$old", 20, []string{"a"})
	if pollCount(tracker.snapshot("!r:x", "@a:x")[0], 1) != 1 {
		t.Fatal("stale vote overwrote newer vote")
	}
	vote("$invalid", 40, []string{"a", "missing"})
	if tracker.snapshot("!r:x", "@a:x")[0]["voters"] != 0 {
		t.Fatal("invalid vote did not spoil previous vote")
	}
	vote("$duplicate", 45, []string{"a", "a"})
	vote("$malformed", 46, "a")
	vote("$withdraw", 47, []string{})
	if tracker.snapshot("!r:x", "@a:x")[0]["voters"] != 0 {
		t.Fatal("malformed/withdrawn vote counted")
	}
	tracker.update(pollUpdateFixture("end", "$early", "@a:x", "!r:x", "$p", 9, false, nil))
	if tracker.snapshot("!r:x", "@a:x")[0]["closed"] != false {
		t.Fatal("end before start accepted")
	}
	tracker.update(pollUpdateFixture("end", "$end", "@a:x", "!r:x", "$p", 25, false, nil))
	tracker.update(pollUpdateFixture("end", "$later", "@a:x", "!r:x", "$p", 50, false, nil))
	s := tracker.snapshot("!r:x", "@a:x")[0]
	if s["ended_at"] != int64(25) || pollCount(s, 0) != 1 {
		t.Fatal("late end did not recover preceding vote", s)
	}
	vote("$delayed", 24, []string{"b"})
	if pollCount(tracker.snapshot("!r:x", "@a:x")[0], 1) != 1 {
		t.Fatal("pre-close delayed vote ignored")
	}
}

func TestPollMalformedStartsAndRelations(t *testing.T) {
	cases := []func(map[string]any){
		func(c map[string]any) { delete(c, "question") },
		func(c map[string]any) { c["question"] = map[string]any{"m.text": "wrong"} },
		func(c map[string]any) { c["answers"] = []any{} },
		func(c map[string]any) { c["answers"] = []any{map[string]any{"m.id": "a"}} },
		func(c map[string]any) { a := c["answers"].([]any); a[1].(map[string]any)["m.id"] = "a" },
		func(c map[string]any) { c["max_selections"] = 0 },
		func(c map[string]any) { c["max_selections"] = 1.5 },
	}
	for i, change := range cases {
		var tracker pollTracker
		c := pollStartFixture(false)
		change(c["m.poll"].(map[string]any))
		if tracker.update(pollFixture("m.poll.start", "$p", "@a:x", "!r:x", 10, c)) != "" || len(tracker.snapshot("!r:x", "@a:x")) != 0 {
			t.Fatalf("malformed case %d accepted", i)
		}
	}
	var tracker pollTracker
	c := pollStartFixture(false)
	c["m.relates_to"] = map[string]any{"rel_type": "m.replace"}
	if tracker.update(pollFixture("m.poll.start", "$p", "@a:x", "!r:x", 10, c)) != "" {
		t.Fatal("replacement accepted")
	}
	tracker.update(pollFixture("m.poll.start", "$p", "@a:x", "!r:x", 10, pollStartFixture(false)))
	e := pollUpdateFixture("response", "$v", "@v:x", "!r:x", "$p", 20, false, []string{"a"})
	e.Content.Raw["m.relates_to"].(map[string]any)["rel_type"] = "m.annotation"
	tracker.update(e)
	if tracker.snapshot("!r:x", "@a:x")[0]["voters"] != 0 {
		t.Fatal("non-reference vote accepted")
	}
}

func TestPollBounds(t *testing.T) {
	var tracker pollTracker
	for i := 0; i < 8; i++ {
		tracker.update(pollFixture("m.poll.start", fmt.Sprintf("$%d", i), "@a:x", "!r:x", int64(10+i), pollStartFixture(false)))
	}
	s := tracker.snapshot("!r:x", "@a:x")
	if len(s) != pollPerOwner || s[0]["id"] != "$4" {
		t.Fatal("per-owner eviction", s)
	}
	if tracker.update(pollFixture("m.poll.start", "$stale", "@a:x", "!r:x", 1, pollStartFixture(false))) != "" {
		t.Fatal("stale evicted start rendered")
	}
	for i := 0; i < pollVoterLimit+5; i++ {
		tracker.update(pollUpdateFixture("response", fmt.Sprintf("$v%d", i), fmt.Sprintf("@v%d:x", i), "!r:x", "$7", 30, false, []string{"a"}))
	}
	for i := 0; i < pollHistoryLimit+5; i++ {
		tracker.update(pollUpdateFixture("response", fmt.Sprintf("$h%d", i), "@v0:x", "!r:x", "$7", int64(40+i), false, []string{"b"}))
	}
	p := tracker.polls[len(tracker.polls)-1]
	if len(p.votes) != pollVoterLimit || len(p.votes["@v0:x"]) != pollHistoryLimit || !p.limited {
		t.Fatal("vote bounds")
	}
	for i := 0; i < pollLimit+5; i++ {
		tracker.update(pollFixture("m.poll.start", fmt.Sprintf("$g%d", i), fmt.Sprintf("@g%d:x", i), "!r:x", int64(100+i), pollStartFixture(false)))
	}
	if len(tracker.polls) != pollLimit {
		t.Fatal("global bound")
	}
}

func TestPollTextAndMetadataBudget(t *testing.T) {
	var tracker pollTracker
	for i := 0; i < 4; i++ {
		c := pollStartFixture(false)
		p := c["m.poll"].(map[string]any)
		p["question"] = map[string]any{"m.text": []any{map[string]any{"mimetype": "text/html", "body": "ignored"}, map[string]any{"body": strings.Repeat("界", 200)}}}
		answers := make([]any, 25)
		for j := range answers {
			answers[j] = map[string]any{"m.id": fmt.Sprintf("%d%s", j, strings.Repeat("<", 20)), "m.text": []any{map[string]any{"body": strings.Repeat("界", 200)}}}
		}
		p["answers"] = answers
		p["max_selections"] = 30
		if tracker.update(pollFixture("m.poll.start", fmt.Sprintf("$%d", i), "@a:x", "!r:x", int64(10+i), c)) == "" {
			t.Fatal("bounded start rejected")
		}
	}
	s := tracker.snapshot("!r:x", "@a:x")
	raw, _ := json.Marshal(s)
	if len(raw) > pollMetadataLimit || len(s) == 0 {
		t.Fatal("metadata budget", len(raw))
	}
	for _, p := range tracker.polls {
		if len(p.question) > 256 || !utf8.ValidString(p.question) || len(p.options) != 20 || p.max != 20 || len(p.options[0].text) > 96 {
			t.Fatal("text/option bounds")
		}
	}
}

func TestPollSelectionLimitAndConcurrentSnapshots(t *testing.T) {
	var tracker pollTracker
	tracker.update(pollFixture("m.poll.start", "$p", "@a:x", "!r:x", 10, pollStartFixture(false)))
	tracker.update(pollUpdateFixture("response", "$v", "@v:x", "!r:x", "$p", 20, false, []string{"a", "b"}))
	s := tracker.snapshot("!r:x", "@a:x")[0]
	if pollCount(s, 0) != 1 || pollCount(s, 1) != 0 {
		t.Fatal("max selections not applied")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				tracker.update(pollUpdateFixture("response", fmt.Sprintf("$%d-%d", i, j), fmt.Sprintf("@v%d:x", i), "!r:x", "$p", int64(30+j), false, []string{"b"}))
				tracker.snapshot("!r:x", "@a:x")[0]["question"] = "local"
			}
		}(i)
	}
	wg.Wait()
	if tracker.snapshot("!r:x", "@a:x")[0]["question"] != "Lunch?" {
		t.Fatal("snapshot mutation")
	}
}
