package a2a

import (
	"context"
	"errors"
	"sync"
	"time"
)

// An agent that answers a peer may ask that peer back before it goes on, see
// AskBack. The peer waits for the answer to its request, so the question
// cannot be a request of its own: the two agents would wait for one another.
// Instead the question is what the request is answered with, for now, and
// the agent that asked stays as it is and waits. The peer answers the
// question with a message of its own, KindAnswer, and what that is answered
// with is what the agent says next: the result of the request, or another
// question. So only one side waits at a time, and no request is open across
// the other.
//
// A dialog is the run of the agent for one request, from the request to its
// result, over as many questions as the budget of the task allows.

var (
	// QuestionTime is how long an agent waits for the answer to a question
	// it asked back. Tests shorten it.
	QuestionTime = 10 * time.Minute
	// dialogKept is how long a dialog that ended is remembered, for a
	// message of it that is delivered once more.
	dialogKept = 10 * time.Minute
)

var (
	// ErrNoAsker is the error of AskBack where nobody is answered who could
	// be asked: in a conversation of the owner.
	ErrNoAsker = errors.New("a2a: there is no peer to ask back: this is no answer to a request of a peer")
	// ErrNoAnswer is the error of AskBack when the peer did not answer in
	// QuestionTime.
	ErrNoAnswer = errors.New("a2a: the peer did not answer the question in time")
	// ErrGaveUp is the error of AskBack when the peer gave up the question,
	// and with it the request.
	ErrGaveUp = errors.New("a2a: the peer gave up the request")
)

// dialogs are the dialogs of a server: those that run, and those that ended
// a little while ago.
type dialogs struct {
	mu sync.Mutex
	// byMessage finds what a message is answered with: a message that is
	// delivered twice is answered once, and starts nothing anew.
	byMessage map[string]turn
	// byQuestion finds the dialog whose agent waits for the answer to a
	// question, and byContext the one that runs in a conversation.
	byQuestion map[string]*dialog
	byContext  map[string]*dialog
}

// turn is what a message of a peer is answered with: the outcome at index
// of the dialog d.
type turn struct {
	d     *dialog
	index int
}

// dialog is the run of the agent for one request of a peer.
type dialog struct {
	all       *dialogs
	peer      string
	contextID string
	task      *Task
	cancel    context.CancelFunc

	mu       sync.Mutex
	outcomes []outcome     // what the agent said so far: its questions, then its result
	changed  chan struct{} // closed when the next outcome is there
	waiting  int           // requests of the peer that wait for an outcome
	open     *question     // the question the agent waits for the answer to
	ended    time.Time     // when the result was there; zero while it runs
}

// outcome is what the agent of a dialog answers a message with.
type outcome struct {
	msg *Message
	err *Error
}

// question is a question an agent asked back.
type question struct {
	id     string
	answer chan reply // takes one
}

// reply is what a question is answered with: the text of the peer, or why
// there is none.
type reply struct {
	text string
	err  error
}

type dialogKey struct{}

// key names a message, a question or a conversation of a peer: none of them
// is found by the key of another peer.
func key(peer, id string) string { return peer + "\n" + id }

// start returns what the request msg of the peer from is answered with: the
// first outcome of the dialog that run runs for it, which starts unless the
// request is one that was delivered before.
func (a *dialogs) start(ctx context.Context, from Caller, msg Message, hops int, run func(ctx context.Context, d *dialog)) (turn, *Error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, seen := a.byMessage[key(from.PublicKey, msg.MessageID)]; seen && msg.MessageID != "" {
		return t, nil
	}
	if running := a.byContext[key(from.PublicKey, msg.ContextID)]; running != nil {
		if id := running.asking(); id != "" {
			return turn{}, &Error{CodeQuestionOpen, "the agent asked back in this conversation and waits: answer the question " + id + " first"}
		}
	}
	task, room := taskOf(msg.meta().Task)
	if !room {
		return turn{}, &Error{CodeBudget, ErrBudget.Error()}
	}
	a.prune(time.Now())
	d := &dialog{all: a, peer: from.PublicKey, contextID: msg.ContextID, task: task, changed: make(chan struct{})}
	// The dialog outlives the request: the agent goes on while the peer
	// thinks about a question. It ends with the task at the latest.
	runCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), task.Budget().Until)
	d.cancel = cancel
	runCtx = context.WithValue(WithOrigin(runCtx, Origin{PublicKey: from.PublicKey, Hops: hops, Task: task}), dialogKey{}, d)
	if a.byMessage == nil {
		a.byMessage, a.byQuestion, a.byContext = map[string]turn{}, map[string]*dialog{}, map[string]*dialog{}
	}
	t := turn{d, 0}
	if msg.MessageID != "" {
		a.byMessage[key(d.peer, msg.MessageID)] = t
	}
	a.byContext[key(d.peer, d.contextID)] = d
	go run(runCtx, d)
	return t, nil
}

// prune forgets the dialogs that ended dialogKept before now. a.mu is held.
func (a *dialogs) prune(now time.Time) {
	for id, t := range a.byMessage {
		if ended := t.d.over(); !ended.IsZero() && now.Sub(ended) >= dialogKept {
			delete(a.byMessage, id)
		}
	}
}

