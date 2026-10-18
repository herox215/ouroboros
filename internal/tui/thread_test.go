package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// /thread shows the thread, which takes the start directory of the UI as
// its project the first time the UI shows it. It is no session of the list.
func TestThreadCommand(t *testing.T) {
	m, original := newModel(t)
	b := &directoryFake{Backend: original}
	m.backend = b
	m.SetInitialDirectory("/start")
	shown := m.chat
	m.input.SetValue("draft")
	cmd := run(m, "/thread")
	if m.input.Value() != "" {
		m.input.SetValue("") // the command line, not what was typed before it
	}
	if m.chat.contextID != plugin.ThreadID || m.screen != screenChat || m.notice != "the thread: say what is to be done, ouro hands it to its session" {
		t.Fatalf("shown %q, notice %q", m.chat.contextID, m.notice)
	}
	settle(m, cmd)
	if b.calls != 1 || b.id != plugin.ThreadID || b.path != "/start" || m.notice != "the thread: what is new starts in /start" {
		t.Fatalf("directory %+v, notice %q", b, m.notice)
	}
	if header := ansi.Strip(m.headerView()); !strings.Contains(header, "· thread") || !strings.HasSuffix(strings.TrimSpace(header), "· effort low") {
		t.Fatalf("header %q", header)
	}
	if cmd := run(m, "/thread"); cmd != nil || m.notice != "this is the thread: say what is to be done" {
		t.Fatalf("again: %q", m.notice)
	}

	// Shown again, it keeps the project it has: the directory set last is
	// that of the conversation shown before, which is new.
	settle(m, m.switchTo(shown))
	settle(m, run(m, "/thread"))
	if b.calls != 2 || b.id != shown.contextID || m.chat.contextID != plugin.ThreadID {
		t.Fatalf("directory set %d times, last of %q, shown %q", b.calls, b.id, m.chat.contextID)
	}
	m.chat.title = "fix the login" // as a message would name it
	m.openSessions()
	for _, c := range m.sessions.all(m) {
		if c.contextID == plugin.ThreadID {
			t.Fatal("the thread is listed among the sessions")
		}
	}
}

// A thread that cannot take the start directory keeps its project, and
// says so; without a start directory, or a backend for directories, none is
// set.
func TestThreadKeepsItsProject(t *testing.T) {
	m, original := newModel(t)
	b := &directoryFake{Backend: original, err: errors.New("wait for the answer first")}
	m.backend = b
	m.SetInitialDirectory("/start")
	settle(m, run(m, "/thread"))
	if m.notice != "the thread keeps its project: wait for the answer first" || !m.noticeErr {
		t.Fatalf("notice %q", m.notice)
	}

	m, original = newModel(t)
	b = &directoryFake{Backend: original}
	m.backend = b
	settle(m, run(m, "/thread"))
	m, _ = newModel(t)
	m.SetInitialDirectory("/start")
	settle(m, run(m, "/thread"))
	if b.calls != 0 || m.chat.contextID != plugin.ThreadID {
		t.Fatalf("directory set %d times", b.calls)
	}
}

// While anything works for the thread, it is thinking, also between the
// answers that come: they are shown as its answers. What the thread answers
// while another session is shown is told in the footer.
func TestThreadThinksWhileItWorks(t *testing.T) {
	m, _ := newModel(t)
	thread := m.conversation(plugin.ThreadID)
	m.apply(thread, Event{Kind: plugin.SessionMessage, Text: "fix the login, and sort the photos"}, true)
	m.apply(thread, Event{Kind: plugin.SessionWorking, N: 2}, true)
	m.apply(thread, Event{Kind: plugin.SessionAnswer, Text: "the login is fixed"}, true)
	if m.notice != "answered in the thread · /thread" {
		t.Fatalf("notice %q", m.notice)
	}
	m.apply(thread, Event{Kind: plugin.SessionWorking, N: 1}, true)
	m.chat = thread
	m.renderHistory(true)
	if !thread.pending || m.waiting() != "alpha is thinking" {
		t.Fatalf("pending %v, waiting %q", thread.pending, m.waiting())
	}
	m.apply(thread, Event{Kind: plugin.SessionAnswer, Text: "the photos are sorted"}, true)
	m.apply(thread, Event{Kind: plugin.SessionWorking}, true)
	if thread.pending || thread.running || m.waiting() != "" {
		t.Fatalf("pending %v, waiting %q", thread.pending, m.waiting())
	}
	m.renderHistory(true)
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"fix the login, and sort the photos", "the login is fixed", "the photos are sorted"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the chat lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "session") {
		t.Fatalf("the chat tells of sessions:\n%s", view)
	}
}

// What comes of a message the thread handed on is shown in the thread, so
// the footer does not tell of it as of an answer in another session.
func TestRoutedAnswersAreNotTold(t *testing.T) {
	m, _ := newModel(t)
	c := m.conversation("s1")
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "fix the login", Routed: true}, true)
	m.apply(c, Event{Kind: plugin.SessionAsked, Text: "Which?"}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "fixed"}, true)
	if m.notice != "" {
		t.Fatalf("notice %q", m.notice)
	}
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "and now?"}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "done"}, true)
	if m.notice != "answered in “fix the login” · /session" {
		t.Fatalf("notice %q", m.notice)
	}
}

// A question of one session waits on in the thread while the answer of
// another comes in, and enter still answers it.
func TestThreadQuestionOutlivesAnAnswer(t *testing.T) {
	m, b := newModel(t)
	thread := m.conversation(plugin.ThreadID)
	m.chat = thread
	m.apply(thread, Event{Kind: plugin.SessionMessage, Text: "fix the login, and sort the photos"}, true)
	m.apply(thread, Event{Kind: plugin.SessionWorking, N: 2}, true)
	m.apply(thread, Event{Kind: plugin.SessionAsked, Text: "Which folder?", Options: []string{"Pictures", "Documents"}}, true)
	m.apply(thread, Event{Kind: plugin.SessionAnswer, Text: "the login is fixed"}, true)
	m.apply(thread, Event{Kind: plugin.SessionWorking, N: 1}, true)
	if thread.asking == nil || m.waiting() != "alpha waits for your answer" {
		t.Fatalf("the question is gone: %q", m.waiting())
	}
	cmd := press(m, "enter")
	if cmd == nil {
		t.Fatal("enter answers nothing")
	}
	cmd()
	if len(b.sent) != 0 || !thread.asking.sent {
		t.Fatalf("sent %+v", b.sent)
	}
}
