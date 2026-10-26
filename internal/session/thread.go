package session

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
	"ouroboros/internal/presentation"
)

// The thread is the one conversation in which the owner talks with the
// agent about everything: a session of the ID ThreadID, which List leaves
// out. Behind it, each topic is a session of its own, and several of them
// work at once. Its agent hands each message of the owner to the session of
// its topic, or starts one for it, see assistant.WithThread, and says
// nothing of it: what it does on its way is not shown. With every message it
// is told which sessions there are to hand it to: those of the owner in which
// something happened within the last recent, see routable; older ones are
// left out. Of an answer that took a message the thread handed on, the
// thread shows the answer as its own, or why it failed, and the questions it
// asks the owner as its questions, which the owner answers in the thread,
// see relay. Working tells how much works for it meanwhile. So the owner
// talks with one agent, which thinks while the sessions work.

// ThreadID is the ID of the thread.
const ThreadID = "thread"

// backgroundHead marks internal results that the thread must contextualize.
const backgroundHead = "[Background result for "

// recent is how long ago something must have happened in a session for the
// thread to hand messages to it.
const recent = 24 * time.Hour

// maxRouted is how many sessions the thread is told of at most, the latest
// first: the list comes with every message of the owner.
const maxRouted = 30

// threadEffort is how much the model thinks in the thread unless the owner
// set it: it only hands on, and is to be quick.
const threadEffort = "low"

// handleSize is how much of the ID of a session the agent of the thread is
// told: what tells the sessions of a day apart, for less to read than an ID.
const handleSize = 8

// handle is the ID of a session as the agent of the thread is told it.
func handle(id string) string { return id[:min(len(id), handleSize)] }

// routed is a session the thread may hand messages to.
type routed struct {
	id, title, directory string
	anchor               string // immutable original owner request, independent of title
	last                 time.Time
	running              bool
	question             string // what it asks the owner and waits for, "" for nothing
}

// routable are the sessions of the owner the thread may hand messages to:
// those in which something happened within the last recent, without the
// thread, the latest first, at most maxRouted of them. s.mu is held.
func (s *Service) routable() []routed {
	now := s.now()
	var list []routed
	seen := map[string]bool{ThreadID: true}
	for _, se := range s.sessions {
		seen[se.id] = true
		if se.id == ThreadID || len(se.events) == 0 {
			continue
		}
		list = append(list, routed{id: se.id, title: cmp.Or(se.title, se.events[0].Text), directory: se.directory,
			last: se.events[len(se.events)-1].At, running: se.cancel != nil, question: awaited(se.events)})
	}
	if s.archive != nil {
		entries, err := s.archive.List()
		if err != nil {
			s.archive.log("the archive cannot be listed for the thread: %v", err)
		}
		for _, e := range entries {
			id, ok := strings.CutPrefix(e.ID, "owner/")
			if !ok || seen[id] || e.Title == "" || now.Sub(e.Modified) > recent {
				continue
			}
			kept := &session{id: id}
			s.directoryOf(kept) // as the archive keeps it, without loading the session
			list = append(list, routed{id: id, title: e.Title, directory: kept.directory, last: e.Modified})
		}
	}
	list = slices.DeleteFunc(list, func(r routed) bool { return now.Sub(r.last) > recent })
	slices.SortFunc(list, func(a, b routed) int {
		if c := b.last.Compare(a.last); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})
	list = list[:min(len(list), maxRouted)]
	for i := range list {
		list[i].anchor = s.concern(list[i].id)
	}
	return list
}

// awaited is what a session waits for the owner to answer, as its events
// tell: the question it asked last, unless that wait is over, whose Asked
// tells no question; "" for none.
func awaited(events []Event) string {
	var last Event
	for _, e := range events {
		if e.Kind == Asked {
			last = e
		}
	}
	return last.AsQuestion().Question
}

// rosterHead begins what the thread is told of the sessions with every
// message of the owner, see roster.
const rosterHead = "[Sessions of the owner in the last 24 hours"

