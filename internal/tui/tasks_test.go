package tui

import (
	"strings"
	"testing"

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
	fadeAll(m, fade)
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	var list []string
	for _, l := range lines[1 : len(lines)-1] {
		list = append(list, strings.Trim(ansi.Cut(l, m.width-30, m.width), "│╭╮╰╯─ "))
	}
	if text := strings.Join(list, "\n"); !strings.Contains(text, "Tasks\ntask 1 · find the\ncallers\ntask 2 · read the tests") {
		t.Fatalf("the list:\n%s", text)
	}
	_, fade = m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	fadeAll(m, fade)
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
	for _, want := range []string{"◆ task 1 · find the callers ✓ 3s", "↳ Used 1 tool", "they call it twice", "◆ task 2 · read the tests ✗ it broke"} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q in:\n%s", want, view)
		}
	}
	if !c.entries[2].asks() || !c.entries[5].failed {
		t.Fatalf("entries %+v", c.entries)
	}
}
