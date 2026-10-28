package session

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The thread can tell the owner why a topic does not finish, without a new
// session to look: State says what a session does, Stop stops what hangs
// there, see threading.

// State tells what the session id, one of routable, does now: as the agent
// of the thread reads it, see assistant.ToolSessionState.
func (t threading) State(_ context.Context, id string) (string, error) {
	s := t.s
	s.mu.Lock()
	defer s.mu.Unlock()
	id = s.resolve(id)
	if id == ThreadID || !s.routableID(id) {
		return "", fmt.Errorf("no session %q among those of the last 24 hours", id)
	}
	s.handed(id)
	return s.state(s.get(id)), nil
}

// Stop stops in the session id the background job job, or with job 0 the
// answer on its way with its tasks, see assistant.ToolSessionStop.
func (t threading) Stop(ctx context.Context, id string, job int) (string, error) {
	s := t.s
	s.mu.Lock()
	id = s.resolve(id)
	ok := id != ThreadID && s.routableID(id)
	var running bool
	if ok {
		se := s.get(id)
		running = se.cancel != nil || len(se.tasks) > 0
		s.handed(id)
	}
	s.mu.Unlock()
	switch {
	case !ok:
		return "", fmt.Errorf("no session %q among those of the last 24 hours", id)
	case job != 0:
		if err := s.StopJob(ctx, id, job); err != nil {
			return "", err
		}
		return fmt.Sprintf("Job %d stopped: the answer goes on without it.", job), nil
	case !running:
		return "Nothing runs in the session to stop.", nil
	}
	s.Cancel(ctx, id) // fails only for an invalid ID, and a routable one is valid
	return "The answer on its way is stopped, with its tasks; what was queued for it is answered next.", nil
}

// routableID reports whether id is of the sessions the thread may hand
// messages to. s.mu is held.
func (s *Service) routableID(id string) bool {
	for _, r := range s.routable() {
		if r.id == id {
			return true
		}
	}
	return false
}

// state is what se does now, in lines for the agent of the thread. s.mu is
// held.
func (s *Service) state(se *session) string {
	now := s.now()
	ago := func(at time.Time) string { return now.Sub(at).Round(time.Second).String() + " ago" }
	var b strings.Builder
	fmt.Fprintf(&b, "Session %s: %s\n", handle(se.id), strconv.Quote(oneLine(cmp.Or(se.title, firstMessage(se.events)))))
	if se.cancel == nil {
		b.WriteString("No answer is on its way: it is idle and takes the next message at once.\n")
	} else {
		fmt.Fprintf(&b, "An answer is on its way, begun %s.\n", ago(se.since))
	}
	if q := awaited(se.events); q != "" && se.asked != nil {
		fmt.Fprintf(&b, "It waits for the owner to answer: %s\n", strconv.Quote(oneLine(q)))
	}
	if se.cancel != nil {
		open, order := map[string]Event{}, []string{}
		for _, e := range se.events {
			if e.Kind != Step || e.At.Before(se.since) {
				continue
			}
			key := cmp.Or(e.CallID, e.Tool)
			if e.Done {
				delete(open, key)
				continue
			}
			if _, ok := open[key]; !ok {
				order = append(order, key)
			}
			open[key] = e
		}
		for _, key := range order {
			if e, ok := open[key]; ok {
				fmt.Fprintf(&b, "Tool %s runs, called %s, with %s\n", e.Tool, ago(e.At), clip(oneLine(string(e.Args)), 200))
			}
		}
		if len(order) == 0 || len(open) == 0 {
			if last := lastAlong(se.events, se.since); !last.At.IsZero() {
				fmt.Fprintf(&b, "No tool runs: the model thinks or writes; the last thing it did was %s, %s.\n", last.Kind, ago(last.At))
			}
		}
	}
	for _, j := range s.jobs[se.id].jobs {
		fmt.Fprintf(&b, "Background job %d runs since %s: %s\n", j.N, ago(j.Since), clip(j.Command, 200))
	}
	for _, t := range se.tasks {
		fmt.Fprintf(&b, "Delegated task %d runs: %s\n", t.n, strconv.Quote(oneLine(t.title)))
	}
	if len(se.queue) > 0 {
		fmt.Fprintf(&b, "%d message(s) wait for the answer on its way to take them, at its next step or once it ends:\n", len(se.queue))
		for _, m := range se.queue {
			switch {
			case m.task != nil:
				b.WriteString("- the report of a delegated task\n")
			case m.generated:
				b.WriteString("- an internal result\n")
			default:
				fmt.Fprintf(&b, "- %s\n", strconv.Quote(clip(oneLine(m.text), 200)))
			}
		}
	}
	for i := len(se.events) - 1; i >= 0; i-- {
		e := se.events[i]
		switch e.Kind {
		case Answer:
			fmt.Fprintf(&b, "Its last answer ended %s.\n", ago(e.At))
		case Failed:
			fmt.Fprintf(&b, "Its last answer failed %s: %s\n", ago(e.At), clip(oneLine(e.Text), 300))
		case Canceled:
			fmt.Fprintf(&b, "Its last answer was stopped %s.\n", ago(e.At))
		default:
			continue
		}
		break
	}
	return strings.TrimRight(b.String(), "\n")
}

// lastAlong is the last Event of what the answer begun at since did on its
// way, zero for none.
func lastAlong(events []Event, since time.Time) Event {
	for i := len(events) - 1; i >= 0 && !events[i].At.Before(since); i-- {
		switch events[i].Kind {
		case Step, Say, Thought, Asked, Task:
			return events[i]
		}
	}
	return Event{}
}

// clip is text cut to at most n bytes, on a rune boundary.
func clip(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n] + "…"
}
