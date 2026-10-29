package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/chat"
	"ouroboros/internal/history"
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
	completed, err := a.CompleteRestarts(processInstance)
	if err != nil {
		log("completed restarts cannot be kept: %v", err)
		return
	}
	for id, r := range completed {
		if se := s.sessions[strings.TrimPrefix(id, "owner/")]; se != nil {
			s.emit(se, Event{Kind: Restarted, At: r.Time.Local(), PresentationID: r.PresentationID, Text: r.Content})
		}
	}
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

// goOnText is the head of what a session that goes on by itself in a new
// build is told, in place of a message of the owner; resumeNote adds where it
// is and what it was asked, see GoOn.
const goOnText = "[Not from the owner: ouro runs the build you asked for now. Go on with what you were doing.]"

// interruptedText is the head of what a session is told whose answer was on
// its way when the node went away, and which goes on with it now: unlike an
// answer that ended before ouro started anew, this one stopped in the middle,
// and what it had started ended with the process.
const interruptedText = "[Not from the owner: this answer was stopped in the middle when ouro started anew, and the background jobs and tasks it had started ended with it. What it changed before may be incomplete. Check the state of what you were doing and go on with the task.]"

// maxTaskNote is how many bytes of the first task resumeNote repeats, the
// ellipsis that marks a cut included.
const maxTaskNote = 1000

// restartNote reports whether text is a note GoOn gives a session that goes on
// by itself, or an answer that stopped in the middle.
func restartNote(text string) bool {
	return strings.HasPrefix(text, goOnText) || strings.HasPrefix(text, interruptedText)
}

// resumeNote names the chat of the session id, as the archive lists it, and
// the task it was asked first, so that a session that goes on by itself need
// not reconstruct them from the log and the archive.
func (a *archive) resumeNote(id string) string {
	chat := strings.TrimPrefix(id, "owner/")
	task, err := a.FirstUser(id)
	if err != nil && !errors.Is(err, history.ErrNotArchived) {
		a.log("the first task of the session %s cannot be read: %v", id, err)
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return "\n\nThis chat is \"" + chat + "\" in the history archive."
	}
	note := "\n\nThis chat is \"" + chat + "\" in the history archive. Its first task, as the owner put it:\n" + cut(task)
	if last := a.lastOwner(id); last != "" && last != task {
		note += "\n\nWhat the owner wrote last:\n" + cut(last)
	}
	return note + "\n\n" + readLast
}

// archiveEnd tells a session whose answer stopped in the middle where its
// archive ends, and so how much of it is missing: the archive keeps an answer
// once it ended, so what the answer was asked and did is not in it, nor is
// anything that was lost besides. It says nothing when that cannot be read.
func (a *archive) archiveEnd(id string, now time.Time) string {
	records, err := a.Records(id)
	if err != nil && !errors.Is(err, history.ErrNotArchived) {
		a.log("where the archive of the session %s ends cannot be read: %v", id, err)
		return ""
	}
	if len(records) == 0 {
		return "\n\nNothing of this chat is archived: what the answer was asked and did is not in history_read."
	}
	last := records[len(records)-1].Time
	return fmt.Sprintf("\n\nThe archive of this chat ends at %s, %s before ouro started anew: what the answer was asked and did, and whatever came after that, is not in history_read.",
		last.Local().Format("2006-01-02 15:04"), ago(now.Sub(last)))
}

// readLast tells a session that goes on after ouro started anew where to
// read what it did last, rather than searching the archive for it.
const readLast = "To see what you did last, read the end of this chat first: history_read with from -20."

// cut is text bounded to maxTaskNote bytes, the ellipsis that marks a cut
// included.
func cut(text string) string {
	if len(text) > maxTaskNote {
		return strings.ToValidUTF8(text[:maxTaskNote-len("…")], "") + "…"
	}
	return text
}