// roster is what the thread is told of the sessions it may hand messages
// to, the latest first: a line each, with its handle, title, its project,
// unless it is here, the project of the thread, and what it does. Nothing of
// it changes but what the sessions do, so that it is told anew only then, see
// inThread. It is no message of the owner, and is not shown as one, see
// isRoster.
func roster(sessions []routed, here string) string {
	if len(sessions) == 0 {
		return rosterHead + ": none. ouro tells them whenever they change; the owner did not write this.]"
	}
	var b strings.Builder
	b.WriteString(rosterHead + ", the latest first, as ouro tells them whenever they change; the owner did not write it:")
	for _, r := range sessions {
		fmt.Fprintf(&b, "\n- %s: %s", handle(r.id), strconv.Quote(oneLine(r.title)))
		if r.anchor != "" {
			b.WriteString(" · concern: " + strconv.Quote(concernBound(oneLine(r.anchor), 500)))
		}
		switch {
		case r.directory == here: // that of the thread goes without saying
		case r.directory == "":
			b.WriteString(" · in your workspace")
		default:
			b.WriteString(" · in " + r.directory)
		}
		switch {
		case r.question != "":
			b.WriteString(" · asks the owner and waits: " + strconv.Quote(oneLine(r.question)))
		case r.running:
			b.WriteString(" · working")
		}
	}
	b.WriteString("\n]")
	return b.String()
}

// isRoster reports whether text is what roster wrote.
func isRoster(text string) bool { return strings.HasPrefix(text, rosterHead) }

// oneLine is text on one line, so that it cannot end a line of the roster.
func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

// What the agent of the thread is told of a session it handed a message to,
// as a note in its conversation, see relay: relayHead, the ID and the title
// of the session, then what came of it.
const (
	relayHead     = "[Session "
	relayAnswered = " answered, and the owner was shown it as your answer:]\n\n"
	relayFailed   = " failed, and the owner was shown that it did: "
	relayAsked    = " asked the owner, as you, and they answered. The question, then the answer:]\n\n"
)

// relayText is what the agent of the thread is told of the session se: its
// handle and title, then what, one of the parts of relayAnswered, relayFailed
// or relayAsked, and text.
func relayText(se *session, what, text string) string {
	return relayHead + handle(se.id) + " " + strconv.Quote(cmp.Or(se.title, firstMessage(se.events))) + what + text
}

// relayKept is how much of the start and of the end of the answer of a
// session the agent of the thread is sent, see shaped: what it needs to tell
// what the owner refers to, often what the answer asked at its end.
const relayKept = 300

// relayGap is what stands in what is sent of an answer for what is left out.
const relayGap = "\n[…]\n"

// shaped is what the agent of the thread is sent of msgs: of what sessions
// answered only the start and the end, which is enough to hand on, as the
// sessions remember their answers; and the messages of the owner without
// where they wrote them, which tells the thread nothing. It shapes every
// message on its own and the same each time, and leaves out none: so each
// call of the LLM begins with all the one before sent, which is what the
// caches of providers keep.
func shaped(msgs []openai.Message) []openai.Message {
	msgs = slices.Clone(msgs)
	for i, m := range msgs {
		if m.Role != "user" {
			continue
		}
		if at := strings.Index(m.Content, relayAnswered); strings.HasPrefix(m.Content, relayHead) && at >= 0 && len(m.Content)-at-len(relayAnswered) > 2*relayKept+len(relayGap) {
			head, answer := m.Content[:at+len(relayAnswered)], m.Content[at+len(relayAnswered):]
			msgs[i].Content = head + strings.ToValidUTF8(answer[:relayKept], "") + relayGap + strings.ToValidUTF8(answer[len(answer)-relayKept:], "")
		}
		msgs[i].Source = MessageSource{}
	}
	return msgs
}

