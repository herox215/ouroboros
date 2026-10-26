// Package chat is what the front ends of ouro share of a conversation with
// the agent: how an event of a session is shown, what is on its way in it,
// the question the agent asks there, the messages that wait in its queue,
// and where what the owner writes goes. The terminal UI and the window keep
// a State for each session and only draw it.
package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ouroboros/plugin"
)

// Event is what happened in a session, as the front ends show it: never the
// raw arguments or output of a tool.
type Event struct {
	Kind plugin.SessionKind
	// Text is the message, what the agent said along with the tools it asks
	// for, the answer, why it failed, or the binary ouro starts anew from.
	// Of a call of a tool it is what is shown of it, like "asking beta", ""
	// for nothing; once Done, it may say more, like how long the call took.
	// For kind "directory", it is the absolute project path, or empty to use
	// the session workspace again.
	Text string
	Done bool
	At   time.Time
	// Tool is the name of the tool of a call, CallID tells the call from
	// others, and Failed says that the call ended with an error.
	Tool   string
	CallID string
	Failed bool
	// Context is how long the context of the session is: what the last call
	// of the LLM in it sent, in bytes. 0 if none was made.
	Context int
	// Cache is how much of what the LLM was sent in the session the
	// provider had cached.
	Cache plugin.SessionCache
	// Options and Recommended are of a question the agent asks the owner,
	// plugin.SessionAsked, whose Text is the question: the answers to
	// choose from, and the one of them the agent would choose, "" for none.
	// Once Done, Text is what the owner answered, "" if they did not.
	Options     []string
	Recommended string
	// N is how much works for the thread, of plugin.SessionWorking, and Own
	// tells that the thread itself answers.
	N   int
	Own bool
	// Routed says of a message that the thread handed it on.
	Routed bool
}

// Show is e as the front ends show it. Of the tools the agent calls on the
// way, they are told what the plugins of ctx want shown: a call without a
// plugin.ToolView shows as nothing. Once the call ended, the line carries
// what the plugin of the tool says of its outcome.
func Show(ctx *plugin.Context, e plugin.SessionEvent) Event {
	shown := Event{Kind: e.Kind, Text: e.Text, Done: e.Done, At: e.At, CallID: e.CallID,
		Context: e.Context, Cache: e.Cache, Routed: e.Routed}
	switch e.Kind {
	case plugin.SessionStep:
		s := e.AsStep()
		shown.Tool, shown.Failed, shown.Text = s.Tool, s.Done && s.Err != nil, ""
		if ctx != nil {
			shown.Text = plugin.ToolLine(ctx, s)
		}
		if shown.Text != "" && s.Done {
			shown.Text = plugin.ToolDetail(ctx, s, shown.Text)
		}
	case plugin.SessionTask:
		shown.Text, shown.Failed = taskLine(e), e.Err != ""
	case plugin.SessionCalling:
		shown.Text = size(e.N)
	case plugin.SessionWorking:
		shown.N, shown.Own = e.N, e.Own
	case plugin.SessionAsked:
		if !e.Done {
			q := e.AsQuestion()
			shown.Text, shown.Options, shown.Recommended = q.Question, q.Options, q.Recommended
		}
	}
	return shown
}

// size is n bytes as the chat tells them: "512 bytes", "3.2 KiB".
func size(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/1024)
}

// taskLine is what the chat shows of the report of a task the agent
// delegated: its number and what it was, and how it ended.
func taskLine(e plugin.SessionEvent) string {
	line := fmt.Sprintf("task %d · %s", e.N, e.Remark)
	if e.Err != "" {
		return line + " ✗ " + strings.Join(strings.Fields(e.Err), " ")
	}
	return line + " ✓ " + e.Took.Round(time.Second).String()
}

// Question is what the agent asks the owner in a session, while it waits
// for the answer.
type Question struct {
	Text        string
	Options     []string // the answers to choose from
	Recommended string   // the one of them the agent would choose, "" for none
	Sent        bool     // the answer is on its way to the node
}

// State is what a front end knows of a session beside what it shows of its
// history: whether an answer is on its way, the messages queued for it, the
// question the agent asks, how much works for the thread, and whether the
// owner may write in it at all.
type State struct {
	// Peer is the peer a conversation of a peer with the agent is with: it
	// is only to read. "" for a session of the owner.
	Peer string
	// Pending says that a message was sent or an answer is on its way:
	// what is sent meanwhile is queued.
	Pending bool
	// Running says that the node has said an answer is on its way.
	Running bool
	// Queued are the messages that wait for the answer on its way to take
	// them.
	Queued []string
	// Asking is what the agent asks the owner and waits for; nil without a
	// question.
	Asking *Question
	// Working is how much works for the thread, and Own says that the
	// thread itself answers, not only the sessions it handed messages to.
	Working int
	Own     bool
}

