package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// threadModel is the UI of a fakeBackend as it opens, in the thread, without
// the splash, with the start directory, if there is one, and the backend for
// directories b.
func threadModel(t *testing.T, directory string, err error) (*Model, *directoryFake) {
	t.Helper()
	b := &directoryFake{Backend: newBackend(t), err: err}
	m := New(b, nil, nil)
	t.Cleanup(m.quit)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.splash.show = false
	m.Update(m.refreshStatus())
	m.SetInitialDirectory(directory)
	return m, b
}

// A UI shows the thread first, which takes the start directory of the UI as
// its project. /new starts a conversation of its own, and /thread goes back
// to the thread, which keeps the project it has. It is no session of the
// list.
func TestTheThreadIsShownFirst(t *testing.T) {
	m, b := threadModel(t, "/start", nil)
	if m.chat.contextID != plugin.ThreadID || m.chat.directory != "/start" {
		t.Fatalf("shown %q in %q", m.chat.contextID, m.chat.directory)
	}
	settle(m, m.watch(m.chat))
	if b.calls != 1 || b.id != plugin.ThreadID || b.path != "/start" || m.notice != "" {
		t.Fatalf("directory %+v, notice %q", b, m.notice)
	}
	if header := ansi.Strip(m.headerView()); !strings.Contains(header, "· thread") || !strings.HasSuffix(strings.TrimSpace(header), "· effort low") {
		t.Fatalf("header %q", header)
	}
	if cmd := run(m, "/thread"); cmd != nil || m.notice != "this is the thread: say what is to be done" {
		t.Fatalf("again: %q", m.notice)
	}
	thread := m.chat
	m.input.SetValue("draft")
	settle(m, m.newConversation())
	if m.chat == thread || b.calls != 2 || b.id != m.chat.contextID {
		t.Fatalf("shown %q, directory set %d times, last of %q", m.chat.contextID, b.calls, b.id)
	}
	settle(m, run(m, "/thread"))
	if m.chat != thread || b.calls != 2 || m.notice != "back in the thread" || m.input.Value() != "draft" {
		t.Fatalf("shown %q, directory set %d times, notice %q, input %q", m.chat.contextID, b.calls, m.notice, m.input.Value())
	}
	m.chat.title = "fix the login" // as a message would name it
	m.openSessions()
	for _, c := range m.sessions.all(m) {
		if c.contextID == plugin.ThreadID {
			t.Fatal("the thread is listed among the sessions")
		}
	}
}

// A thread that cannot take the start directory, as it answers, is shown
// with the project it has, and says so; without a start directory none is
// set. Shown again after a session the UI started in, it takes it then.
func TestThreadKeepsItsProject(t *testing.T) {
	m, _ := threadModel(t, "/start", errors.New("wait for the answer first"))
	settle(m, m.watch(m.chat))
	if !m.chat.watching || m.notice != "the thread keeps its project: wait for the answer first" || !m.noticeErr {
		t.Fatalf("watching %v, notice %q", m.chat.watching, m.notice)
	}
	m, b := threadModel(t, "", nil)
	settle(m, m.watch(m.chat))
	if b.calls != 0 || !m.chat.watching {
		t.Fatalf("directory set %d times", b.calls)
	}
	m, b = threadModel(t, "/start", nil)
	m.Resume("s1")
	settle(m, m.watch(m.chat))
	settle(m, run(m, "/thread"))
	if m.chat.contextID != plugin.ThreadID || b.calls != 1 || b.id != plugin.ThreadID {
		t.Fatalf("shown %q, directory set %d times, last of %q", m.chat.contextID, b.calls, b.id)
	}
}

// While anything works for the thread, it is thinking, also between the
// answers that come: they are shown as its answers. What the thread answers
// while another session is shown is told in the footer.
func TestThreadStaysAvailableWhileTopicsWork(t *testing.T) {
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
	if thread.pending || m.waiting() != "" {
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

// The thread shortcut preserves both drafts and marks the thread in the header.
func TestThreadShortcutAndHighlight(t *testing.T) {
	m, _ := threadModel(t, "", nil)
	thread := m.chat
	m.input.SetValue("thread draft")
	settle(m, m.newConversation())
	topic := m.chat
	m.input.SetValue("topic draft")
	badge := boldStyle.Foreground(lipgloss.Color("0")).Background(lipgloss.Color("7")).Render(" thread ")
	if strings.Contains(m.headerView(), badge) {
		t.Fatal("a topic is marked as the thread")
	}
	settle(m, m.handleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}))
	if m.chat != thread || m.input.Value() != "thread draft" || m.thoughtsHidden {
		t.Fatalf("thread shortcut: chat %q, draft %q, thoughts hidden %v", m.chat.contextID, m.input.Value(), m.thoughtsHidden)
	}
	if !strings.Contains(m.headerView(), badge) {
		t.Fatalf("thread lacks highlight: %q", m.headerView())
	}
	settle(m, m.switchTo(topic))
	if m.input.Value() != "topic draft" {
		t.Fatalf("topic draft lost: %q", m.input.Value())
	}
}
