// Package tui is a terminal UI to control the local node and chat with its
// agent, which can ask the nodes on the network. Plugins add screens and keys
// to it: see plugin.Screen and plugin.Key, and extend.go for how they are
// bound.
package tui

import (
	"cmp"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// refreshInterval is how often node status, connection requests and the
// screen of a plugin that is open are checked. Tests shorten it.
var refreshInterval = 10 * time.Second

// Status describes the local node.
type Status = plugin.NodeStatus

// Contacts are the connection requests of the local node.
type Contacts struct {
	Incoming []plugin.Request
	Outgoing []plugin.Request
}

// Backend is what the UI controls.
type Backend interface {
	Status() (Status, error)
	Start() error
	Stop() error
	// Ask lets the agent of this node answer text within the conversation
	// contextID. On the way it reports what it is waiting for.
	Ask(ctx context.Context, contextID, text string, step func(Step)) (string, error)
	Contacts() (Contacts, error)
	// Accept and Reject decide on a connection request. They fail with
	// plugin.ErrNotSaved if the decision could not be stored.
	Accept(ctx context.Context, publicKey string) error
	Reject(ctx context.Context, publicKey string) error
	// SetupCommand returns the interactive setup, run with the terminal handed over.
	SetupCommand() (*exec.Cmd, error)
}

// Step reports what the agent is waiting for on the way to its answer, like
// "asking beta", and with Done set, that it is no longer waiting for it.
type Step struct {
	Text string
	Done bool
}

type focus int

const (
	focusCommands focus = iota // keys are commands
	focusInput
)

// screen is the top-level view: the chat, the connection requests or a screen
// a plugin contributed.
type screen int

const (
	screenChat screen = iota
	screenRequests
	screenPlugin // the one of Model.screens that is open
)

// entry is one message in the conversation: from the user or from the agent.
type entry struct {
	mine   bool
	text   string
	at     time.Time
	failed bool
}

// conversation is the chat with the agent of this node.
type conversation struct {
	contextID string
	entries   []entry
	pending   bool               // waiting for a reply; one message at a time keeps the history in order
	doing     string             // what the agent is waiting for, like "asking beta"
	cancel    context.CancelFunc // stops the answer that is pending
}

// Model is the UI state.
type Model struct {
	backend Backend
	now     func() time.Time
	// ctx ends when the user quits; what the UI asks of the backend stops with it.
	ctx  context.Context
	quit context.CancelFunc

	status    Status
	chat      conversation
	focus     focus
	greeted   bool // the input was focused once the node turned out to be configured
	notice    string
	noticeErr bool

	trust     Contacts
	screen    screen
	screens   []plugin.Screen // of the plugins, as far as they got their key
	keys      []plugin.Key    // of the plugins, for where the commands are
	open      int             // the screen of a plugin that is shown
	body      body            // what it shows
	reqSel    int
	unreadReq int
	seenReq   int

	width, height int
	showSplash    bool
	splashStart   time.Time
	splashElapsed time.Duration
	input         textarea.Model
	history       viewport.Model
	spinner       spinner.Model
}

// New returns the UI for backend, with the screens and keys the plugins
// contributed. A contribution whose key is taken is left out, which the UI
// says in its footer when it opens: see bind.
func New(backend Backend, screens []plugin.Screen, keys []plugin.Key) *Model {
	input := textarea.New()
	input.Placeholder = "Message… (Enter sends, Alt+Enter new line)"
	input.ShowLineNumbers = false
	input.Prompt = ""
	input.SetHeight(inputHeight)
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "shift+enter", "ctrl+j"))

	history := viewport.New()
	history.SoftWrap = true
	history.KeyMap = viewport.KeyMap{} // keys go to the input; scrolling is handled explicitly

	ctx, quit := context.WithCancel(context.Background())
	m := &Model{
		backend:    backend,
		ctx:        ctx,
		quit:       quit,
		now:        time.Now,
		chat:       conversation{contextID: plugin.NewID()},
		showSplash: true,
		input:      input,
		history:    history,
		spinner:    spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
	var problems []string
	m.screens, m.keys, problems = bind(screens, keys)
	if len(problems) > 0 {
		m.setNotice(strings.Join(problems, "; "), true)
	}
	return m
}

type (
	statusMsg struct {
		status Status
		err    error
	}
	contactsMsg struct {
		trust Contacts
		err   error
	}
	replyMsg struct {
		contextID, text string
		err             error
		cancelled       bool // the answer was stopped; whatever it returned is dropped
	}
	// stepMsg is a step of the answer that is on its way; next delivers what follows.
	stepMsg struct {
		contextID string
		step      Step
		next      <-chan tea.Msg
	}
	// bodyMsg is what the screen of a plugin shows.
	bodyMsg struct {
		screen int
		body   body
	}
	// actionMsg reports the outcome of start, stop or setup, or of a key a
	// plugin contributed.
	actionMsg struct {
		done string
		err  error
	}
	setupMsg struct{ cmd *exec.Cmd }
	tickMsg  struct{}
	// splashMsg advances the splash animation.
	splashMsg struct{ at time.Time }
)