// recentOf is of msgs, the conversation of the thread, what happened within
// assistant.ThreadWindow before now: from the first message of the owner or of ouro
// that is not older, so that no call of a tool loses its result, or from
// none. Messages without a time are of the answer on its way, and new. The
// last list of the sessions it was told before that stays first: it is told
// it anew only once it changes, see inThread.
func recentOf(msgs []openai.Message, now time.Time) []openai.Message {
	start := slices.IndexFunc(msgs, func(m openai.Message) bool {
		return m.Role == "user" && (m.At.IsZero() || now.Sub(m.At) <= assistant.ThreadWindow)
	})
	if start < 0 {
		start = len(msgs)
	}
	kept := msgs[start:]
	if slices.ContainsFunc(kept, func(m openai.Message) bool { return m.Role == "user" && isRoster(m.Content) }) {
		return kept
	}
	for _, m := range slices.Backward(msgs[:start]) {
		if m.Role == "user" && isRoster(m.Content) {
			return append([]openai.Message{m}, kept...)
		}
	}
	return kept
}

// shapedFor is ctx for the answer in se: under which the agent of the thread
// is sent what shaped makes of what of its conversation is recent, see
// recentOf, if se is the thread.
func (s *Service) shapedFor(ctx context.Context, se *session) context.Context {
	if se.id != ThreadID {
		return ctx
	}
	now := s.now()
	return agent.WithSent(ctx, func(msgs []openai.Message) []openai.Message {
		return shaped(recentOf(msgs, now))
	})
}

// rosterEvery is after how many answers of the thread it is told the
// sessions anew, though they did not change: so that a list of them is still
// in what it remembers once that was cut, see agent.Memory.
const rosterEvery = 6

// inThread are the messages for the agent of the thread se, with the list
// of the sessions before them where it changed since it was last told, or was
// told rosterEvery answers ago; for another session, messages. What the
// thread is told stays as it was told it: so the list of the sessions is kept
// with the conversation rather than sent anew each time. s.mu is held.
func (s *Service) inThread(se *session, messages []openai.Message) []openai.Message {
	if se.id != ThreadID {
		return messages
	}
	sessions := roster(s.routable(), se.directory)
	se.told++
	if sessions == se.roster && se.told < rosterEvery {
		return messages
	}
	se.roster, se.told = sessions, 0
	return append([]openai.Message{{Role: "user", Content: sessions}}, messages...)
}

// resolve is id, the handle of a session or the start of its ID, as the
// ID of the one of routable it names; id as it is where it names none, or
// more than one. s.mu is held.
func (s *Service) resolve(id string) string {
	var named []string
	for _, r := range s.routable() {
		if r.id == id {
			return id
		}
		if strings.HasPrefix(r.id, id) {
			named = append(named, r.id)
		}
	}
	if len(named) == 1 {
		return named[0]
	}
	return id
}

// asRelay reads text as relayText wrote it, and reports whether it is such a
// note: as the events the thread showed of it, without their time.
func asRelay(text string) ([]Event, bool) {
	rest, ok := strings.CutPrefix(text, relayHead)
	_, rest, cut := strings.Cut(rest, " ")
	quoted, err := strconv.QuotedPrefix(rest)
	if !ok || !cut || err != nil {
		return nil, false
	}
	rest = rest[len(quoted):]
	if answer, ok := strings.CutPrefix(rest, relayAnswered); ok {
		return []Event{{Kind: Answer, Text: answer}}, true
	}
	if why, ok := strings.CutPrefix(rest, relayFailed); ok {
		why, ok = strings.CutSuffix(why, "]")
		return []Event{{Kind: Failed, Text: why}}, ok
	}
	asked, ok := strings.CutPrefix(rest, relayAsked)
	question, answer, cut := strings.Cut(asked, "\n\n")
	if !ok || !cut || !json.Valid([]byte(question)) {
		return nil, false
	}
	return []Event{{Kind: Asked, Args: json.RawMessage(question)}, {Kind: Asked, Done: true, Text: answer}}, true
}

// firstMessage is the first message of the owner among events, "" for none.
func firstMessage(events []Event) string {
	if i := slices.IndexFunc(events, func(e Event) bool { return e.Kind == Message }); i >= 0 {
		return events[i].Text
	}
	return ""
}

// threading is what the agent of the thread hands messages on with, see
// assistant.Thread.
type threading struct{ s *Service }

