package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// The node archives every answer of its agents (see history.Archive): of the
// owner and of every peer. A Service that has the archive goes on with the
// sessions of the owner in it, also with those from before the node started:
// once one is asked for, it is loaded from the archive, what happened in it
// for the front ends and what the agent remembers of it, cut as any
// conversation is. The conversations of peers in it are there to read, and
// to delete.

// archive is the archive of the node, as a Service uses it.
type archive struct {
	*history.Archive
	log func(format string, args ...any)
	// peer names the peer of a party of the archive, see history.Party.
	peer func(party string) string
	// forget makes the agent of the peers forget a conversation; nil while
	// no node answers peers.
	forget func(contextID string)
}

// Archive makes s go on with the sessions of the owner in a, and lets the
// front ends read and delete the conversations of peers in it. peer names
// the peer of a party of the archive; what cannot be read is logged with log.
func (s *Service) Archive(a *history.Archive, log func(format string, args ...any), peer func(party string) string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archive = &archive{Archive: a, log: log, peer: peer}
}

// ForgetPeers tells s how the agent that answers the peers forgets one of
// their conversations, once it is deleted.
func (s *Service) ForgetPeers(forget func(contextID string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.archive != nil {
		s.archive.forget = forget
	}
}

// load returns the session id of the owner as it is archived, nil if it is
// not: its events, and the agent remembers it. s.mu is held.
func (s *Service) load(id string) *session {
	if s.archive == nil {
		return nil
	}
	records, err := s.archive.Records(history.Owner(id))
	if err != nil && !errors.Is(err, history.ErrNotArchived) {
		s.archive.log("the session %s cannot be read from the archive: %v", id, err)
	}
	if len(records) == 0 {
		return nil
	}
	events, msgs := replay(records)
	memory, _ := json.Marshal(msgs) // messages are always written
	if err := s.agent.Resume(id, memory); err != nil {
		s.archive.log("what the agent remembers of the session %s is lost: %v", id, err)
	}
	var usage Event
	if len(events) > 0 {
		usage = events[len(events)-1]
	}
	if restorer, ok := s.agent.(interface {
		RestoreStatistics(string, int, assistant.Cache)
	}); ok {
		restorer.RestoreStatistics(id, usage.Context, usage.Cache)
	}
	last := records[len(records)-1].Time
	title, err := s.archive.Title(history.Owner(id))
	if err != nil {
		s.archive.log("the title of the session %s cannot be read: %v", id, err)
	}
	if title != "" {
		events = append(events, Event{Kind: Title, At: last.Local(), Text: title, Context: usage.Context, Cache: usage.Cache})
	}
	se := &session{id: id, events: events, watchers: map[chan Event]struct{}{}, resumed: last, title: title}
	s.effortOf(se)
	return se
}

// replay returns what happened in an archived conversation, as its events,
// and what the agent remembers of it, as it did when its answers ended: an
// answer that failed or was stopped as Abandon leaves it.
//
// What is archived is what the agent was sent and gave, so the events lack
// what only the front ends were told: the calls of tools a script made, which
// are a call of run, why an answer failed, and the size of the context.
func replay(records []history.Record) ([]Event, []openai.Message) {
	var events []Event
	var msgs []openai.Message
	steps := map[string]Event{} // the calls of tools that started, by ID
	start := 0
	var size int
	var cache assistant.Cache
	for _, r := range records {
		at := r.Time.Local()
		switch r.Role {
		case "user":
			msgs = append(msgs, openai.Message{Role: "user", Content: r.Content})
			events = append(events, Event{Kind: Message, At: at, Text: unnoted(r.Content)})
		case "assistant":
			m := openai.Message{Role: "assistant", Content: r.Content}
			if len(r.ToolCalls) == 0 {
				msgs = append(msgs, m)
				events = append(events, Event{Kind: Answer, At: at, Text: r.Content})
				continue
			}
			events = append(events, Event{Kind: Say, At: at, Text: r.Content})
			for _, c := range r.ToolCalls {
				m.ToolCalls = append(m.ToolCalls, openai.ToolCall{ID: c.ID, Type: "function", Function: openai.FunctionCall{Name: c.Name, Arguments: c.Arguments}})
				step := Event{Kind: Step, At: at, Tool: c.Name, Args: args(c.Arguments)}
				steps[c.ID] = step
				events = append(events, step)
			}
			msgs = append(msgs, m)
		case "tool":
			msgs = append(msgs, openai.Message{Role: "tool", ToolCallID: r.ToolCallID, Content: r.Content})
			if step, ok := steps[r.ToolCallID]; ok {
				step.At, step.Done = at, true
				if failed, ok := strings.CutPrefix(r.Content, "error: "); ok {
					step.Err = failed
				}
				events = append(events, step)
			}
		case "end":
			if r.Statistics != nil {
				size = r.Statistics.Context
				var restored assistant.Cache
				if json.Unmarshal(r.Statistics.Cache, &restored) == nil {
					cache = restored
				}
			}
			switch r.Outcome {
			case history.Failed:
				msgs = agent.Abandon(msgs)
				events = append(events, Event{Kind: Failed, At: at, Text: "the answer failed"})
			case history.Canceled:
				msgs = agent.Abandon(msgs)
				events = append(events, Event{Kind: Canceled, At: at})
			}
			for i := start; i < len(events); i++ {
				events[i].Context, events[i].Cache = size, cache
			}
			start = len(events)
		}
	}
	return events, msgs
}

// args are the arguments of a call as an event has them: as JSON, or as a
// text of JSON when the model wrote no valid JSON.
func args(arguments string) json.RawMessage {
	if json.Valid([]byte(arguments)) {
		return json.RawMessage(arguments)
	}
	b, _ := json.Marshal(arguments) // a text: cannot fail
	return b
}

// noted starts a message of the owner as the agent is told it after ouro
// started anew, see resumed.
const noted = "[Not from the owner: ouro started anew"

// unnoted is a message of the owner as it is shown: without what the agent
// was told along with it.
func unnoted(text string) string {
	if !strings.HasPrefix(text, noted) {
		return text
	}
	if _, rest, ok := strings.Cut(text, "]\n\n"); ok {
		return rest
	}
	return text
}

// resumed is what the agent is told along with the first message of a
// session it goes on with after ouro started anew, since its last answer
// ended at last: what tools gave back before may no longer be so.
func resumed(last, now time.Time) string {
	return fmt.Sprintf(noted+" since the last answer of this conversation, %s ago. "+
		"What tools gave back before may be outdated, and the variables of scripts are gone.]\n\n", ago(now.Sub(last)))
}

// ago is how long d is, for people.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d/(24*time.Hour)), "day")
}

