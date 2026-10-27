package matrix

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ouroboros/plugin"
)

// A room says where the conversation works and moves it: /project and /cd
// answer with the project of the thread, /cd PATH changes it, /cd --workspace
// returns to the profile workspace, and a path that is no directory, a
// conversation that is busy and an unclosed quote change nothing. None of it
// is a message to the agent.
func TestRoomShowsAndChangesTheProject(t *testing.T) {
	s := store{t.TempDir()}
	cfg := configuration{Enabled: true, Token: "token", Homeserver: "https://example.org", UserID: "@bot:example.org", AllowedUsers: []string{"@owner:example.org"}, Revision: "1"}
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	sessions := &testSessions{}
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	conn := &outputConnection{output: make(chan string, 8)}
	msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org"}
	reply := func(text string) string {
		t.Helper()
		msg.Text = text
		if err := c.receive(context.Background(), conn, msg); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		select {
		case got := <-conn.output:
			return got
		default:
			t.Fatalf("%q sent nothing", text)
			return ""
		}
	}

	if got := reply("/project"); !strings.HasPrefix(got, "No project:") {
		t.Fatalf("a conversation without a project: %q", got)
	}
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionDirectory, Text: "/home/owner/project"})
	for _, command := range []string{"/project", "/cd"} {
		if got := reply(command); got != "Project: /home/owner/project" {
			t.Fatalf("%s: %q", command, got)
		}
	}
	if got := reply(`/cd "/tmp/other project"`); got != "Project: /tmp/other project" {
		t.Fatalf("a quoted path: %q", got)
	}
	if got := reply("/cd --workspace"); !strings.HasPrefix(got, "No project:") {
		t.Fatalf("reset: %q", got)
	}

	sessions.dirErr = plugin.ErrBusy
	if got := reply("/cd /tmp/next"); !strings.Contains(got, "an answer is in progress") {
		t.Fatalf("a busy conversation: %q", got)
	}
	sessions.dirErr = errors.New("/tmp/nope: no such file or directory")
	if got := reply("/cd /tmp/nope"); !strings.Contains(got, "did not change: /tmp/nope") {
		t.Fatalf("a path that is no directory: %q", got)
	}
	sessions.dirErr = nil
	before := len(sessions.directories)
	if got := reply(`/cd " /tmp/open`); !strings.Contains(got, "close the quote") {
		t.Fatalf("an unclosed quote: %q", got)
	}
	if len(sessions.directories) != before {
		t.Fatalf("an unclosed quote changed the project: %v", sessions.directories)
	}

	// Where the conversation works is no message to the agent.
	if sessions.asks != 0 || sessions.created != 0 {
		t.Fatalf("the agent was asked: %+v", sessions)
	}
}
