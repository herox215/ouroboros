package a2a

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// A task is what an agent started when it asked a peer: the request, the
// questions the peer asks back, their answers, and what the peer asks of
// other peers on the way. All of it has one budget, which no message sets
// anew: MaxExchanges messages and TaskTime. It travels with every message
// and its answer, and each side takes the lower of what it knows and what
// it is told, so that a peer can spend a budget but not fill it.

const (
	// MaxExchanges is how many messages peers send one another for one
	// task: requests, questions back, answers and notes.
	MaxExchanges = 16
	// TaskTime is how long a task may take from its first message on.
	TaskTime = 30 * time.Minute
)

// ErrBudget is the error of a message that the budget of its task has no
// room for.
var ErrBudget = fmt.Errorf("a2a: the budget of this task between peers is used up (%d messages or %s, whichever comes first): "+
	"finish with what you have", MaxExchanges, TaskTime)

// Budget is what is left of the budget of a task, as a message tells it.
type Budget struct {
	ID    string    `json:"id"`
	Left  int       `json:"left"`  // messages
	Until time.Time `json:"until"` // when the task is over
}

// Task is the budget of a task as one of its agents knows it.
type Task struct {
	mu    sync.Mutex
	id    string
	left  int
	until time.Time
}

// NewTask returns the budget of a task that starts now.
func NewTask() *Task {
	return &Task{id: NewID(), left: MaxExchanges, until: time.Now().Add(TaskTime)}
}

// taskOf returns the task a request belongs to, as b tells it, and whether
// it has room for the request, which its sender counted. A request that
// tells no task starts one, of which it is the first message. What b says is
// taken as far as a task can have it: no more messages and no more time than
// a new one.
func taskOf(b *Budget) (*Task, bool) {
	t := NewTask()
	if b == nil {
		t.left--
		return t, true
	}
	t.id = b.ID
	t.heard(b)
	return t, b.Left >= 0 && time.Now().Before(t.until)
}

// ID names the task.
func (t *Task) ID() string { return t.id }

// Budget is what is left of the task.
func (t *Task) Budget() *Budget {
	t.mu.Lock()
	defer t.mu.Unlock()
	return &Budget{ID: t.id, Left: t.left, Until: t.until}
}

// spend takes one message from the budget. It fails with ErrBudget when
// none is left, or the time is over.
func (t *Task) spend() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.left <= 0 || !time.Now().Before(t.until) {
		return ErrBudget
	}
	t.left--
	return nil
}

// heard takes what a peer says is left of the task: the lower of it and what
// is known here. nil says nothing.
func (t *Task) heard(b *Budget) {
	if b == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.left = max(0, min(t.left, b.Left))
	if b.Until.Before(t.until) {
		t.until = b.Until
	}
}

type taskKey struct{}

// WithTask returns a context for the answer of an agent to its owner, under
// which what it sends to peers belongs to the task t. An agent that answers
// a peer has the task of the message it answers, see Origin.
func WithTask(ctx context.Context, t *Task) context.Context {
	return context.WithValue(ctx, taskKey{}, t)
}

// TaskFrom returns the task what is sent under ctx belongs to: the one of
// the message that is answered, or the one WithTask gave. nil if neither.
func TaskFrom(ctx context.Context) *Task {
	if origin, ok := OriginFrom(ctx); ok && origin.Task != nil {
		return origin.Task
	}
	t, _ := ctx.Value(taskKey{}).(*Task)
	return t
}