// stop stops the agents of the dialogs that run.
func (a *dialogs) stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, d := range a.byContext {
		d.cancel()
	}
}

// end is told the result of the dialog d, which is over with it.
func (a *dialogs) end(d *dialog, o outcome) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byContext[key(d.peer, d.contextID)] == d {
		delete(a.byContext, key(d.peer, d.contextID))
	}
	d.mu.Lock()
	d.ended = time.Now()
	d.say(o)
	d.mu.Unlock()
	d.cancel()
}

// answered returns what the message msg of the peer from is answered with,
// which answers the question meta names or gives it up: the next outcome of
// the dialog that asked it. Given up, the dialog is stopped, and there is
// none.
func (a *dialogs) answered(from Caller, msg Message, meta Meta) (turn, bool, *Error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, seen := a.byMessage[key(from.PublicKey, msg.MessageID)]; seen && msg.MessageID != "" {
		return t, true, nil
	}
	d := a.byQuestion[key(from.PublicKey, meta.Question)]
	if d == nil {
		return turn{}, false, &Error{CodeNoQuestion, "no such question is open: it was answered or given up, or its agent stopped waiting"}
	}
	delete(a.byQuestion, key(from.PublicKey, meta.Question))
	d.task.heard(meta.Task)
	d.mu.Lock()
	q, t := d.open, turn{d, len(d.outcomes)}
	d.open = nil
	d.mu.Unlock()
	if meta.Kind == KindCancel {
		q.answer <- reply{err: ErrGaveUp}
		d.cancel()
		return turn{}, false, nil
	}
	if msg.MessageID != "" {
		a.byMessage[key(from.PublicKey, msg.MessageID)] = t
	}
	q.answer <- reply{text: msg.Text()}
	return t, true, nil
}

// asking is the ID of the question the agent of d waits for the answer to,
// "" if for none.
func (d *dialog) asking() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.open == nil {
		return ""
	}
	return d.open.id
}

// over is when d ended, zero if it runs.
func (d *dialog) over() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ended
}

// say adds what the agent says next. d.mu is held.
func (d *dialog) say(o outcome) {
	d.outcomes = append(d.outcomes, o)
	close(d.changed)
	d.changed = make(chan struct{})
}

// await returns the outcome at index of d once it is there. When ctx ends
// before, the peer gave up waiting: the agent is stopped then, unless another
// request of the peer still waits for the same.
func (d *dialog) await(ctx context.Context, index int) (*Message, *Error) {
	d.mu.Lock()
	d.waiting++
	for len(d.outcomes) <= index {
		changed := d.changed
		d.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			d.mu.Lock()
			d.waiting--
			alone := d.waiting == 0 && len(d.outcomes) <= index
			d.mu.Unlock()
			if alone {
				d.cancel()
			}
			return nil, &Error{CodeInternalError, "the request was given up"}
		}
		d.mu.Lock()
	}
	d.waiting--
	o := d.outcomes[index]
	d.mu.Unlock()
	return o.msg, o.err
}

// AskBack asks the peer whose request is answered under ctx what the agent
// needs to go on, and returns its answer. The peer gets the question as what
// its request is answered with for now, and answers it with a message of its
// own: until then the agent waits here, for QuestionTime at most. The
// question counts against the budget of the task, like every message of it.
//
// It fails with ErrNoAsker where no peer is answered, with ErrBudget when
// the budget of the task is used up, with ErrNoAnswer when the peer does not
// answer in time, and with ErrGaveUp when it gave up the request.
func AskBack(ctx context.Context, text string) (string, error) {
	d, _ := ctx.Value(dialogKey{}).(*dialog)
	if d == nil {
		return "", ErrNoAsker
	}
	if err := d.task.spend(); err != nil {
		return "", err
	}
	q := &question{id: NewID(), answer: make(chan reply, 1)}
	msg := NewMessage("agent", d.contextID, text)
	msg.Metadata = &Meta{Kind: KindQuestion, Question: q.id, Task: d.task.Budget()}
	d.all.mu.Lock()
	d.all.byQuestion[key(d.peer, q.id)] = d
	d.mu.Lock()
	d.open = q
	d.say(outcome{msg: &msg})
	d.mu.Unlock()
	d.all.mu.Unlock()

	timer := time.NewTimer(QuestionTime)
	defer timer.Stop()
	select {
	case r := <-q.answer:
		return r.text, r.err
	case <-ctx.Done():
		d.all.unanswered(d, q, ctx.Err())
	case <-timer.C:
		d.all.unanswered(d, q, ErrNoAnswer)
	}
	r := <-q.answer
	return r.text, r.err
}

// unanswered ends the wait for the answer to q, a question of d, with err:
// an answer that comes now finds no question. One that came just before is
// what the question is answered with.
func (a *dialogs) unanswered(d *dialog, q *question, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.open == q {
		d.open = nil
		delete(a.byQuestion, key(d.peer, q.id))
		q.answer <- reply{err: err}
	}
}