// Send hands text to the session id, one of routable, as a message of the
// owner: "" for their words, see words. The handle of the session names it
// as well as its ID.
func (t threading) Send(ctx context.Context, id, text string) (string, string, error) {
	t.s.mu.Lock()
	id = t.s.resolve(id)
	t.s.mu.Unlock()
	words, pictures := t.s.words(text)
	// Keep this request's words and pictures while newer thread input arrives.
	// Only thread-context races are retried, at most twice, and every attempt
	// obtains an independent decision from fresh evidence. Semantic refusals,
	// verifier failures and target changes are never retried here.
	const attempts = 3
	for attempt := 0; ; attempt++ {
		checked, decision, err := t.checkConcern(ctx, id, words, pictures, false)
		if err != nil {
			return "", "", err
		}
		threadChanged := false
		title, outcome, err := t.s.hand(ctx, id, words, pictures, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if changed := checked.changed(t.s, id); changed != "" {
				threadChanged = changed == "thread context"
				return fmt.Errorf("CheckTopic: %s changed during verification; no message sent. Recheck its current concern before retrying", changed)
			}
			if !slices.ContainsFunc(t.s.routable(), func(r routed) bool { return r.id == id }) {
				return fmt.Errorf("no session %q among those of the last 24 hours: hand the message to one of the list, or start a new one with %s", id, assistant.ToolSessionNew)
			}
			return nil
		})
		if threadChanged && attempt+1 < attempts {
			continue
		}
		if err != nil {
			return title, outcome, err
		}
		t.s.handedTo(id)
		// The tool result is archived with the routing call, making the fresh
		// decision inspectable without adding it to target context.
		return title, outcome + "; CheckTopic: continue (" + decision.Reason + ")", nil
	}
}

// Search returns the messages of the whole thread that hold query, see
// history.Archive.Search: also those its agent is no longer sent, see
// recentOf.
func (t threading) Search(_ context.Context, query string) (string, error) {
	t.s.mu.Lock()
	a := t.s.archive
	t.s.mu.Unlock()
	if a == nil {
		return "nothing of this conversation is kept to search", nil
	}
	found, _, err := a.Search(history.Who{Session: ThreadID}, query, false, 0)
	return found, err
}

// Start starts a session with text as its first message, "" for the words
// of the owner, see words, in directory, "" for the project of the thread.
func (t threading) Start(ctx context.Context, text, directory string) (string, error) {
	s := t.s
	text, pictures := s.words(text)
	if strings.TrimSpace(text) == "" && len(pictures) == 0 {
		return "", ErrEmpty
	}
	if directory == "" {
		s.mu.Lock()
		directory = s.get(ThreadID).directory
		s.mu.Unlock()
	}
	id, err := s.New(ctx)
	if err != nil {
		return "", err
	}
	if err := s.SetDirectory(ctx, id, directory); err != nil {
		s.Delete(ctx, id) // nothing was said in it: it goes as it came
		return "", err
	}
	if _, _, err := s.hand(ctx, id, text, pictures, func() error { return nil }); err != nil {
		s.Delete(ctx, id)
		return "", err
	}
	s.handedTo(id)
	return id, nil
}

// handedTo tells the thread that its agent started the session id or handed
// it a message, see Handed.
func (s *Service) handedTo(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handed(id)
}

// handed tells the thread that the session id concerns the part of it on
// its way, see Handed. s.mu is held.
func (s *Service) handed(id string) {
	s.emit(s.get(ThreadID), Event{Kind: Handed, Text: id})
}

// reported is the session whose result the background report text tells
// the agent of the thread, see relay, "" for none: so the thread tells
// Handed again when it is read back.
func reported(text string) string {
	rest, ok := strings.CutPrefix(text, backgroundHead)
	if !ok {
		return ""
	}
	// The title is quoted, see relay: skip it as a whole, as it may hold
	// anything, also what looks like a handle and a status.
	title, err := strconv.QuotedPrefix(rest)
	if err != nil {
		return ""
	}
	rest, ok = strings.CutPrefix(rest[len(title):], " (")
	if !ok {
		return ""
	}
	handle, _, ok := strings.Cut(rest, ") ")
	if !ok {
		return ""
	}
	return handle
}