// lastOwner is the last message the owner wrote in the session id: not a
// note of ouro or the report of a task, "" if there is none.
func (a *archive) lastOwner(id string) string {
	records, err := a.Records(id)
	if err != nil {
		return ""
	}
	for _, r := range slices.Backward(records) {
		text := strings.TrimSpace(unnoted(r.Content))
		if r.Role != "user" || text == "" || restartNote(text) || strings.HasPrefix(text, "[") {
			continue
		}
		return text
	}
	return ""
}

// lostTasksText tells of the tasks that ran when the node went away and ended
// with it, named, and that their work is lost. Alone says that nothing else is
// told the session, so that it says that ouro started anew.
func lostTasksText(tasks []history.Task, alone bool) string {
	head := "The tasks this conversation had delegated ended with the node, too, before they could report; their work is lost:"
	if alone {
		head = "[Not from the owner: ouro started anew, and the tasks this conversation delegated ended with it before they could report. Their work is lost:"
	}
	text := head + "\n" + history.TaskLines(tasks)
	text += "\n\nWhat they were to do is in the call of " + assistant.ToolDelegate + " in this chat: history_read with from -40 finds it; delegate them again if their work is needed."
	if alone {
		text += "]"
	}
	return text
}

// clearTasks tells whoever watches the session id that no task runs in it any
// more, see GoOn.
func (s *Service) clearTasks(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(s.get(id), Event{Kind: Tasks})
}

