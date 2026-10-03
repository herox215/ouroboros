package tui

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/internal/a2a"
	"ouroboros/internal/contacts"
	"ouroboros/internal/discovery"
)

type sent struct {
	node      discovery.Node
	contextID string
	text      string
}

type fakeBackend struct {
	status        Status
	nodes         []discovery.Node
	sent          []sent
	reply         string
	sendErr       error
	started       int
	stopped       int
	setupCommands int

	trust     Contacts
	accepted  []string
	rejected  []string
	acceptErr error
	rejectErr error
}

func (f *fakeBackend) Status() (Status, error)             { return f.status, nil }
func (f *fakeBackend) Start() error                        { f.started++; return nil }
func (f *fakeBackend) Stop() error                         { f.stopped++; return nil }
func (f *fakeBackend) Discover() ([]discovery.Node, error) { return f.nodes, nil }
func (f *fakeBackend) Contacts() (Contacts, error)         { return f.trust, nil }
func (f *fakeBackend) SetupCommand() (*exec.Cmd, error) {
	f.setupCommands++
	return exec.Command("true"), nil
}
func (f *fakeBackend) Send(_ context.Context, node discovery.Node, contextID, text string) (string, error) {
	f.sent = append(f.sent, sent{node, contextID, text})
	return f.reply, f.sendErr
}
func (f *fakeBackend) Accept(publicKey string) error {
	f.accepted = append(f.accepted, publicKey)
	return f.acceptErr
}
func (f *fakeBackend) Reject(publicKey string) error {
	f.rejected = append(f.rejected, publicKey)
	return f.rejectErr
}

var (
	alpha = discovery.Node{ID: "aaa111", Name: "alpha", Model: "m1", Port: 1, Host: "10.0.0.1"}
	beta  = discovery.Node{ID: "bbb222", Name: "beta", Model: "m2", Port: 2, Host: "10.0.0.2"}
)

func newModel(t *testing.T, nodes ...discovery.Node) (*Model, *fakeBackend) {
	t.Helper()
	b := &fakeBackend{status: Status{Configured: true, Running: true, ID: alpha.ID, Name: "alpha"}, nodes: nodes, reply: "pong"}
	m := New(b)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.showSplash = false // dismiss the start screen; its own test is below
	m.Update(m.refreshStatus())
	m.Update(m.discover())
	return m, b
}

func press(m *Model, keys ...string) tea.Cmd {
	var cmds []tea.Cmd
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg.Code = tea.KeyEnter
		case "esc":
			msg.Code = tea.KeyEscape
		case "tab":
			msg.Code = tea.KeyTab
		default:
			msg.Code, msg.Text = rune(k[0]), k
		}
		_, cmd := m.Update(msg)
		cmds = append(cmds, cmd)
	}
	return cmds[len(cmds)-1]
}

// say types text into the chat with the selected node and sends it.
func say(t *testing.T, m *Model, text string) tea.Cmd {
	t.Helper()
	if m.focus != focusInput {
		press(m, "enter")
	}
	m.input.SetValue(text)
	cmd := press(m, "enter")
	if cmd == nil {
		t.Fatal("sending should return a command")
	}
	return cmd
}

func TestSplashShowsLogoUntilKey(t *testing.T) {
	m, _ := newModel(t)
	m.showSplash = true
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := m.View().Content
	if !strings.Contains(view, "▄▄██████▄▄") || !strings.Contains(view, "ouroboros") {
		t.Fatalf("splash should show the logo and the title:\n%s", view)
	}
	if strings.Contains(view, "Nodes") {
		t.Fatal("splash should hide the normal view")
	}

	if cmd := press(m, "x"); cmd != nil {
		t.Fatal("closing the splash should not run a command")
	}
	if m.showSplash {
		t.Fatal("a key should close the splash")
	}
	if view := m.View().Content; !strings.Contains(view, "Nodes") {
		t.Fatal("after the splash the normal view should be shown")
	}
}