// handedBy is the session a finished call of the thread's agent started or
// handed a message to, as its arguments or its result in the archive tell,
// "" for none: so the thread tells Handed again when it is read back.
func handedBy(step Event, result string) string {
	if step.Err != "" {
		return ""
	}
	switch step.Tool {
	case assistant.ToolSessionSend, assistant.ToolSessionState, assistant.ToolSessionStop:
		var to struct {
			Session string `json:"session"`
		}
		_ = json.Unmarshal(step.Args, &to) // what cannot be read names no session
		return to.Session
	case assistant.ToolSessionNew:
		_, rest, ok := strings.Cut(result, " in session ")
		started, _, _ := strings.Cut(rest, ".")
		if ok {
			return started
		}
	}
	return ""
}

// words is text, which the agent of the thread hands on, or where it gives
// none, what the owner said since its last answer, as they said it: so a
// session gets the words of the owner, and not what the agent of the thread
// makes of them. The pictures of those messages go with it either way: the
// agent of the thread cannot hand them on in words.
func (s *Service) words(text string) (string, []openai.Image) {
	s.mu.Lock()
	defer s.mu.Unlock()
	thread := s.get(ThreadID)
	pictures := slices.Clone(thread.pictures)
	if strings.TrimSpace(text) != "" {
		return text, pictures
	}
	return strings.Join(thread.said, "\n\n"), pictures
}

// hand gives text to the session id as a message of the owner that the
// thread handed on, once may, run with s.mu held, has nothing against it,
// and returns the title of the session and what became of the message: it
// answers the question the session waits for, the answer on its way takes
// it, or one starts for it. The thread shows what comes of that answer, see
// relay.
func (s *Service) hand(ctx context.Context, id, text string, pictures []openai.Image, may func() error) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(text) == "" && len(pictures) == 0 {
		return "", "", ErrEmpty
	}
	if err := s.agent.Ready(); err != nil {
		return "", "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.node
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case s.closed:
		return "", "", ErrStopped
	case p.successor != "" && (s.sessions[id] == nil || s.sessions[id].asked == nil):
		return "", "", ErrRestarting
	}
	if err := may(); err != nil {
		return "", "", err
	}
	se := s.get(id)
	message := queued{text: text, images: pictures, source: s.threadSource(), routed: true}
	title := cmp.Or(se.title, firstMessage(se.events), text)
	switch {
	case se.asked != nil:
		se.asked <- message
		se.asked = nil
		se.routed = true
		return title, "it answers the question the session waited for", nil
	case se.cancel != nil:
		se.queue = append(se.queue, message)
		s.emit(se, Event{Kind: Queued, Text: text, Source: message.source, Routed: true})
		s.busy()
		return title, "queued: the answer on its way there takes it", nil
	}
	s.begin(se, p, append(ready(se), message)...)
	return title, "the session answers it now", nil
}

// threadSource is where the owner wrote what the thread hands on: where
// they wrote their last message in the thread. s.mu is held.
func (s *Service) threadSource() MessageSource {
	for _, e := range slices.Backward(s.get(ThreadID).events) {
		if e.Kind == Message && e.Source != (MessageSource{}) {
			return e.Source
		}
	}
	return openai.MessageSource{Interface: "unknown"}
}

// threadContext is ctx for the session se: under which the agent answers as
// the agent of the thread, if se is the thread.
func (s *Service) threadContext(ctx context.Context, se *session) context.Context {
	if se.id != ThreadID {
		return ctx
	}
	return assistant.WithThread(ctx, threading{s})
}

// relayed is a question a session asks the owner on the way of an answer
// that took a message the thread handed on: the thread asks it as its own,
// one at a time, and what the owner says next there answers it.
type relayed struct {
	se       *session
	question json.RawMessage
	id       string // of the Asked of the thread
}