func (m *Model) Init() tea.Cmd {
	m.splashStart = m.now()
	return tea.Batch(m.goOnline, m.loadContacts, tick(), m.spinner.Tick, tea.RequestBackgroundColor, splashTick())
}

func splashTick() tea.Cmd {
	return tea.Tick(splashFrame, func(at time.Time) tea.Msg { return splashMsg{at} })
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) refreshStatus() tea.Msg {
	s, err := m.backend.Status()
	return statusMsg{s, err}
}

// goOnline starts the node unless it is running or not configured yet: while
// the UI is open, the node is online.
func (m *Model) goOnline() tea.Msg {
	s, err := m.backend.Status()
	if err != nil || s.Running || !s.Configured {
		return statusMsg{s, err}
	}
	if err := m.backend.Start(); err != nil {
		return actionMsg{err: err}
	}
	return m.refreshStatus()
}

func (m *Model) loadContacts() tea.Msg {
	trust, err := m.backend.Contacts()
	return contactsMsg{trust, err}
}

// refresh reloads the node status, the connection requests and the screen of
// a plugin that is open.
func (m *Model) refresh() tea.Cmd {
	if m.screen == screenPlugin {
		return tea.Batch(m.refreshStatus, m.loadContacts, m.loadBody())
	}
	return tea.Batch(m.refreshStatus, m.loadContacts)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
	case tea.BackgroundColorMsg:
		m.input.SetStyles(textarea.DefaultStyles(msg.IsDark()))
	case tickMsg:
		return m, tea.Batch(m.refresh(), tick())
	case splashMsg:
		if !m.showSplash {
			return m, nil
		}
		m.splashElapsed = msg.at.Sub(m.splashStart)
		if m.splashElapsed >= splashDuration {
			m.showSplash = false
			return m, nil
		}
		return m, splashTick()
	case statusMsg:
		if msg.err != nil {
			m.setNotice(msg.err.Error(), true)
		} else {
			m.status = msg.status
			if len(m.chat.entries) == 0 {
				m.renderHistory(true) // the empty chat names the agent
			}
			// The chat is what the UI is opened for: start typing right away.
			if m.status.Configured && !m.greeted {
				m.greeted = true
				if m.focus == focusCommands {
					return m, m.toggleFocus()
				}
			}
		}
	case contactsMsg:
		if msg.err != nil {
			m.setNotice("contacts: "+msg.err.Error(), true)
		} else {
			m.applyContacts(msg.trust)
		}
	case stepMsg:
		m.step(msg)
		return m, func() tea.Msg { return <-msg.next }
	case replyMsg:
		m.receive(msg)
	case bodyMsg:
		if m.screen == screenPlugin && m.open == msg.screen {
			m.body = msg.body
		}
	case actionMsg:
		if msg.err != nil {
			m.setNotice(msg.err.Error(), true)
		} else {
			m.setNotice(msg.done, false)
		}
		return m, m.refresh()
	case setupMsg:
		return m, tea.ExecProcess(msg.cmd, setupDone)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.history, cmd = m.history.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if m.showSplash {
			if msg.String() == "ctrl+c" {
				return m, m.leave()
			}
			m.showSplash = false
			return m, nil
		}
		return m, m.handleKey(msg)
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// setupDone reports how the interactive setup ended.
func setupDone(err error) tea.Msg {
	if err != nil {
		return actionMsg{err: err}
	}
	return actionMsg{done: "node configured and started"}
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	m.notice = ""
	switch m.screen {
	case screenRequests:
		return m.handleRequestKey(msg)
	case screenPlugin:
		return m.handleScreenKey(msg)
	}
	switch msg.String() {
	case "ctrl+c":
		return m.leave()
	case "tab":
		return m.toggleFocus()
	case "pgup":
		m.history.PageUp()
		return nil
	case "pgdown":
		m.history.PageDown()
		return nil
	}

	if m.focus == focusInput {
		switch msg.String() {
		case "esc":
			return m.toggleFocus()
		case "enter":
			return m.send()
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return cmd
	}

	switch msg.String() {
	case "q":
		return m.leave()
	case "enter", "i":
		return m.toggleFocus()
	case "s":
		return m.toggleNode()
	case "c":
		return m.setup()
	case "n":
		m.newConversation()
	case "R":
		return m.openRequests()
	default:
		return m.contributed(msg.String())
	}
	return nil
}

// handleRequestKey handles keys on the connection requests screen.
func (m *Model) handleRequestKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		return m.leave()
	case "esc", "q", "R":
		m.screen = screenChat
	case "up", "k":
		m.selectRequest(m.reqSel - 1)
	case "down", "j":
		m.selectRequest(m.reqSel + 1)
	case "a":
		return m.decideRequest(true)
	case "x":
		return m.decideRequest(false)
	}
	return nil
}

// leave quits the UI and stops what it still asks of the backend: a pending
// answer, with the peers it asks, and a decision on a connection request.
func (m *Model) leave() tea.Cmd {
	m.quit()
	return tea.Quit
}

