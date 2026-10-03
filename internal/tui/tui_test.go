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

	"ouroboros/internal/contacts"
	"ouroboros/internal/discovery"
)

type sent struct {
	contextID string
	text      string
}

type fakeBackend struct {
	status        Status
	nodes         []discovery.Node
	sent          []sent
	steps         []Step // reported before the reply
	reply         string
	sendErr       error
	started       int
	stopped       int
	setupCommands int
	updates       int
	updateAvail   bool
	updated       bool
	updateErr     error

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
func (f *fakeBackend) UpdateAvailable() (bool, error)      { return f.updateAvail, nil }
func (f *fakeBackend) Update() (bool, error)               { f.updates++; return f.updated, f.updateErr }
func (f *fakeBackend) SetupCommand() (*exec.Cmd, error) {
	f.setupCommands++
	return exec.Command("true"), nil
}
func (f *fakeBackend) Ask(_ context.Context, contextID, text string, step func(Step)) (string, error) {
	f.sent = append(f.sent, sent{contextID, text})
	for _, s := range f.steps {
		step(s)
	}
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

// commands is a model whose keys are commands rather than text for the input.
func commands(t *testing.T, nodes ...discovery.Node) (*Model, *fakeBackend) {
	t.Helper()
	m, b := newModel(t, nodes...)
	press(m, "esc")
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

// say types text into the chat and sends it.
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

// deliver runs cmd and the commands that follow it until the reply is in.
func deliver(m *Model, cmd tea.Cmd) {
	for cmd != nil {
		_, cmd = m.Update(cmd())
	}
}

func TestSplashShowsLogoUntilKey(t *testing.T) {
	m, _ := newModel(t)
	m.showSplash = true
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := m.View().Content
	if !strings.Contains(view, strings.TrimSpace(splashLogo(0)[1])) || !strings.Contains(view, "ouroboros") {
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

func TestUnconfiguredNodeShowsLogoAndNoChat(t *testing.T) {
	m, b := newModel(t)
	b.status = Status{}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m.Update(m.refreshStatus())
	view := m.View().Content
	if !strings.Contains(view, strings.TrimSpace(logoLines[1])) || !strings.Contains(view, "Press c to set up") {
		t.Fatalf("an unconfigured node should show the logo and the setup hint:\n%s", view)
	}
	if press(m, "esc", "enter"); m.focus == focusInput || !m.noticeErr {
		t.Fatal("without a configured node there is nobody to chat with")
	}
}

func TestChatIsFocusedOnStart(t *testing.T) {
	m, _ := newModel(t)
	if m.focus != focusInput {
		t.Fatal("a configured node should open with the chat input focused")
	}
	press(m, "esc")
	m.Update(m.refreshStatus())
	if m.focus != focusList {
		t.Fatal("a later status refresh must not steal the focus")
	}
}

func TestNodesAreListedByName(t *testing.T) {
	m, _ := newModel(t, beta, alpha)
	if len(m.nodes) != 2 || m.nodes[0].ID != alpha.ID || m.nodes[1].ID != beta.ID {
		t.Fatalf("nodes = %+v", m.nodes)
	}
	view := m.View().Content
	for _, want := range []string{"alpha (you)", "beta", "m2 · bbb222", "● online"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q", want)
		}
	}
}

func TestChat(t *testing.T) {
	m, b := newModel(t) // the agent needs no other node
	cmd := say(t, m, "  ping  ")
	if !m.chat.pending || m.input.Value() != "" {
		t.Fatal("message should be pending and the input cleared")
	}
	if !strings.Contains(m.View().Content, "alpha is thinking") {
		t.Fatal("a pending answer should be announced")
	}
	m.input.SetValue("again")
	if press(m, "enter") != nil || len(m.chat.entries) != 1 {
		t.Fatal("a second message must wait for the reply")
	}

	deliver(m, cmd)
	c := m.chat
	if len(b.sent) != 1 || b.sent[0] != (sent{c.contextID, "ping"}) {
		t.Fatalf("sent %+v", b.sent)
	}
	if c.pending || len(c.entries) != 2 || c.entries[1].text != "pong" || c.entries[1].mine {
		t.Fatalf("entries %+v", c.entries)
	}
	if view := m.View().Content; !strings.Contains(view, "ping") || !strings.Contains(view, "pong") {
		t.Fatal("history should show both messages")
	}

	// The conversation keeps its context.
	deliver(m, say(t, m, "more"))
	if b.sent[1].contextID != c.contextID {
		t.Fatal("context changed within a conversation")
	}
}

func TestChatKeepsTheAgentsTalkToPeersOutOfTheHistory(t *testing.T) {
	m, b := newModel(t, alpha, beta)
	b.steps = []Step{{Peer: "beta"}, {Peer: "beta", Done: true}}
	b.reply = "beta says it is noon"
	cmd := say(t, m, "ask beta for the time")

	_, cmd = m.Update(cmd()) // the question to beta is on its way
	if m.chat.asking != "beta" || !m.chat.pending || !strings.Contains(m.View().Content, "asking beta…") {
		t.Fatalf("the call to beta should be announced: %+v", m.chat)
	}
	_, cmd = m.Update(cmd()) // beta answered, the agent thinks again
	if m.chat.asking != "" || !strings.Contains(m.View().Content, "alpha is thinking") {
		t.Fatalf("after the peer's answer the agent thinks: %+v", m.chat)
	}
	deliver(m, cmd)

	c := m.chat
	if c.pending || len(c.entries) != 2 || c.entries[1].text != "beta says it is noon" {
		t.Fatalf("only the user and the agent belong in the history: %+v", c.entries)
	}
}

func TestSendError(t *testing.T) {
	m, b := newModel(t, alpha)
	b.sendErr = errors.New("connection refused")
	deliver(m, say(t, m, "ping"))
	e := m.chat.entries[1]
	if !e.failed || e.text != "connection refused" {
		t.Fatalf("entry %+v", e)
	}
}

func TestConnectionRequests(t *testing.T) {
	m, b := commands(t, alpha)
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
	m, b := commands(t, alpha)
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

func TestNewConversationDropsLateReply(t *testing.T) {
	m, b := newModel(t, alpha)
	b.steps = []Step{{Peer: "beta"}}
	cmd := say(t, m, "ping")
	old := m.chat.contextID
	press(m, "esc", "n")
	deliver(m, cmd)
	c := m.chat
	if c.contextID == old || len(c.entries) != 0 || c.pending || c.asking != "" {
		t.Fatalf("conversation not reset: %+v", c)
	}
}

func TestVanishedNodeIsNoLongerListed(t *testing.T) {
	m, b := newModel(t, alpha, beta)
	deliver(m, say(t, m, "ping"))
	b.nodes = []discovery.Node{alpha}
	m.Update(m.discover())
	if len(m.nodes) != 1 || strings.Contains(m.View().Content, "bbb222") {
		t.Fatalf("nodes = %+v", m.nodes)
	}
	if len(m.chat.entries) != 2 {
		t.Fatal("the conversation with the agent does not depend on the nodes")
	}
}

func TestToggleNode(t *testing.T) {
	m, b := commands(t)
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
	m, b := commands(t)
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

func TestUpdateHintAndRestart(t *testing.T) {
	m, b := commands(t, alpha)
	if strings.Contains(m.View().Content, "update available") {
		t.Fatal("no hint without an update")
	}
	b.updateAvail = true
	if _, cmd := m.Update(m.checkUpdate()); cmd == nil {
		t.Fatal("the next check should be scheduled")
	}
	if !strings.Contains(m.View().Content, "update available") {
		t.Fatalf("header should announce the update:\n%s", m.View().Content)
	}

	b.updated = true
	cmd := press(m, "u")
	if cmd == nil || !m.updating || !strings.Contains(m.View().Content, "updating…") {
		t.Fatal("u should start the update")
	}
	if press(m, "u") != nil {
		t.Fatal("a running update should not be started again")
	}
	if _, quit := m.Update(cmd()); quit == nil || !m.Restart() || b.updates != 1 {
		t.Fatalf("after an update the UI should restart (updates: %d)", b.updates)
	}
}

func TestUpdateFailureAndNothingToDo(t *testing.T) {
	m, b := commands(t, alpha)
	m.Update(press(m, "u")())
	if m.Restart() || m.updating || !strings.Contains(m.View().Content, "already up to date") {
		t.Fatalf("without a new version the UI keeps running:\n%s", m.View().Content)
	}

	b.updateErr = errors.New("git: offline")
	m.Update(press(m, "u")())
	if m.Restart() || !m.noticeErr || !strings.Contains(m.View().Content, "update: git: offline") {
		t.Fatalf("a failed update should be reported:\n%s", m.View().Content)
	}
}

func TestChatNotesWhatTheAgentHasDone(t *testing.T) {
	m, b := newModel(t, alpha)
	b.steps = []Step{{Note: "soul updated"}}
	b.reply = "done"
	cmd := say(t, m, "be calm")

	_, cmd = m.Update(cmd())
	if !m.chat.pending || !strings.Contains(m.View().Content, "◆ soul updated") {
		t.Fatalf("the note should be in the history: %+v", m.chat.entries)
	}
	deliver(m, cmd)
	if c := m.chat; len(c.entries) != 3 || !c.entries[1].note || c.entries[2].text != "done" {
		t.Fatalf("history: %+v", c.entries)
	}
}
