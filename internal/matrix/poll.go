package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	pollPerOwner      = 4
	pollLimit         = 256
	pollVoterLimit    = 256
	pollHistoryLimit  = 16
	pollMetadataLimit = 12 * 1024 // Leave space in the gateway's 16 KiB budget.
)

type pollOption struct{ id, text string }
type pollVote struct {
	timestamp  int64
	event      id.EventID
	selections []string
}
type pollState struct {
	room             id.RoomID
	creator          id.UserID
	id               id.EventID
	timestamp, ended int64
	question, kind   string
	unstable         bool
	max              int
	options          []pollOption
	votes            map[id.UserID][]pollVote
	limited          bool
}

// pollTracker is an in-memory, bounded view, not an authoritative tally. Call
// only after authenticated sender, trust and safeRoom checks. Evicted polls and
// votes cannot be recovered without replay. No network or global SDK hooks.
type pollTracker struct {
	mu    sync.Mutex
	polls []*pollState // Oldest start first, with event ID breaking timestamp ties.
}

func pollEvent(e *event.Event) bool { return e != nil && pollAction(e.Type.Type) != "" }
func pollAction(name string) string {
	for _, prefix := range []string{"m.poll.", "org.matrix.msc3381.poll."} {
		if strings.HasPrefix(name, prefix) {
			switch action := strings.TrimPrefix(name, prefix); action {
			case "start", "response", "end":
				return action
			}
		}
	}
	return ""
}

