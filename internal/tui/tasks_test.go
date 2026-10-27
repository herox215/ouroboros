package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The tasks the agent delegated are shown while they run, also once the
// answer that started them ended: in the line of the chat, with what esc
// does then, and in the wait of an answer. esc stops them, and the list of
// the jobs lists them.
func TestTasksAreShown(t *testing.T) {
	m, b := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "look"}, true)
	m.apply(c, Event{Kind: plugin.SessionTasks, Text: "task 1 · find the callers\ntask 2 · read the tests"}, true)
	if got := m.waiting(); got != "alpha is thinking · task 1 · find the callers · task 2 · read the tests" {
		t.Fatalf("the wait: %q", got)
	}
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "started them"}, true)
	if got := ansi.Strip(m.chatTitle()); !strings.Contains(got, "2 tasks run · esc stops") {
		t.Fatalf("the line of the chat: %q", got)
	}
	_, fade := m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	loaded(m, fade)
	var list []string
	for _, l := range strings.Split(ansi.Strip(m.railView(m.railWidth(), m.height)), "\n") {
		list = append(list, strings.TrimSpace(l))
	}
	if text := strings.Join(list, "\n"); !strings.Contains(text, "TASKS\ntask 1 · find the\ncallers\ntask 2 · read the\ntests") {
		t.Fatalf("the list:\n%s", text)
	}
	_, fade = m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	loaded(m, fade)
	cmd := press(m, "esc")
	if cmd == nil || m.notice != "stopping the tasks…" {
		t.Fatalf("esc: %q", m.notice)
	}
	if msg, ok := cmd().(actionMsg); !ok || msg.done != "tasks stopped" || len(b.canceled) != 1 {
		t.Fatalf("stopped %+v, %v", msg, b.canceled)
	}
	m.apply(c, Event{Kind: plugin.SessionTasks, Text: "task 2 · read the tests"}, true)
	if got := ansi.Strip(m.chatTitle()); !strings.Contains(got, "1 task runs") {
		t.Fatalf("one left: %q", got)
	}
	// With an answer on its way, esc stops both.
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "more"}, true)
	if press(m, "esc"); m.notice != "stopping the answer and the tasks…" {
		t.Fatalf("both: %q", m.notice)
	}
	m.apply(c, Event{Kind: plugin.SessionTasks}, true)
	if c.tasks != nil || strings.Contains(m.chatTitle(), "task") {
		t.Fatalf("none left: %q, %q", c.tasks, m.chatTitle())
	}
}

// The report of a task is a line of its own in the chat, and an answer to
// it starts as one to a message of the owner does: the calls of its rounds
// are summed up below it.
func TestTaskReportIsShown(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "look"}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "started"}, true)
	m.apply(c, Event{Kind: plugin.SessionTask, Text: "task 1 · find the callers ✓ 3s"}, true)
	if !c.pending || !c.running {
		t.Fatal("no answer is on its way for the report")
	}
	m.apply(c, Event{Kind: plugin.SessionStep, Tool: "read", Text: "reading a file"}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "they call it twice"}, true)
	m.apply(c, Event{Kind: plugin.SessionTask, Text: "task 2 · read the tests ✗ it broke", Failed: true}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "it failed"}, true)
	m.renderHistory(false)
	view := ansi.Strip(m.history.View())
	for _, want := range []string{"↳ TASK 1 · FIND THE CALLERS ✓ 3S", "│ ↳ Used 1 tool", "they call it twice", "↳ TASK 2 · READ THE TESTS ✗ IT BROKE"} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q in:\n%s", want, view)
		}
	}
	if !c.entries[2].asks() || !c.entries[5].failed {
		t.Fatalf("entries %+v", c.entries)
	}
}

// A session that waits for its tasks is at work, though no answer is on its
// way: it stays in the columns beside the conversation and says that it
// waits, and each task says since when it runs, kept while they come and go.
func TestTheWaitForTasksCounts(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	start := time.Unix(1000, 0)
	at := start
	m.now = func() time.Time { return at }
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "look"}, true)
	m.apply(c, Event{Kind: plugin.SessionTasks, Text: "task 1 · find the callers\ntask 2 · read the tests", At: at}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "started them", At: at}, true)
	if !c.busy() || c.pending || c.running {
		t.Fatalf("busy %v, pending %v, running %v", c.busy(), c.pending, c.running)
	}
	if list := m.runningSessions(); len(list) != 1 || list[0] != c {
		t.Fatalf("a session that waits for its tasks is not beside the conversation: %v", list)
	}
	var state string
	for _, r := range m.sidebarRows(34) {
		if strings.Contains(strip(r.text), "State:") {
			state = strip(r.text)
		}
	}
	if !strings.Contains(state, "State: waiting · own") {
		t.Fatalf("the sidebar: %q", state)
	}
	at = start.Add(90 * time.Second)
	details := strings.Join(m.artefact(c, 40), "\n")
	for _, want := range []string{"● waits for 2 tasks", "1m30s", "task 1 find the callers", "task 2 read the tests", "next  their reports, then the answer"} {
		if !strings.Contains(strip(details), want) {
			t.Fatalf("no %q beside the conversation:\n%s", want, strip(details))
		}
	}
	// A task that ran before keeps the moment it began; one that comes now
	// begins now.
	at = start.Add(2 * time.Minute)
	m.apply(c, Event{Kind: plugin.SessionTasks, Text: "task 2 · read the tests\ntask 3 · run the suite", At: at}, true)
	if since, ok := c.oldestTask(); !ok || !since.Equal(start) {
		t.Fatalf("the oldest task began at %v, from %v", since, start)
	}
	if began := c.taskSince[3]; !began.Equal(at) {
		t.Fatalf("task 3 began at %v, not at %v", began, at)
	}
	// Once no task is left, the session is not at work anymore.
	m.apply(c, Event{Kind: plugin.SessionTasks, At: at}, true)
	if c.busy() || len(m.runningSessions()) != 0 {
		t.Fatalf("busy %v with %d sessions beside", c.busy(), len(m.runningSessions()))
	}
}

// A session whose tasks ended with the node says so in the conversation, and
// the tasks it waited for are cleared beside it: the owner is shown that the
// work is gone.
func TestTasksEndedWithTheNodeAreShown(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "look"}, true)
	m.apply(c, Event{Kind: plugin.SessionTasks, Text: "task 1 · find the callers"}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "started"}, true)
	note := "[Not from the owner: ouro started anew, and the tasks this conversation delegated ended with it before they could report. Their work is lost:\ntask 1 · find the callers\n\nDelegate them again if their work is needed.]"
	m.apply(c, Event{Kind: plugin.SessionTasks}, false)
	if c.busy() || len(c.tasks) != 0 {
		t.Fatalf("busy %v with the tasks %q", c.busy(), c.tasks)
	}
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: note}, false)
	if n := len(c.entries); n == 0 || c.entries[n-1].text != note {
		t.Fatalf("the conversation does not end with the note: %+v", c.entries)
	}
}
