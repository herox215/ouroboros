package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/plugin"
)

// testSessions are the sessions of the owner. Ask fails with askErr and Watch
// with watchErr; asked, when set, gets the text of every Ask.
type testSessions struct {
	plugin.Sessions
	mu                              sync.Mutex
	created, asks, replies, cancels int
	events                          []plugin.SessionEvent
	askErr, watchErr                error
	asked                           chan string
}

func (s *testSessions) New(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created++
	return fmt.Sprintf("gateway-session-%d", s.created), nil
}
func (s *testSessions) Ask(_ context.Context, _, text string) error {
	s.mu.Lock()
	s.asks++
	err, asked := s.askErr, s.asked
	s.mu.Unlock()
	if asked != nil {
		asked <- text
	}
	return err
}
func (s *testSessions) Steer(context.Context, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies++
	return nil
}
func (s *testSessions) Cancel(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancels++
	return nil
}
func (s *testSessions) Watch(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watchErr != nil {
		return nil, nil, s.watchErr
	}
	return append([]plugin.SessionEvent(nil), s.events...), nil, nil
}

func (s *testSessions) set(events ...plugin.SessionEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = events
}

type outputConnection struct {
	fakeConnection
	output chan string
}

func (o *outputConnection) Send(ctx context.Context, _ string, text string) error {
	select {
	case o.output <- text:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *outputConnection) SendTo(ctx context.Context, room, peer, text string) error {
	if peer != "@owner:example.org" {
		return errors.New("wrong recipient")
	}
	return o.Send(ctx, room, text)
}

func TestConversationAuthorizationPersistenceAndPresentation(t *testing.T) {
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
	msg := incoming{RoomID: "!room:example.org", Sender: "@stranger:example.org", Text: "must not run"}
	if err = c.receive(context.Background(), conn, msg); !errors.Is(err, plugin.ErrGatewayDenied) {
		t.Fatalf("unauthorized: %v", err)
	}
	if sessions.created != 0 {
		t.Fatal("unauthorized user created a session")
	}
	msg.Sender = "@owner:example.org"
	msg.Text = "hello"
	if err = c.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	restored, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if err = restored.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	if sessions.created != 1 || sessions.asks != 2 {
		t.Fatal("did not resume profile conversation")
	}
	sessions.mu.Lock()
	sessions.events = []plugin.SessionEvent{{Kind: plugin.SessionAsked}}
	sessions.mu.Unlock()
	msg.Text = "/answer yes"
	if err = restored.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	msg.Text = "/cancel"
	if err = restored.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	if sessions.replies != 1 || sessions.cancels != 1 {
		t.Fatal("commands not routed")
	}
	sessions.mu.Lock()
	sessions.events = []plugin.SessionEvent{{Kind: plugin.SessionThought, Text: "private thought"}, {Kind: plugin.SessionAnswer, Text: "visible answer"}}
	sessions.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); restored.poll(ctx, conn) }()
	select {
	case text := <-conn.output:
		if text != "visible answer" {
			t.Fatalf("leaked presentation %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no answer published")
	}
	cancel()
	<-done
	// Snapshot metadata can move as answers are appended. Restoring must not
	// resend the first answer or skip the new one.
	again, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	sessions.mu.Lock()
	sessions.events = append(sessions.events, plugin.SessionEvent{Kind: plugin.SessionEffort, Text: "high"}, plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "second answer"})
	sessions.mu.Unlock()
	ctx2, stop2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { defer close(done2); again.poll(ctx2, conn) }()
	select {
	case text := <-conn.output:
		if text != "second answer" {
			t.Fatalf("replayed or skipped event: %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("new answer missing")
	}
	stop2()
	<-done2
	// Revocation applies even to a previously bound session.
	cfg.AllowedUsers = nil
	if err = s.save(cfg); err != nil {
		t.Fatal(err)
	}
	msg.Text = "must not run"
	if err = restored.receive(context.Background(), conn, msg); !errors.Is(err, plugin.ErrGatewayDenied) {
		t.Fatalf("revocation: %v", err)
	}
}

// conversationStore is a store with the settings of an enabled gateway that
// allows the owner.
func conversationStore(t *testing.T) (store, configuration) {
	t.Helper()
	s := store{t.TempDir()}
	cfg := configuration{Enabled: true, Token: "token", Homeserver: "https://example.org", UserID: "@bot:example.org", AllowedUsers: []string{"@owner:example.org"}, Revision: "1"}
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	return s, cfg
}

func TestOpenConversationsRefusesDamagedState(t *testing.T) {
	for name, data := range map[string]string{
		"no JSON":     "{",
		"no entry":    `{"key": null}`,
		"no session":  `{"key": {"Session": "", "Room": "!room:example.org", "Identity": {"Gateway": "matrix:x", "Subject": "@owner:example.org\n!room:example.org"}}}`,
		"no room":     `{"key": {"Session": "s", "Room": "", "Identity": {"Gateway": "matrix:x", "Subject": "@owner:example.org\n!room:example.org"}}}`,
		"no identity": `{"key": {"Session": "s", "Room": "!room:example.org", "Identity": {"Gateway": "", "Subject": "@owner:example.org\n!room:example.org"}}}`,
	} {
		s, _ := conversationStore(t)
		if err := os.WriteFile(s.path("conversations.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if c, err := openConversations(s, &testSessions{}); err == nil || c != nil {
			t.Errorf("%s: opened %s", name, data)
		}
	}
	s, _ := conversationStore(t)
	if err := os.Mkdir(s.path("conversations.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := openConversations(s, &testSessions{}); err == nil {
		t.Fatal("opened conversations it cannot read")
	}
}

func TestConversationPolicyFollowsTheSettings(t *testing.T) {
	s, cfg := conversationStore(t)
	c, err := openConversations(s, &testSessions{})
	if err != nil {
		t.Fatal(err)
	}
	owner := plugin.GatewayIdentity{Gateway: account(cfg), Subject: "@owner:example.org\n!room:example.org"}
	session, err := c.adapter.Open(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	// The subject names the peer; a subject without one names nobody.
	if _, err := c.adapter.Open(context.Background(), plugin.GatewayIdentity{Gateway: account(cfg), Subject: "@owner:example.org"}); !errors.Is(err, plugin.ErrGatewayDenied) {
		t.Fatalf("opened without a peer: %v", err)
	}
	for name, change := range map[string]func(*configuration){
		"disabled":         func(c *configuration) { c.Enabled = false },
		"logged out":       func(c *configuration) { c.Token = "" },
		"other account":    func(c *configuration) { c.UserID = "@other:example.org" },
		"other homeserver": func(c *configuration) { c.Homeserver = "https://matrix.example.org" },
		"owner removed":    func(c *configuration) { c.AllowedUsers = []string{"@lucas:example.org"} },
	} {
		changed := cfg
		change(&changed)
		if err := s.save(changed); err != nil {
			t.Fatal(err)
		}
		if _, err := c.adapter.Poll(context.Background(), owner, session); !errors.Is(err, plugin.ErrGatewayDenied) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := os.WriteFile(s.path("settings.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.adapter.Poll(context.Background(), owner, session); !errors.Is(err, plugin.ErrGatewayDenied) {
		t.Fatalf("unreadable settings: %v", err)
	}
	cfg.AllowedUsers = []string{"@lucas:example.org", "@owner:example.org"}
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := c.adapter.Poll(context.Background(), owner, session); err != nil {
		t.Fatalf("allowed again: %v", err)
	}
}

func TestReceiveExplainsWhatItCannotDo(t *testing.T) {
	s, _ := conversationStore(t)
	sessions := &testSessions{}
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	conn := &recordingConnection{}
	msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}
	if err := c.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	sessions.mu.Lock()
	sessions.askErr = plugin.ErrBusy
	sessions.mu.Unlock()
	if err := c.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	msg.Text = "  /answer yes "
	if err := c.receive(context.Background(), conn, msg); err != nil {
		t.Fatal(err)
	}
	sent := conn.messages()
	if len(sent) != 2 || !strings.HasPrefix(sent[0], "!room:example.org @owner:example.org An answer is already in progress.") ||
		sent[1] != "!room:example.org @owner:example.org There is no pending question to answer." {
		t.Fatalf("sent %q", sent)
	}
	// A question that is already answered is no pending question.
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionAsked}, plugin.SessionEvent{Kind: plugin.SessionAsked, Done: true})
	if err := c.receive(context.Background(), conn, msg); err != nil || len(conn.messages()) != 3 || sessions.replies != 0 {
		t.Fatalf("answered an answered question: %v", err)
	}
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionAsked, Done: true}, plugin.SessionEvent{Kind: plugin.SessionAsked})
	if err := c.receive(context.Background(), conn, msg); err != nil || sessions.replies != 1 {
		t.Fatalf("did not answer the pending question: %v", err)
	}
	unavailable := errors.New("session unavailable")
	sessions.mu.Lock()
	sessions.watchErr = unavailable
	sessions.mu.Unlock()
	if err := c.receive(context.Background(), conn, msg); !errors.Is(err, unavailable) || len(conn.messages()) != 3 {
		t.Fatalf("answer without the session: %v", err)
	}
	if err := os.WriteFile(s.path("settings.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.receive(context.Background(), conn, msg); err == nil {
		t.Fatal("received with unreadable settings")
	}
}

func TestReceiveForgetsAConversationItCannotSave(t *testing.T) {
	s, _ := conversationStore(t)
	sessions := &testSessions{}
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.path("conversations.json"), 0700); err != nil {
		t.Fatal(err)
	}
	msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}
	if err := c.receive(context.Background(), &recordingConnection{}, msg); err == nil {
		t.Fatal("answered in a conversation that is not saved")
	}
	if len(c.entries) != 0 || sessions.asks != 0 {
		t.Fatalf("kept %d conversations, asked %d times", len(c.entries), sessions.asks)
	}
	os.Remove(s.path("conversations.json"))
	if err := c.receive(context.Background(), &recordingConnection{}, msg); err != nil {
		t.Fatal(err)
	}
	if len(c.entries) != 1 || sessions.created != 2 || sessions.asks != 1 {
		t.Fatalf("%d conversations, %d sessions, %d asks", len(c.entries), sessions.created, sessions.asks)
	}
}