// Apply takes what happened in the session. A question ends only when the
// wait for its answer is over: the question of an answer ends before it,
// and in the thread the answer of one session comes while another asks.
func (s *State) Apply(e Event) {
	switch e.Kind {
	case plugin.SessionMessage:
		s.Queued = s.Queued[min(1, len(s.Queued)):] // one that was queued is the first of them
		s.Pending, s.Running = true, true
	case plugin.SessionTask:
		// The report of a task starts an answer, or goes into the one on its
		// way, as a message of the owner does.
		s.Pending, s.Running = true, true
	case plugin.SessionWorking:
		s.Working, s.Own = e.N, e.Own
		// Background topics do not occupy the conversation input.
		if e.N == 0 {
			s.Pending, s.Running = false, false
		}
	case plugin.SessionQueued:
		s.Queued = append(s.Queued, e.Text)
	case plugin.SessionUnqueued:
		s.Queued = s.Queued[:max(0, len(s.Queued)-1)]
	case plugin.SessionAsked:
		s.Asking = nil
		if !e.Done {
			s.Asking = &Question{Text: e.Text, Options: e.Options, Recommended: e.Recommended}
		}
	case plugin.SessionAnswer, plugin.SessionFailed, plugin.SessionCanceled:
		s.Pending, s.Running = false, false // the loop of this answer is over
	}
}

// Lost forgets what was on its way once the events of the session stopped
// coming, as when the node stopped: what became of it is in what the session
// shows once it is watched again.
func (s *State) Lost() {
	s.Pending, s.Running, s.Asking, s.Working, s.Own = false, false, nil, 0, false
}

// Active is how many sessions the thread handed messages to work, beside
// the thread itself.
func (s *State) Active() int {
	active := s.Working
	if s.Own {
		active--
	}
	return max(0, active)
}

// Way is how what the owner writes goes to a session.
type Way int

const (
	// Ask starts an answer.
	Ask Way = iota
	// Steer gives it to the answer on its way: it is queued until the
	// answer takes it.
	Steer
	// Answer answers the question the agent asks, through Steer: in the
	// thread it answers the question of a session the thread relays,
	// without a turn of the thread.
	Answer
)

// ErrReadOnly is what writing in the conversation of a peer fails with.
var ErrReadOnly = errors.New("the conversation of a peer is only to read")

// Send says where what the owner writes now goes, and takes it as sent:
// what they say while the agent waits for an answer is that answer, what
// they say while an answer is on its way is queued for it, and else it
// starts an answer.
func (s *State) Send() (Way, error) {
	switch {
	case s.Peer != "":
		return 0, ErrReadOnly
	case s.Asking != nil && !s.Asking.Sent:
		s.Asking.Sent = true
		return Answer, nil
	case s.Pending:
		return Steer, nil
	}
	s.Pending = true
	return Ask, nil
}

// Unsent takes back what Send took as sent, when the node did not take it:
// what is on its way is what the node said, and the question waits for its
// answer again.
func (s *State) Unsent() {
	s.Pending = s.Running
	if s.Asking != nil {
		s.Asking.Sent = false
	}
}

// TakesBack reports whether the owner's key to take back, up in the input,
// takes back the last message queued for the answer on its way: with
// nothing written, as what is written is theirs to move in.
func (s *State) TakesBack(written string) bool {
	return written == "" && len(s.Queued) > 0
}

// TakenBack is what the input holds once text, the message taken back from
// the queue, is the owner's again: before what they wrote meanwhile, to
// change or to drop. text "" is none, which the answer took meanwhile.
func TakenBack(text, written string) string {
	if text == "" {
		return written
	}
	return strings.TrimSuffix(text+"\n"+written, "\n")
}

// Sessions is what Deliver gives messages to.
type Sessions interface {
	Ask(ctx context.Context, id, text string) error
	Steer(ctx context.Context, id, text string) error
}

// Deliver gives text to the session id the way Send said, as written in the
// front end named by from, like "tui".
func Deliver(ctx context.Context, sessions Sessions, id, text string, way Way, from string) error {
	ctx = plugin.WithMessageSource(ctx, plugin.MessageSource{Interface: from})
	if way == Ask {
		return sessions.Ask(ctx, id, text)
	}
	return sessions.Steer(ctx, id, text)
}