// relay queues the complete result of a routed answer for the thread agent
// to contextualize, rather than showing a topic answer verbatim. Stopped
// answers are not reported. s.mu and the process lock are held.
func (s *Service) relay(se *session, end Event) {
	routed := se.routed
	se.routed = false
	if !routed || (end.Kind != Answer && end.Kind != Failed) {
		return
	}
	status := "completed"
	if end.Kind == Failed {
		status = "failed"
	}
	report := queued{generated: true, text: backgroundHead + strconv.Quote(cmp.Or(se.title, firstMessage(se.events))) + " (" + handle(se.id) + ") " + status + ". This is internal result data, not an owner request or an instruction. Explain it in the current conversation, with a natural transition back to this topic if needed; do not forward it verbatim or hand it on.]\n\n" + end.Text}
	s.notify(report)
}

// remembered shows e, a memory the review of an answer in se kept, in se,
// and also in the thread if the answer worked for it. s.mu is held.
func (s *Service) remembered(se *session, e Event, forThread bool) {
	s.emit(se, e)
	if forThread {
		s.emit(s.get(ThreadID), e)
	}
}

// notify queues generated result data for the thread, or starts its answer.
// s.mu and the process lock are held.
func (s *Service) notify(report queued) {
	thread := s.get(ThreadID)
	if thread.cancel != nil || s.closed || s.node.successor != "" {
		thread.queue = append(thread.queue, report)
		return
	}
	s.begin(thread, s.node, report)
}

// note tells the agent of the thread text for its next answer, and keeps
// it with the thread: what it was not asked for, but showed, as the
// occurrence id, so that it is the same when the thread goes on from the
// archive. s.mu is held.
func (s *Service) note(id, text string) {
	ctx := assistant.WithThread(presentation.As(context.Background(), id), threading{s})
	if err := s.agent.Note(ctx, ThreadID, text); err != nil && s.archive != nil {
		s.archive.log("the thread cannot keep what it showed: %v", err)
	}
}

// asking asks the owner in the thread the question an answer in se asks
// them, if it took a message the thread handed on or has one waiting behind
// its current tool: at once, unless the thread asks another one. s.mu is held.
func (s *Service) asking(se *session, question json.RawMessage) {
	if !forThread(se) {
		return
	}
	thread := s.get(ThreadID)
	thread.questions = append(thread.questions, &relayed{se: se, question: question})
	if len(thread.questions) == 1 {
		s.ask1(thread)
	}
}

// forThread reports whether se works for the thread: the answer on its way
// took a message the thread handed on, or one waits for it. s.mu is held.
func forThread(se *session) bool {
	return se.routed || waitsForThread(se)
}

// waitsForThread reports whether a message the thread handed on waits in
// the queue of se. s.mu is held.
func waitsForThread(se *session) bool {
	return slices.ContainsFunc(se.queue, func(m queued) bool { return m.routed })
}

// withdraw takes back the question of se the thread relays, once nothing in
// se works for the thread any more: it waits on in se alone. s.mu is held.
func (s *Service) withdraw(se *session) {
	thread := s.sessions[ThreadID]
	if forThread(se) || thread == nil {
		return
	}
	i := slices.IndexFunc(thread.questions, func(q *relayed) bool { return q.se == se })
	if i < 0 {
		return
	}
	q := thread.questions[i]
	thread.questions = slices.Delete(thread.questions, i, i+1)
	if i > 0 {
		return // it was not asked yet
	}
	s.emit(thread, Event{Kind: Asked, PresentationID: q.id, Done: true})
	if len(thread.questions) > 0 {
		s.ask1(thread)
	}
}

// ask1 asks the owner in thread the first of the questions it relays. s.mu
// is held.
func (s *Service) ask1(thread *session) {
	q := thread.questions[0]
	q.id = presentation.NewID()
	s.emit(thread, Event{Kind: Asked, PresentationID: q.id, Args: q.question})
}