func TestPresented(t *testing.T) {
	question := func(args string, done bool) plugin.SessionEvent {
		return plugin.SessionEvent{Kind: plugin.SessionAsked, Args: json.RawMessage(args), Done: done}
	}
	for _, tc := range []struct {
		event plugin.SessionEvent
		want  string
	}{
		{plugin.SessionEvent{Kind: plugin.SessionSay, Text: "on my way"}, "on my way"},
		{plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "done"}, "done"},
		{plugin.SessionEvent{Kind: plugin.SessionFailed, Text: "secret error details"}, "The agent could not complete this answer. Check the local Ouro session for details."},
		{plugin.SessionEvent{Kind: plugin.SessionCanceled}, "Answer canceled."},
		{question(`{"question": "Delete it?", "options": ["yes", "no"]}`, false), "Delete it?\n\nyes\nno\n\nReply with /answer YOUR ANSWER"},
		{question(`{"question": "Which name?"}`, false), "Which name?\n\nReply with /answer YOUR ANSWER"},
		{question(`{"question": "Delete it?", "options": ["yes", "no"]}`, true), ""},
		{question(`{}`, false), ""},
		{plugin.SessionEvent{Kind: plugin.SessionThought, Text: "private thought"}, ""},
		{plugin.SessionEvent{Kind: plugin.SessionStep, Tool: "shell", Args: json.RawMessage(`{"command": "ls"}`)}, ""},
	} {
		if got := presented(tc.event); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.event.Kind, got, tc.want)
		}
	}
}