func pollText(raw json.RawMessage) string {
	var c struct {
		Text []struct {
			Body string `json:"body"`
			MIME string `json:"mimetype"`
		} `json:"m.text"`
		Unstable string `json:"org.matrix.msc1767.text"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	for _, text := range c.Text {
		if text.MIME == "" || text.MIME == "text/plain" {
			return text.Body
		}
	}
	return c.Unstable
}
func pollBound(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.ToValidUTF8(s, "")
}
func pollID(s string) bool { return s != "" && len(s) <= 256 }

func parsePoll(fields map[string]json.RawMessage) *pollState {
	raw, ok := fields["m.poll"]
	if !ok {
		raw, ok = fields["m.poll.start"]
	} // Early stable-name clients.
	if !ok {
		raw = fields["org.matrix.msc3381.poll.start"]
	}
	var c struct {
		Question json.RawMessage   `json:"question"`
		Answers  []json.RawMessage `json:"answers"`
		Kind     string            `json:"kind"`
		Max      *int              `json:"max_selections"`
	}
	if json.Unmarshal(raw, &c) != nil || len(c.Answers) == 0 {
		return nil
	}
	p := &pollState{question: pollBound(pollText(c.Question), 256), kind: "undisclosed", max: 1, votes: make(map[id.UserID][]pollVote)}
	if strings.TrimSpace(p.question) == "" {
		return nil
	}
	if c.Kind == "m.disclosed" || c.Kind == "org.matrix.msc3381.poll.disclosed" {
		p.kind = "disclosed"
	}
	if c.Max != nil {
		if *c.Max < 1 {
			return nil
		}
		p.max = *c.Max
	}
	if len(c.Answers) > 20 {
		c.Answers = c.Answers[:20]
	}
	seen := make(map[string]bool)
	for _, raw := range c.Answers {
		var a struct {
			ID       string `json:"m.id"`
			Unstable string `json:"id"`
		}
		if json.Unmarshal(raw, &a) != nil {
			return nil
		}
		if a.ID == "" {
			a.ID = a.Unstable
		}
		text := pollBound(pollText(raw), 96)
		if !pollID(a.ID) || seen[a.ID] || strings.TrimSpace(text) == "" {
			return nil
		}
		seen[a.ID] = true
		p.options = append(p.options, pollOption{a.ID, text})
	}
	if p.max > len(p.options) {
		p.max = len(p.options)
	}
	return p
}

// update returns readable text only for a newly retained valid start. Responses
// (including invalid votes that cancel previous selections) and ends are silent.
func (t *pollTracker) update(e *event.Event) string {
	if !pollEvent(e) || !pollID(string(e.ID)) || !pollID(string(e.RoomID)) || !pollID(string(e.Sender)) || e.Timestamp <= 0 {
		return ""
	}
	raw, err := json.Marshal(&e.Content)
	if err != nil || len(raw) > 64*1024 {
		return ""
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if pollAction(e.Type.Type) == "start" {
		for _, p := range t.polls {
			if p.room == e.RoomID && p.id == e.ID {
				return ""
			}
		}
		p := parsePoll(fields)
		if p == nil {
			return ""
		}
		// Replacement starts are not new polls.
		var rel struct {
			Type string `json:"rel_type"`
		}
		if r, ok := fields["m.relates_to"]; ok {
			if json.Unmarshal(r, &rel) != nil || rel.Type == "m.replace" {
				return ""
			}
		}
		p.unstable = strings.HasPrefix(e.Type.Type, "org.matrix.msc3381.")
		p.room, p.creator, p.id, p.timestamp = e.RoomID, e.Sender, e.ID, e.Timestamp
		t.polls = append(t.polls, p)
		sort.Slice(t.polls, func(i, j int) bool {
			a, b := t.polls[i], t.polls[j]
			if a.timestamp == b.timestamp {
				return a.id < b.id
			}
			return a.timestamp < b.timestamp
		})
		count := 0
		for _, q := range t.polls {
			if q.room == p.room && q.creator == p.creator {
				count++
			}
		}
		for i := 0; count > pollPerOwner && i < len(t.polls); {
			q := t.polls[i]
			if q.room == p.room && q.creator == p.creator {
				t.polls = append(t.polls[:i], t.polls[i+1:]...)
				count--
			} else {
				i++
			}
		}
		if len(t.polls) > pollLimit {
			t.polls = t.polls[len(t.polls)-pollLimit:]
		}
		retained := false
		for _, q := range t.polls {
			if q == p {
				retained = true
				break
			}
		}
		if !retained {
			return ""
		}
		var text strings.Builder
		fmt.Fprintf(&text, "Poll: %s", p.question)
		for i, a := range p.options {
			fmt.Fprintf(&text, "\n%d. %s", i+1, a.text)
		}
		return text.String()
	}
	var rel struct {
		Type string     `json:"rel_type"`
		ID   id.EventID `json:"event_id"`
	}
	if json.Unmarshal(fields["m.relates_to"], &rel) != nil || rel.Type != "m.reference" {
		return ""
	}
	for _, p := range t.polls {
		if p.room != e.RoomID || p.id != rel.ID || e.Timestamp < p.timestamp {
			continue
		}
		if pollAction(e.Type.Type) == "end" {
			if p.creator == e.Sender && e.Timestamp >= p.timestamp && (p.ended == 0 || e.Timestamp < p.ended) {
				p.ended = e.Timestamp
			}
			return ""
		}
		if p.ended != 0 && e.Timestamp > p.ended {
			return ""
		}
		history, exists := p.votes[e.Sender]
		if !exists && len(p.votes) >= pollVoterLimit {
			p.limited = true
			return ""
		}
		// Retain a bounded history so a late end can exclude newer responses and
		// recover the preceding vote. Equal timestamps use event ID deterministically.
		for _, v := range history {
			if v.event == e.ID {
				return ""
			}
		}
		var selections []string
		selectionsRaw, ok := fields["m.selections"]
		if !ok {
			var c struct {
				Answers json.RawMessage `json:"answers"`
			}
			if json.Unmarshal(fields["org.matrix.msc3381.poll.response"], &c) == nil {
				selectionsRaw = c.Answers
			}
		}
		valid := json.Unmarshal(selectionsRaw, &selections) == nil
		seen := make(map[string]bool)
		// Unknown or repeated options spoil the entire vote, not just that option.
		for _, selected := range selections {
			found := false
			for _, a := range p.options {
				if a.id == selected {
					found = true
					break
				}
			}
			if !found || seen[selected] {
				valid = false
			}
			seen[selected] = true
		}
		if !valid {
			selections = nil
		} else if len(selections) > p.max {
			selections = selections[:p.max]
		}
		history = append(history, pollVote{e.Timestamp, e.ID, selections})
		sort.Slice(history, func(i, j int) bool {
			if history[i].timestamp == history[j].timestamp {
				return history[i].event < history[j].event
			}
			return history[i].timestamp < history[j].timestamp
		})
		if len(history) > pollHistoryLimit {
			history = history[len(history)-pollHistoryLimit:]
			p.limited = true
		}
		p.votes[e.Sender] = history
		return ""
	}
	return ""
}

// snapshot returns chronological, gateway-neutral objects for polls created by
// sender in room. No voter identities are exposed. Undisclosed open polls omit
// all participation data. Every map/slice is fresh; times are Unix milliseconds.
// Counts cover retained history only when limited is true. Oldest snapshots are
// omitted as necessary to keep their combined encoded size below 12 KiB.
func (t *pollTracker) snapshot(room id.RoomID, sender id.UserID) []map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]map[string]any, 0)
	for _, p := range t.polls {
		if p.room != room || p.creator != sender {
			continue
		}
		s := map[string]any{"id": string(p.id), "question": p.question, "kind": p.kind, "max_selections": p.max, "timestamp": p.timestamp, "closed": p.ended != 0}
		if p.ended != 0 {
			s["ended_at"] = p.ended
		}
		visible := p.kind == "disclosed" || p.ended != 0
		counts := make(map[string]int)
		voters := 0
		if visible {
			for _, history := range p.votes {
				for i := len(history) - 1; i >= 0; i-- {
					v := history[i]
					if p.ended != 0 && v.timestamp > p.ended {
						continue
					}
					if len(v.selections) > 0 {
						voters++
					}
					for _, selected := range v.selections {
						counts[selected]++
					}
					break
				}
			}
			s["voters"] = voters
			s["results_scope"] = "observed_events"
			s["limited"] = p.limited
		}
		options := make([]map[string]any, 0, len(p.options))
		for _, a := range p.options {
			option := map[string]any{"id": a.id, "text": a.text}
			if visible {
				option["votes"] = counts[a.id]
			}
			options = append(options, option)
		}
		s["options"] = options
		out = append(out, s)
	}
	for len(out) > 0 {
		raw, _ := json.Marshal(out)
		if len(raw) <= pollMetadataLimit {
			break
		}
		out = out[1:]
	}
	return out
}

// voteContent validates against a locked snapshot; it never returns mutable
// tracker state. Unknown polls fail closed, including after process restart.
func (t *pollTracker) voteContent(room id.RoomID, owner id.UserID, poll id.EventID, selections []string) (event.Type, map[string]any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range t.polls {
		if p.room != room || p.id != poll {
			continue
		}
		if p.creator != owner {
			return event.Type{}, nil, errors.New("the Matrix poll creator is not the expected peer")
		}
		if p.ended != 0 {
			return event.Type{}, nil, errors.New("the Matrix poll is closed")
		}
		if len(selections) > p.max {
			return event.Type{}, nil, errors.New("too many Matrix poll selections")
		}
		seen := make(map[string]bool)
		for _, selected := range selections {
			found := false
			for _, option := range p.options {
				found = found || option.id == selected
			}
			if !found || seen[selected] {
				return event.Type{}, nil, errors.New("invalid or duplicate Matrix poll option")
			}
			seen[selected] = true
		}
		// Always encode withdrawals as [], not null.
		answers := append([]string{}, selections...)
		content := map[string]any{"m.relates_to": map[string]any{"rel_type": "m.reference", "event_id": string(poll)}}
		name := "m.poll.response"
		if p.unstable {
			name = "org.matrix.msc3381.poll.response"
			content[name] = map[string]any{"answers": answers}
		} else {
			content["m.selections"] = answers
		}
		return event.NewEventType(name), content, nil
	}
	return event.Type{}, nil, errors.New("unknown Matrix poll in this room (not observed or no longer retained; history recovery is unavailable)")
}

// VotePollTo sends an encrypted response to an observed, open poll created by
// expectedPeer in room. Empty selections withdraw the current vote. The tracker
// is deliberately live-only: fetching a start alone cannot establish that a
// historical poll is still open, so unknown/evicted polls fail closed.
func (c *Client) VotePollTo(ctx context.Context, room, expectedPeer, pollID string, optionIDs []string) error {
	if expectedPeer == "" {
		return errors.New("the expected Matrix peer is required")
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	selections := append([]string{}, optionIDs...)
	kind, content, err := c.polls.voteContent(id.RoomID(room), id.UserID(expectedPeer), id.EventID(pollID), selections)
	if err != nil {
		return err
	}
	check := func() error {
		_, _, err := c.polls.voteContent(id.RoomID(room), id.UserID(expectedPeer), id.EventID(pollID), selections)
		return err
	}
	resp, err := c.sendEvent(ctx, room, id.UserID(expectedPeer), kind, content, check)
	if err != nil {
		return err
	}
	// Own inbound events are skipped. Record only after successful publication;
	// a concurrent end is still applied by update's timestamp/history rules.
	c.polls.update(&event.Event{ID: resp.EventID, RoomID: id.RoomID(room), Sender: c.cli.UserID, Timestamp: time.Now().UnixMilli(), Type: kind, Content: event.Content{Raw: content}})
	return nil
}