// GoOn lets the sessions of the owner go on that asked to once ouro started
// anew from the build they asked for, see AskGoOn, and those whose answer the
// previous image could not finish, see history.BeginAnswer: a session that
// asked is told goOnText, an interrupted one interruptedText, and either which
// chat it is and what it was asked first, see resumeNote, each once. The tasks
// that ran when the node went away are told to their session, and named,
// whether or not it goes on also: they are gone, and their work is lost. A
// node calls it when it starts.
func (s *Service) GoOn() {
	s.mu.Lock()
	a := s.archive
	s.mu.Unlock()
	if a == nil {
		return
	}
	ids, err := a.GoingOn()
	if err != nil {
		a.log("which sessions go on by themselves cannot be read: %v", err)
	}
	interrupted, err := a.Interrupted()
	if err != nil {
		a.log("which answers were interrupted cannot be read: %v", err)
	}
	lost, err := a.RestartedTasks(processInstance)
	if err != nil {
		a.log("which tasks ended with the node cannot be read: %v", err)
	}
	type note struct {
		head  string
		tasks []history.Task
	}
	notes := make(map[string]*note, len(ids)+len(interrupted)+len(lost))
	var order []string
	add := func(id, head string) {
		if _, ok := notes[id]; ok {
			return
		}
		notes[id] = &note{head: head}
		order = append(order, id)
	}
	for _, id := range ids {
		add(id, goOnText)
	}
	// An answer that asked to go on carries its own note of what was pending;
	// one that was only interrupted is told that it stopped in the middle.
	for _, id := range interrupted {
		add(id, interruptedText+a.archiveEnd(id, s.now()))
	}
	for id, tasks := range lost {
		if n, ok := notes[id]; ok {
			n.tasks = tasks
			continue
		}
		notes[id] = &note{tasks: tasks}
		order = append(order, id)
	}
	for _, id := range order {
		n := notes[id]
		text := n.head
		if len(n.tasks) > 0 {
			if text != "" {
				text += "\n\n"
			}
			text += lostTasksText(n.tasks, text == "")
		}
		text += a.resumeNote(id)
		name := strings.TrimPrefix(id, "owner/")
		if len(n.tasks) > 0 {
			s.clearTasks(name) // the tasks are gone: the owner sees them cleared before the note says so
		}
		if err := s.askInput(context.Background(), name, text, true); err != nil {
			a.log("the session %s does not go on by itself: %v", id, err)
		}
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
		// Metadata can precede the first message. Do not resume an empty
		// conversation or announce a restart when its first message arrives.
		title, titleErr := s.archive.Title(history.Owner(id))
		if titleErr != nil {
			s.archive.log("the title of the session %s cannot be read: %v", id, titleErr)
		}
		se := &session{id: id, title: title, watchers: map[chan Event]struct{}{}}
		s.effortOf(se)
		s.modelOf(se)
		s.directoryOf(se)
		if title != "" {
			se.events = append(se.events, Event{Kind: Title, Text: title})
		}
		if err != nil && title == "" && !se.efforted && !se.directed {
			return nil
		}
		s.loadTasks(se)
		return se
	}
	events, msgs := replay(records)
	memory, _ := json.Marshal(msgs) // messages are always written
	model, _, err := s.archive.Model(history.Owner(id))
	if err != nil {
		s.archive.log("the model of the session %s cannot be read: %v", id, err)
	}
	// A conversation whose model changed is not resumed: the model of the
	// node has none of it cached, so it would be sent in full. What follows
	// starts anew, see modelFor.
	if now := s.model(id); model == "" || now == "" || model == now {
		if err := s.agent.Resume(id, memory); err != nil {
			s.archive.log("what the agent remembers of the session %s is lost: %v", id, err)
		}
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
	if id == ThreadID {
		events = quiet(events)
	}
	se := &session{id: id, events: events, watchers: map[chan Event]struct{}{}, resumed: last, title: title}
	s.effortOf(se)
	s.modelOf(se)
	s.directoryOf(se)
	s.loadTasks(se)
	return se
}

// loadTasks tells se what the archive knows of its tasks: the number of the
// last one, so that a number is given once in a session, and those that ended
// with a node that started anew, see errNotRunning. s.mu is held.
func (s *Service) loadTasks(se *session) {
	tasks, err := s.archive.Tasks(history.Owner(se.id))
	if err != nil {
		s.archive.log("the tasks of the session %s cannot be read: %v", se.id, err)
		return
	}
	for _, t := range tasks {
		se.delegated = max(se.delegated, t.N)
		if t.Ended == history.TaskRestart {
			if se.lost == nil {
				se.lost = map[int]string{}
			}
			se.lost[t.N] = t.Title
		}
	}
}

// replay returns what happened in an archived conversation, as its events,
// and what the agent remembers of it, as it did when its answers ended: an
// answer that failed or was stopped as Abandon leaves it.
//
// What is archived is what the agent was sent and gave, so the events lack
// what only the front ends were told: the calls of tools a script made, which
// are a call of run, why an answer failed, and the size of the context.
func replay(records []history.Record) ([]Event, []chat.Message) {
	var events []Event
	var msgs []chat.Message
	steps := map[string]Event{} // the calls of tools that started, by ID
	start := 0
	var size int
	var cache assistant.Cache
	for i, r := range records {
		at := r.Time.Local()
		id := r.PresentationID
		if id == "" {
			// Legacy records have no live identity; their archive sequence is stable.
			seq := r.Seq
			if seq == 0 {
				seq = i + 1
			}
			id = fmt.Sprintf("archive:%d", seq)
		}
		switch r.Role {
		case history.Restarted:
			events = append(events, Event{Kind: Restarted, PresentationID: id, At: at, Text: r.Content, Context: size, Cache: cache})
		case history.ModelChanged:
			// Another model answers from here on: what came before is not
			// resumed, and it is not shown again.
			msgs = nil
		case "user":
			msgs = append(msgs, chat.Message{Role: "user", Content: r.Content, Source: r.Source, At: r.Time})
			if strings.HasPrefix(unnoted(r.Content), backgroundHead) {
				// An internal background result, not a message of the owner: it
				// tells which session the part of the thread it begins concerns.
				if of := reported(unnoted(r.Content)); of != "" {
					events = append(events, Event{Kind: Handed, At: at, Text: of})
				}
				continue
			}
			// What a tool said to the owner shows as it did, the same
			// occurrence; what the agent kept of what was asked shows not.
			if said, ok := strings.CutPrefix(r.Content, agent.SaidHead); ok {
				events = append(events, Event{Kind: Say, PresentationID: id, At: at, Text: said})
				continue
			}
			if strings.HasPrefix(r.Content, assistant.ExchangeHead) || strings.HasPrefix(r.Content, agent.DraftHead) {
				continue
			}
			if ended, ok := assistant.AsTaskReport(unnoted(r.Content)); ok {
				events = append(events, Event{Kind: Task, At: at, N: ended.N, Remark: ended.Title, Text: ended.Answer, Err: ended.Err})
				continue
			}
			// What the thread is told of the sessions is not said by the owner.
			if isRoster(unnoted(r.Content)) {
				continue
			}
			if relayed, ok := asRelay(unnoted(r.Content)); ok {
				for _, e := range relayed {
					e.At, e.PresentationID = at, id
					events = append(events, e)
				}
				continue
			}
			events = append(events, Event{Kind: Message, At: at, Text: unnoted(r.Content), Source: r.Source.Normalized()})
		case "assistant":
			m := chat.Message{Role: "assistant", Content: r.Content, At: r.Time}
			if len(r.ToolCalls) == 0 {
				msgs = append(msgs, m)
				events = append(events, Event{Kind: Answer, PresentationID: id, At: at, Text: r.Content})
				continue
			}
			events = append(events, Event{Kind: Say, PresentationID: id, At: at, Text: r.Content})
			for _, c := range r.ToolCalls {
				m.ToolCalls = append(m.ToolCalls, chat.ToolCall{ID: c.ID, Type: "function", Function: chat.FunctionCall{Name: c.Name, Arguments: c.Arguments}})
				step := Event{Kind: Step, At: at, CallID: c.ID, Tool: c.Name, Args: args(c.Arguments)}
				steps[c.ID] = step
				events = append(events, step)
			}
			msgs = append(msgs, m)
		case "tool":
			msgs = append(msgs, chat.Message{Role: "tool", ToolCallID: r.ToolCallID, Content: r.Content, At: r.Time})
			if step, ok := steps[r.ToolCallID]; ok {
				step.At, step.Done = at, true
				if failed, ok := strings.CutPrefix(r.Content, "error: "); ok {
					step.Err = failed
				}
				events = append(events, step)
				if to := handedBy(step, r.Content); to != "" {
					events = append(events, Event{Kind: Handed, At: at, Text: to})
				}
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
				events = append(events, Event{Kind: Failed, PresentationID: id, At: at, Text: "the answer failed"})
			case history.Canceled:
				msgs = agent.Abandon(msgs)
				events = append(events, Event{Kind: Canceled, PresentationID: id, At: at})
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
// started anew, see resumed. It says plainly that the owner wrote what comes
// after it: a note that began "Not from the owner" was taken by models for a
// mark of the whole message, and the owner for a stranger. Messages of
// before it begin with earlier.
const (
	noted   = "[A note of ouro, which the owner did not write: ouro started anew"
	earlier = "[Not from the owner: ouro started anew"
)

// unnoted is a message of the owner as it is shown: without what the agent
// was told along with it.
func unnoted(text string) string {
	if !strings.HasPrefix(text, noted) && !strings.HasPrefix(text, earlier) {
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
		"What tools gave back before may be outdated, and the variables of scripts are gone. What follows is the message of the owner.]\n\n", ago(now.Sub(last)))
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

// Find returns the sessions whose content holds query, as List and Peers
// name them: what a front end filters its list of sessions by besides the
// title of a session.
func (s *Service) Find(_ context.Context, query string) ([]string, error) {
	s.mu.Lock()
	a := s.archive
	s.mu.Unlock()
	if a == nil {
		return nil, nil
	}
	hits, err := a.Match(query)
	for i, id := range hits { // a session of the owner is named as List names it, without its party
		hits[i] = strings.TrimPrefix(id, "owner/")
	}
	hits = slices.DeleteFunc(hits, func(id string) bool { return id == ThreadID }) // which List leaves out too
	slices.Sort(hits)
	return hits, err
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