// plural is n things.
func plural(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

// Peers describes the conversations of peers in the archive, the latest
// first: ID names one for Read and Delete, Peer is who it is with.
func (s *Service) Peers(context.Context) ([]Info, error) {
	s.mu.Lock()
	a := s.archive
	s.mu.Unlock()
	if a == nil {
		return nil, nil
	}
	entries, err := a.List()
	var list []Info
	for _, e := range entries {
		if party, _, _ := strings.Cut(e.ID, "/"); party != "owner" {
			list = append(list, Info{ID: e.ID, Title: e.Title, Last: e.Modified, Peer: a.peer(party)})
		}
	}
	return list, err
}

// Read returns what happened in the conversation of a peer id, as Peers
// names it.
func (s *Service) Read(_ context.Context, id string) ([]Event, error) {
	s.mu.Lock()
	a := s.archive
	s.mu.Unlock()
	if a == nil || !peerID(id) {
		return nil, ErrNoSession
	}
	records, err := a.Records(id)
	if errors.Is(err, history.ErrNotArchived) {
		return nil, ErrNoSession
	}
	events, _ := replay(records)
	return events, err
}

// peerID reports whether id names a conversation of a peer, as Peers names
// them.
func peerID(id string) bool {
	party, _, ok := strings.Cut(id, "/")
	return ok && strings.HasPrefix(party, "peer-")
}

// deletePeer deletes the conversation of a peer id from the archive, and
// what the agent of the peers remembers of it.
func (s *Service) deletePeer(id string) error {
	s.mu.Lock()
	a := s.archive
	s.mu.Unlock()
	if a == nil {
		return ErrNoSession
	}
	err := a.Delete(id)
	if errors.Is(err, history.ErrNotArchived) {
		return ErrNoSession
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	forget := a.forget
	s.mu.Unlock()
	if forget != nil {
		_, conversation, _ := strings.Cut(id, "/")
		forget(conversation)
	}
	return nil
}