// answered ends the question of se the thread relays, once its wait is
// over with answer, "" for none: one that was asked in the thread is told
// to be over there, and what the owner answered is told to the agent of the
// thread. The next question waits no more. s.mu is held.
func (s *Service) answered(se *session, answer queued) {
	thread := s.sessions[ThreadID]
	if thread == nil {
		return
	}
	i := slices.IndexFunc(thread.questions, func(q *relayed) bool { return q.se == se })
	if i < 0 {
		return
	}
	q := thread.questions[i]
	thread.questions = slices.Delete(thread.questions, i, i+1)
	if i > 0 {
		return // it was not asked yet
	}
	s.emit(thread, Event{Kind: Asked, PresentationID: q.id, Done: true, Text: answer.text, Source: answer.source.Normalized()})
	if answer.text != "" {
		s.note(q.id, relayText(se, relayAsked, string(q.question)+"\n\n"+answer.text))
	}
	if len(thread.questions) > 0 {
		s.ask1(thread)
	}
}

// answerRelayed gives text, which the owner said in the thread, to the
// question the thread asks them, and reports whether there is one. s.mu is
// held.
func (s *Service) answerRelayed(text string, source MessageSource) bool {
	return s.answerRelayedImages(queued{text: text, source: source})
}

func (s *Service) answerRelayedImages(message queued) bool {
	thread := s.sessions[ThreadID]
	if thread == nil || len(thread.questions) == 0 || thread.questions[0].se.asked == nil {
		return false
	}
	se := thread.questions[0].se
	se.asked <- message
	se.asked = nil
	se.routed = true // the owner answered in the thread: it shows what comes of it
	return true
}

// busy tells the thread how much works for it, see Working, once that
// changed: its own answer, and those of the sessions it handed messages to,
// also those that wait for an answer to take them. s.mu is held.
func (s *Service) busy() {
	thread := s.sessions[ThreadID]
	if thread == nil {
		return
	}
	n := 0
	own := thread.cancel != nil
	for _, se := range s.sessions {
		if se.cancel != nil && (se == thread || se.routed) || waitsForThread(se) ||
			slices.ContainsFunc(se.tasks, func(t *task) bool { return t.routed }) {
			n++
		}
	}
	if n != thread.working || own != thread.own {
		thread.working, thread.own = n, own
		s.transient(thread, Event{Kind: Working, N: n, Own: own})
	}
}

// stopThread stops, with the answer of the thread, the answers of the
// sessions it handed messages to and the tasks that continue them, also
// those that outlived an answer that already ended. Other tasks are left
// alone. What it handed on that still waits, and the reports of the tasks,
// stay with the session, but the thread no longer shows what comes of them,
// nor asks their questions. s.mu is held.
func (s *Service) stopThread() {
	for _, se := range s.sessions {
		if se.routed && se.cancel != nil {
			se.cancel()
		}
		for _, t := range se.tasks {
			if t.routed {
				stopTask(t)
			}
		}
		unroute(se, false)
		s.withdraw(se)
	}
	s.busy()
}

// quietly is ctx for the answer in se: under which nothing of what the
// model thinks and writes on its way is told, if se is the thread, see
// quiet.
func (s *Service) quietly(ctx context.Context, se *session) context.Context {
	if se.id != ThreadID {
		return ctx
	}
	return openai.WithThought(openai.WithAnswering(openai.WithCalling(ctx, nil), nil), nil)
}

// along tells e, of what the agent does on its way to the answer in se,
// unless se is the thread, see quiet.
func (s *Service) along(se *session, e Event) {
	if se.id != ThreadID {
		s.add(se, e)
	}
}

// handedOn reports whether end, of an answer in se, is that of the thread
// when it handed on and answered nothing itself: it shows nothing, as the
// sessions answer.
func handedOn(se *session, end Event) bool {
	return se.id == ThreadID && end.Kind == Answer && strings.TrimSpace(end.Text) == ""
}

// quiet are of the events of the thread those that are shown: not what its
// agent did on its way, its words along with handing on and the calls that
// handed on.
func quiet(events []Event) []Event {
	return slices.DeleteFunc(events, func(e Event) bool { return e.Kind == Say || e.Kind == Step })
}
