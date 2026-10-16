package session

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/openai"
	"ouroboros/internal/workdir"
)

// task is a task the agent delegated that runs in the background of its
// session, past the answer that started it.
type task struct {
	n      int
	title  string
	cancel context.CancelFunc
}

// delegating runs the tasks the agent delegates in the session se, see
// assistant.Tasks.
type delegating struct {
	s  *Service
	se *session
}

// Start runs tasks in the background of the session: each keeps the node
// running as an answer does, ends with Cancel or Close, and once it ended,
// its report is the next message the agent takes, see deliver.
func (d delegating) Start(tasks []assistant.Task) ([]int, error) {
	s, se := d.s, d.se
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.node
	p.mu.Lock()
	defer p.mu.Unlock()
	if s.closed {
		return nil, ErrStopped
	}
	if running := len(se.tasks); running+len(tasks) > assistant.MaxDelegates {
		return nil, fmt.Errorf("%d tasks run in this conversation already, and at most %d run at once: start more once one ended",
			running, assistant.MaxDelegates)
	}
	started := make([]int, len(tasks))
	for i, t := range tasks {
		ctx, cancel := context.WithCancel(s.ctx)
		ctx = workdir.With(ctx, se.directory)
		ctx = openai.WithEffortOf(ctx, func() string { return s.effort(se) })
		se.delegated++
		running := &task{n: se.delegated, title: t.Title, cancel: cancel}
		se.tasks = append(se.tasks, running)
		started[i] = running.n
		p.answering++
		s.answers.Add(1)
		go s.run(ctx, se, running, t.Run)
	}
	s.emit(se, Event{Kind: Tasks, Text: taskLines(se.tasks)})
	return started, nil
}

// run does the task t of se with do, and tells the agent how it ended.
func (s *Service) run(ctx context.Context, se *session, t *task, do func(context.Context) (string, error)) {
	defer s.answers.Done()
	start := s.now()
	answer, err := do(ctx)
	stopped := ctx.Err() != nil
	t.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.node
	p.mu.Lock()
	defer p.mu.Unlock()
	se.tasks = slices.DeleteFunc(se.tasks, func(other *task) bool { return other == t })
	s.emit(se, Event{Kind: Tasks, Text: taskLines(se.tasks)})
	p.answering--
	// A task that was stopped is not reported: the owner stopped it, or
	// the node stops.
	if !stopped {
		s.deliver(se, p, t, answer, err, s.now().Sub(start))
	}
	p.settle()
}

// deliver hands the agent of se the report of the task t that ended with
// answer, or with err, after took: the answer on its way takes it as it
// takes a message of the owner, or one starts for it. Where the node takes
// no message, it waits for the next one. s.mu and p.mu are held.
func (s *Service) deliver(se *session, p *process, t *task, answer string, err error, took time.Duration) {
	e := &Event{Kind: Task, N: t.n, Remark: t.title, Text: answer, Took: took}
	if err != nil {
		e.Err = err.Error()
	}
	report := queued{text: assistant.TaskReport(t.n, t.title, answer, err), task: e}
	if se.cancel != nil || s.closed || p.successor != "" {
		se.queue = append(se.queue, report)
		return
	}
	s.begin(se, p, report)
}

// taskLines are the tasks as a session tells them, see the Event Tasks.
func taskLines(tasks []*task) string {
	lines := make([]string, len(tasks))
	for i, t := range tasks {
		lines[i] = fmt.Sprintf("task %d · %s", t.n, t.title)
	}
	return strings.Join(lines, "\n")
}
