package tui

import (
	"cmp"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

type directoryFake struct {
	Backend
	ctx      context.Context
	id, path string
	calls    int
	err      error
}

func (b *directoryFake) SetSessionDirectory(ctx context.Context, id, path string) error {
	b.ctx, b.id, b.path = ctx, id, path
	b.calls++
	return b.err
}

func TestDirectoryCommandArguments(t *testing.T) {
	for _, tc := range []struct{ line, path string }{
		{"/cd /tmp/project", "/tmp/project"},
		{"/cd  ../my project  ", "../my project"},
		{"/cd	/tmp/my  project", "/tmp/my  project"},
		{"/cd '/tmp/my project'", "/tmp/my project"},
		{"/cd \"/tmp/my project\"", "/tmp/my project"},
		{"/cd --workspace", ""},
		{"/cd '--workspace'", "--workspace"},
	} {
		t.Run(tc.line, func(t *testing.T) {
			m, original := newModel(t)
			b := &directoryFake{Backend: original}
			m.backend = b
			cmd := run(m, tc.line)
			if cmd == nil {
				t.Fatalf("no command: %s", m.notice)
			}
			msg := cmd().(actionMsg)
			if msg.err != nil || b.calls != 1 || b.ctx != m.ctx || b.id != m.chat.contextID || b.path != tc.path {
				t.Fatalf("call %+v, result %+v", b, msg)
			}
			if m.input.Value() != "" || m.chat.directory != "" {
				t.Fatal("command should clear input and wait for the canonical directory event")
			}
		})
	}
}

func TestDirectoryCommandShowAndErrors(t *testing.T) {
	m, original := newModel(t)
	// A legacy backend can still show the path and use all other commands.
	// With nothing after the command, as with a space, it is shown too.
	for _, line := range []string{"/cd", "/cd "} {
		for _, path := range []string{"", "/tmp/my project"} {
			m.chat.directory = path
			if cmd := run(m, line); cmd != nil || m.noticeErr || m.notice != "directory: "+cmp.Or(path, "workspace") || m.input.Value() != "" {
				t.Fatalf("show %q: %q, input %q", line, m.notice, m.input.Value())
			}
		}
	}
	if cmd := run(m, "/cd /tmp"); cmd != nil || !m.noticeErr {
		t.Fatal("legacy backend accepted a directory change")
	}
	b := &directoryFake{Backend: original}
	m.backend = b
	unclosed, empty := "/cd: close the quote around the path", "/cd: use --workspace to reset the directory"
	for line, notice := range map[string]string{"/cd 'unfinished": unclosed, "/cd \"unfinished'": unclosed, "/cd '": unclosed, "/cd \"": unclosed,
		"/cd ''": empty, "/cd \"\"": empty} {
		if cmd := run(m, line); cmd != nil || !m.noticeErr || m.notice != notice || m.input.Value() != line {
			t.Fatalf("invalid %q: notice %q, input %q", line, m.notice, m.input.Value())
		}
	}
	m.chat.Peer = "peer"
	if cmd := run(m, "/cd /tmp"); cmd != nil || !m.noticeErr || b.calls != 0 {
		t.Fatal("changed a peer conversation")
	}
	m.chat.Peer = ""
	m.chat.Pending, m.chat.Running = true, true
	b.err = plugin.ErrBusy
	cmd := run(m, "/cd /tmp")
	if cmd == nil {
		t.Fatal("busy rejection belongs to the backend")
	}
	msg := cmd().(actionMsg)
	if !errors.Is(msg.err, plugin.ErrBusy) || b.calls != 1 {
		t.Fatalf("busy: %+v", msg)
	}
	m.Update(msg)
	if !m.noticeErr || m.notice != plugin.ErrBusy.Error() || m.chat.directory != "/tmp/my project" {
		t.Fatalf("busy changed directory: %q, %q", m.notice, m.chat.directory)
	}
}

func TestDirectoryEventsAndVisiblePath(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.replay(c, []Event{{Kind: "directory", Text: "/tmp/old"}, {Kind: "directory", Text: "/tmp/my project"}})
	if c.directory != "/tmp/my project" || len(c.entries) != 0 {
		t.Fatalf("replay: %+v", c)
	}
	for _, view := range []string{m.headerView(), m.View().Content} {
		if !strings.Contains(ansi.Strip(view), c.directory) {
			t.Fatalf("path not visible: %s", view)
		}
	}
	other := m.conversation("other")
	m.event(eventMsg{id: other.contextID, event: Event{Kind: "directory", Text: "/tmp/other"}})
	if other.directory != "/tmp/other" || strings.Contains(ansi.Strip(m.headerView()), "/tmp/other") {
		t.Fatal("background directory leaked into the header")
	}
	m.event(eventMsg{id: c.contextID, event: Event{Kind: "directory", Text: "/tmp/live"}})
	if c.directory != "/tmp/live" || !strings.Contains(ansi.Strip(m.headerView()), "/tmp/live") {
		t.Fatal("live path not visible")
	}
	m.event(eventMsg{id: c.contextID, event: Event{Kind: "directory"}})
	if c.directory != "" || strings.Contains(ansi.Strip(m.headerView()), "/tmp/live") {
		t.Fatal("workspace reset did not clear path")
	}
	m.replay(c, []Event{{Kind: "directory", Text: "/tmp/old"}, {Kind: "directory"}})
	if c.directory != "" {
		t.Fatal("replay did not reset to workspace")
	}
	c.directory = "/tmp/stale"
	m.replay(c, nil)
	if c.directory != "" {
		t.Fatal("replay without directory retained stale state")
	}
	if matches := m.matching("/cd"); len(matches) != 1 || matches[0].name != "cd" {
		t.Fatal("missing command completion")
	}
	if matches := m.matching("/cd /tmp"); len(matches) != 0 {
		t.Fatal("path completion is not a command suggestion")
	}
}