func TestSplashClosesAfterTimeout(t *testing.T) {
	m, _ := newModel(t)
	m.showSplash = true
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.splashStart = time.Unix(0, 0)
	if _, cmd := m.Update(splashMsg{m.splashStart.Add(splashDuration / 2)}); cmd == nil || !m.showSplash {
		t.Fatal("the splash should keep animating until its time is up")
	}
	m.Update(splashMsg{m.splashStart.Add(splashDuration)})
	if m.showSplash {
		t.Fatal("the splash should close when its time is up")
	}
	if view := m.View().Content; !strings.Contains(view, "Nodes") {
		t.Fatal("after the splash the normal view should be shown")
	}
}

func TestSplashFramesKeepTheirSize(t *testing.T) {
	width := ansi.StringWidth(logoLines[0])
	for at := time.Duration(0); at <= splashDuration; at += splashFrame {
		frame := splashLogo(at)
		if len(frame) != len(logoLines) {
			t.Fatalf("frame at %v has %d lines, want %d", at, len(frame), len(logoLines))
		}
		for _, l := range frame {
			if ansi.StringWidth(l) != width {
				t.Fatalf("frame at %v has a line of width %d, want %d", at, ansi.StringWidth(l), width)
			}
		}
	}
	if slices.Equal(splashLogo(0), splashLogo(splashDuration)) {
		t.Fatal("the splash should animate")
	}
}

func TestSplashCtrlCQuits(t *testing.T) {
	m, _ := newModel(t)
	m.showSplash = true
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("ctrl+c should quit")
	}
	if !m.showSplash {
		t.Fatal("ctrl+c should keep the splash and quit instead")
	}
}

func TestEmptyChatShowsLogo(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m.Update(m.discover()) // no nodes: nothing selected
	if view := m.View().Content; !strings.Contains(view, "▄▄██████▄▄") {
		t.Fatalf("empty chat should show the logo:\n%s", view)
	}
}

func TestNodesAreListedByName(t *testing.T) {
	m, _ := newModel(t, beta, alpha)
	if got := strings.Join(m.order, ","); got != "aaa111,bbb222" {
		t.Fatalf("order = %s", got)
	}
	view := m.View().Content
	for _, want := range []string{"alpha (you)", "beta", "m2 · bbb222", "● online"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q", want)
		}
	}
}

func TestChat(t *testing.T) {
	m, b := newModel(t, alpha)
	cmd := say(t, m, "  ping  ")
	if !m.current().pending || m.input.Value() != "" {
		t.Fatal("message should be pending and the input cleared")
	}
	m.input.SetValue("again")
	if press(m, "enter") != nil || len(m.current().entries) != 1 {
		t.Fatal("a second message must wait for the reply")
	}

	m.Update(cmd())
	c := m.current()
	if len(b.sent) != 1 || b.sent[0] != (sent{alpha, c.contextID, "ping"}) {
		t.Fatalf("sent %+v", b.sent)
	}
	if c.pending || len(c.entries) != 2 || c.entries[1].text != "pong" || c.entries[1].mine {
		t.Fatalf("entries %+v", c.entries)
	}
	if view := m.View().Content; !strings.Contains(view, "ping") || !strings.Contains(view, "pong") {
		t.Fatal("history should show both messages")
	}

	// The conversation keeps its context.
	m.Update(say(t, m, "more")())
	if b.sent[1].contextID != c.contextID {
		t.Fatal("context changed within a conversation")
	}
}

func TestSendError(t *testing.T) {
	m, b := newModel(t, alpha)
	b.sendErr = errors.New("connection refused")
	m.Update(say(t, m, "ping")())
	e := m.current().entries[1]
	if !e.failed || e.text != "connection refused" {
		t.Fatalf("entry %+v", e)
	}
}

func TestPendingSendShowsNotice(t *testing.T) {
	m, b := newModel(t, alpha)
	b.sendErr = &a2a.Error{Code: a2a.CodePending, Message: "waiting"}
	m.Update(say(t, m, "ping")())
	e := m.current().entries[1]
	if !e.system || e.failed || !strings.Contains(e.text, "Waiting for approval") {
		t.Fatalf("entry %+v", e)
	}
}