func (m *Model) openRequests() tea.Cmd {
	m.screen = screenRequests
	m.reqSel = 0
	m.unreadReq = 0
	m.seenReq = len(m.trust.Incoming)
	return nil
}

func (m *Model) selectRequest(i int) {
	if len(m.trust.Incoming) == 0 {
		m.reqSel = 0
		return
	}
	m.reqSel = min(max(i, 0), len(m.trust.Incoming)-1)
}

func (m *Model) decideRequest(accept bool) tea.Cmd {
	if m.reqSel >= len(m.trust.Incoming) {
		return nil
	}
	pub := m.trust.Incoming[m.reqSel].PublicKey
	verb, failed := "declined", "rejecting failed"
	if accept {
		verb, failed = "accepted", "accepting failed"
	}
	return func() tea.Msg {
		var err error
		if accept {
			err = m.backend.Accept(m.ctx, pub)
		} else {
			err = m.backend.Reject(m.ctx, pub)
		}
		if errors.Is(err, plugin.ErrNotSaved) {
			// Nothing was decided. The request stays in the list, to be tried
			// again; why it failed is in the log.
			return actionMsg{err: errors.New(failed + " – the contact could not be saved")}
		}
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{done: "request " + verb}
	}
}

func (m *Model) applyContacts(trust Contacts) {
	m.trust = trust
	if len(trust.Incoming) > m.seenReq {
		m.unreadReq += len(trust.Incoming) - m.seenReq
	}
	if m.screen == screenRequests {
		m.unreadReq = 0
	}
	m.seenReq = len(trust.Incoming)
	m.selectRequest(m.reqSel)
}

func (m *Model) toggleFocus() tea.Cmd {
	if m.focus == focusInput {
		m.focus = focusCommands
		m.input.Blur()
		return nil
	}
	if !m.status.Configured {
		m.setNotice("set up the node first: press c", true)
		return nil
	}
	m.focus = focusInput
	return m.input.Focus()
}

func (m *Model) toggleNode() tea.Cmd {
	running, configured := m.status.Running, m.status.Configured
	if !running && !configured {
		return m.setup()
	}
	if running {
		m.setNotice("stopping node…", false)
	} else {
		m.setNotice("starting node…", false)
	}
	return func() tea.Msg {
		if running {
			return actionMsg{"node stopped", m.backend.Stop()}
		}
		return actionMsg{"node started", m.backend.Start()}
	}
}

// setup stops the node and hands the terminal to the interactive setup.
func (m *Model) setup() tea.Cmd {
	return func() tea.Msg {
		if err := m.backend.Stop(); err != nil {
			return actionMsg{err: err}
		}
		cmd, err := m.backend.SetupCommand()
		if err != nil {
			return actionMsg{err: err}
		}
		return setupMsg{cmd}
	}
}

func (m *Model) send() tea.Cmd {
	c := &m.chat
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}
	if c.pending {
		m.setNotice("wait for the reply first", true)
		return nil
	}
	c.entries = append(c.entries, entry{mine: true, text: text, at: m.now()})
	c.pending = true
	m.input.Reset()
	m.renderHistory(true)

	contextID := c.contextID
	ctx, cancel := context.WithCancel(m.ctx)
	c.cancel = cancel
	return func() tea.Msg {
		events := make(chan tea.Msg)
		go func() {
			reply, err := m.backend.Ask(ctx, contextID, text, func(s Step) {
				select {
				case events <- stepMsg{contextID, s, events}:
				case <-ctx.Done(): // nobody follows the steps of a stopped answer
				}
			})
			events <- replyMsg{contextID, reply, err, ctx.Err() != nil}
		}()
		return <-events
	}
}

// step notes what the agent is waiting for while the answer is pending.
func (m *Model) step(msg stepMsg) {
	c := &m.chat
	if c.contextID != msg.contextID {
		return // step of a conversation that was reset
	}
	c.doing = msg.step.Text
	if msg.step.Done {
		c.doing = ""
	}
}

func (m *Model) receive(msg replyMsg) {
	c := &m.chat
	if c.contextID != msg.contextID || msg.cancelled {
		return // reply to a conversation that was reset, or stopped by quitting
	}
	c.cancel() // the answer is in; release its context
	c.pending, c.doing, c.cancel = false, "", nil
	e := entry{text: msg.text, at: m.now()}
	if msg.err != nil {
		e.text, e.failed = msg.err.Error(), true
	}
	c.entries = append(c.entries, e)
	m.renderHistory(m.history.AtBottom())
}

// newConversation starts over; the agent keeps no memory of the old context
// and stops working on an answer that is still pending.
func (m *Model) newConversation() {
	if m.chat.cancel != nil {
		m.chat.cancel()
	}
	m.chat = conversation{contextID: plugin.NewID()}
	m.renderHistory(true)
	m.setNotice("new conversation", false)
}

// agentName is how the agent of this node is called in the chat.
func (m *Model) agentName() string {
	return cmp.Or(m.status.Name, "your agent")
}

func (m *Model) setNotice(text string, isErr bool) {
	m.notice, m.noticeErr = text, isErr
}
