package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/openai"
	"ouroboros/internal/workdir"
)

// task is a task the agent delegated that runs in the background of its
// session, past the answer that started it: the session takes messages
// meanwhile, and the agent reaches the task with Tell and Stop.
type task struct {
	n       int
	title   string
	cancel  context.CancelFunc
	told    []openai.Message // what the agent told its helper, for it to take
	stopped bool             // by the agent, with Stop: reported as stopped
}

// errStoppedByAgent is why a task the agent stopped ended, as its report
// tells it.
var errStoppedByAgent = errors.New("stopped by you with " + assistant.ToolTaskStop)

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
		se.delegated++
		running := &task{n: se.delegated, title: t.Title, cancel: cancel}
		ctx = workdir.With(ctx, se.directory)
		ctx = openai.WithEffortOf(ctx, func() string { return s.effort(se) })
		ctx = assistant.WithTaskSteering(ctx, func() []openai.Message { return s.told(running) })
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
	// the node stops. One the agent stopped is, so that it knows.
	switch {
	case t.stopped:
		s.deliver(se, p, t, "", errStoppedByAgent, s.now().Sub(start))
	case !stopped:
		s.deliver(se, p, t, answer, err, s.now().Sub(start))
	}
	p.settle()
}

// runningTask is the task n that runs in se, nil if none does. s.mu is held.
func runningTask(se *session, n int) *task {
	at := slices.IndexFunc(se.tasks, func(t *task) bool { return t.n == n })
	if at < 0 {
		return nil
	}
	return se.tasks[at]
}

// errNotRunning is the error of Stop and Tell for the task n that does not
// run.
func errNotRunning(n int) error {
	return fmt.Errorf("task %d does not run: it ended, or never started", n)
}

// Stop stops the task n: its report says so.
func (d delegating) Stop(n int) error {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	t := runningTask(d.se, n)
	if t == nil {
		return errNotRunning(n)
	}
	t.stopped = true
	t.cancel()
	return nil
}

// Tell gives the helper of the task n text, which it takes after the tools
// of its round ran, as an answer takes what the owner says on the way.
func (d delegating) Tell(n int, text string) error {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	t := runningTask(d.se, n)
	if t == nil {
		return errNotRunning(n)
	}
	t.told = append(t.told, openai.Message{Role: "user", Content: text})
	return nil
}

// told takes what the agent told the helper of t since the last time.
func (s *Service) told(t *task) []openai.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	told := t.told
	t.told = nil
	return told
}

// ready takes of the queue of se what an answer takes now: all of it, but
// while tasks run, the reports of those that ended wait for the last, so
// that the agent takes them together. s.mu is held.
func ready(se *session) []queued {
	if len(se.tasks) == 0 {
		messages := se.queue
		se.queue = nil
		return messages
	}
	var now, later []queued
	for _, m := range se.queue {
		if m.task != nil {
			later = append(later, m)
		} else {
			now = append(now, m)
		}
	}
	se.queue = later
	return now
}

// deliver hands the agent of se the report of the task t that ended with
// answer, or with err, after took: the answer on its way takes it as it
// takes a message of the owner, or one starts for it, once no other task
// runs, with the reports of the others. Where the node takes no message, it
// waits for the next one. s.mu and p.mu are held.
func (s *Service) deliver(se *session, p *process, t *task, answer string, err error, took time.Duration) {
	e := &Event{Kind: Task, N: t.n, Remark: t.title, Text: answer, Took: took}
	if err != nil {
		e.Err = err.Error()
	}
	report := queued{text: assistant.TaskReport(t.n, t.title, answer, err), task: e}
	se.queue = append(se.queue, report)
	if se.cancel != nil || s.closed || p.successor != "" || len(se.tasks) > 0 {
		return
	}
	s.begin(se, p, ready(se)...)
}

// taskLines are the tasks as a session tells them, see the Event Tasks.
func taskLines(tasks []*task) string {
	lines := make([]string, len(tasks))
	for i, t := range tasks {
		lines[i] = fmt.Sprintf("task %d · %s", t.n, t.title)
	}
	return strings.Join(lines, "\n")
}