func TestConnectionRequests(t *testing.T) {
	m, b := newModel(t, alpha)
	b.trust = Contacts{Incoming: []contacts.Request{{PublicKey: "pk1", Name: "gamma", Addr: "1.2.3.4:9"}}}
	m.Update(m.loadContacts())
	if view := m.View().Content; !strings.Contains(view, "✉ 1") {
		t.Fatalf("header should show the pending count:\n%s", view)
	}

	press(m, "R")
	if m.screen != screenRequests {
		t.Fatal("R should open the requests screen")
	}
	if view := m.View().Content; !strings.Contains(view, "gamma") || !strings.Contains(view, "wants to chat") {
		t.Fatalf("requests view:\n%s", view)
	}

	cmd := press(m, "a")
	if cmd == nil {
		t.Fatal("accept should return a command")
	}
	m.Update(cmd())
	if len(b.accepted) != 1 || b.accepted[0] != "pk1" {
		t.Fatalf("accepted %v", b.accepted)
	}

	press(m, "esc")
	if m.screen != screenChat {
		t.Fatal("esc should go back to the chat")
	}
}

func TestRejectRequest(t *testing.T) {
	m, b := newModel(t, alpha)
	b.trust = Contacts{Incoming: []contacts.Request{{PublicKey: "pk1", Name: "gamma"}}}
	m.Update(m.loadContacts())
	press(m, "R")
	cmd := press(m, "x")
	if cmd == nil {
		t.Fatal("reject should return a command")
	}
	m.Update(cmd())
	if len(b.rejected) != 1 || b.rejected[0] != "pk1" {
		t.Fatalf("rejected %v", b.rejected)
	}
}

func TestUnreadRepliesOfOtherConversations(t *testing.T) {
	m, _ := newModel(t, alpha, beta)
	cmd := say(t, m, "ping") // to alpha
	press(m, "esc", "j")     // switch to beta before the reply arrives
	m.Update(cmd())
	if c := m.convs[alpha.ID]; c.unread != 1 {
		t.Fatalf("unread = %d", c.unread)
	}
	if !strings.Contains(m.View().Content, "●1") {
		t.Fatal("view should show the unread count")
	}
	press(m, "k")
	if m.convs[alpha.ID].unread != 0 {
		t.Fatal("selecting a conversation should mark it read")
	}
}

func TestNewConversationDropsLateReply(t *testing.T) {
	m, _ := newModel(t, alpha)
	cmd := say(t, m, "ping")
	old := m.current().contextID
	press(m, "esc", "n")
	m.Update(cmd())
	c := m.current()
	if c.contextID == old || len(c.entries) != 0 || c.pending {
		t.Fatalf("conversation not reset: %+v", c)
	}
}

func TestVanishedNodeKeepsConversation(t *testing.T) {
	m, b := newModel(t, alpha, beta)
	m.Update(say(t, m, "ping")()) // alpha now has history, beta has none
	press(m, "esc")
	b.nodes = nil
	m.Update(m.discover())
	if len(m.order) != 1 || m.current() != m.convs[alpha.ID] || m.current().online {
		t.Fatalf("order %v, selected %v", m.order, m.currentID())
	}
	if !strings.Contains(m.View().Content, "offline") {
		t.Fatal("vanished node should be shown offline")
	}
	b.nodes = []discovery.Node{alpha}
	m.Update(m.discover())
	if !m.current().online || len(m.current().entries) != 2 {
		t.Fatal("returning node should keep its conversation")
	}
}

func TestToggleNode(t *testing.T) {
	m, b := newModel(t)
	m.Update(press(m, "s")())
	if b.stopped != 1 {
		t.Fatal("s should stop a running node")
	}
	m.status.Running = false
	m.Update(press(m, "s")())
	if b.started != 1 {
		t.Fatal("s should start a stopped node")
	}
	m.status.Configured = false
	if msg := press(m, "s")(); b.setupCommands != 1 {
		t.Fatalf("s should run the setup for an unconfigured node, got %T", msg)
	}
}

func TestSetupStopsNodeFirst(t *testing.T) {
	m, b := newModel(t)
	msg := press(m, "c")()
	if _, ok := msg.(setupMsg); !ok || b.stopped != 1 || b.setupCommands != 1 {
		t.Fatalf("got %T, stopped %d", msg, b.stopped)
	}
}

func TestTinyWindow(t *testing.T) {
	m, _ := newModel(t, alpha, beta)
	for _, size := range [][2]int{{1, 1}, {20, 5}, {40, 8}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.showSplash = true
		m.View() // the splash must survive tiny windows
		m.showSplash = false
		m.View()
	}
}
