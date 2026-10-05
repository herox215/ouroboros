// Package tui is a terminal UI to control the local node and chat with its
// agent, which can ask the nodes on the network. The owner controls it with
// commands like /new, typed into the message input: see commands.go. Plugins
// add screens and commands to it: see plugin.Screen and plugin.Slash, and
// extend.go for how they are bound.
package tui

import (
	"cmp"
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// refreshInterval is how often node status, connection requests and the
// screen of a plugin that is open are checked. Tests shorten it.
var refreshInterval = 10 * time.Second

// Status describes the local node.
type Status = plugin.NodeStatus

// Contacts is what waits for the owner of the local node: the connection
// requests, and the changes of the configuration its agent proposed.
type Contacts struct {
	Incoming []plugin.Request
	Outgoing []plugin.Request
	// Proposals are listed after Incoming, and selected like them.
	Proposals []plugin.Proposal
}

// waiting is how many entries wait for a decision.
func (c Contacts) waiting() int { return len(c.Incoming) + len(c.Proposals) }

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
	// AcceptProposal and RefuseProposal decide on a change of the
	// configuration the agent proposed. Accepting fails with plugin.ErrStale
	// if the configuration changed since it was proposed.
	AcceptProposal(id string) error
	RefuseProposal(id string) error
	// Setup starts the setup of the node.
	Setup() (Setup, error)
}

// Step reports what the agent is waiting for on the way to its answer, like
// "asking beta", and with Done set, that it is no longer waiting for it.
type Step struct {
	Text string
	Done bool
}

// screen is the top-level view: the chat, the connection requests, the list
// of the commands or a screen a plugin contributed.
type screen int

const (
	screenChat screen = iota
	screenRequests
	screenHelp
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
	notice    string
	noticeErr bool

	trust     Contacts
	screen    screen
	commands  []command       // of the UI, then of the plugins
	screens   []plugin.Screen // of the plugins, as far as they got their command
	open      int             // the screen of a plugin that is shown
	body      body            // what it shows
	reqSel    int
	choice    int // the action selected: accept or reject, or one of a screen
	unreadReq int
	seenReq   int

	width, height int
	showSplash    bool
	splashStart   time.Time
	splashElapsed time.Duration
	skyShift      time.Duration // how much farther the stars are than the splash: see frame
	animating     bool          // frames are coming, for the splash or the setup
	onboard       *onboarding   // the setup, while it is open
	light         bool          // the background of the terminal is light
	input         textarea.Model
	history       viewport.Model
	spinner       spinner.Model
}

// New returns the UI for backend, with the screens and commands the plugins
// contributed. A contribution whose name is taken is left out, which the UI
// says in its footer when it opens: see bind.
func New(backend Backend, screens []plugin.Screen, slashes []plugin.Slash) *Model {
	input := textarea.New()
	input.Placeholder = "Message, or /help for the commands… (Enter sends, Alt+Enter new line)"
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
	m.input.Focus() // the input is where everything is typed, commands too
	var problems []string
	m.screens, slashes, problems = bind(ownNames(), screens, slashes)
	m.commands = append(m.commands, own...)
	for i, s := range m.screens {
		m.commands = append(m.commands, command{s.Command, "open " + s.Title, func(m *Model) tea.Cmd { return m.openScreen(i) }})
	}
	for _, c := range slashes {
		m.commands = append(m.commands, command{c.Name, c.Help, func(m *Model) tea.Cmd { return m.run(c.Run) }})
	}
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
	// actionMsg reports the outcome of start, stop or setup, or of a command
	// or an action a plugin contributed.
	actionMsg struct {
		done string
		err  error
	}
	tickMsg struct{}
	// splashMsg advances the animation of the splash or of the setup.
	splashMsg struct{ at time.Time }
)

// Init starts the UI with the splash, or with the setup for a node that was
// not set up yet.
func (m *Model) Init() tea.Cmd {
	m.splashStart = m.now()
	if s, err := m.backend.Status(); err == nil && !s.Configured {
		m.showSplash = false
		m.openSetup(true)
	}
	m.animating = true
	return tea.Batch(m.goOnline, m.loadContacts, tick(), m.spinner.Tick, tea.RequestBackgroundColor, splashTick())
}

// animate makes the frames come unless they do already.
func (m *Model) animate() tea.Cmd {
	if m.animating {
		return nil
	}
	m.animating = true
	return splashTick()
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
		m.light = !msg.IsDark()
		m.input.SetStyles(textarea.DefaultStyles(msg.IsDark()))
		if m.onboard != nil {
			m.onboard.input.SetStyles(textinput.DefaultStyles(msg.IsDark()))
		}
	case tickMsg:
		return m, tea.Batch(m.refresh(), tick())
	case splashMsg:
		if m.onboard != nil {
			m.frame(msg.at)
			return m, splashTick()
		}
		if !m.showSplash {
			m.animating = false
			return m, nil
		}
		m.splashElapsed = msg.at.Sub(m.splashStart)
		if m.splashElapsed >= splashDuration {
			m.showSplash, m.animating = false, false
			return m, nil
		}
		return m, splashTick()
	case connectedMsg:
		return m, m.connected(msg)
	case savedMsg:
		return m, m.setupSaved(msg)
	case statusMsg:
		if msg.err != nil {
			m.setNotice(msg.err.Error(), true)
		} else {
			m.status = msg.status
			if len(m.chat.entries) == 0 {
				m.renderHistory(true) // the empty chat names the agent
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
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.history, cmd = m.history.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if m.onboard != nil {
			return m, m.onboardKey(msg)
		}
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
		if m.onboard != nil {
			m.onboard.input, cmd = m.onboard.input.Update(msg)
			return m, cmd
		}
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	m.notice = ""
	if msg.String() == "ctrl+c" {
		return m.leave()
	}
	switch m.screen {
	case screenRequests:
		return m.handleRequestKey(msg)
	case screenPlugin:
		return m.handleScreenKey(msg)
	case screenHelp:
		if msg.String() == "esc" {
			m.screen = screenChat
		}
		return nil
	}
	switch msg.String() {
	case "pgup":
		m.history.PageUp()
		return nil
	case "pgdown":
		m.history.PageDown()
		return nil
	case "esc":
		return nil
	case "enter":
		return m.send()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

// handleRequestKey handles keys on the connection requests screen: up and
// down select an entry, left and right whether to accept or reject it, and
// enter decides.
func (m *Model) handleRequestKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.screen = screenChat
	case "up", "k":
		m.selectRequest(m.reqSel - 1)
	case "down", "j":
		m.selectRequest(m.reqSel + 1)
	case "left", "h":
		m.choice = accept
	case "right", "l":
		m.choice = reject
	case "enter":
		return m.decideRequest(m.choice == accept)
	}
	return nil
}

// The choices for an entry on the connection requests screen.
const (
	accept = iota
	reject
)

// leave quits the UI and stops what it still asks of the backend: a pending
// answer, with the peers it asks, and a decision on a connection request.
func (m *Model) leave() tea.Cmd {
	m.quit()
	return tea.Quit
}

func (m *Model) openRequests() tea.Cmd {
	m.screen = screenRequests
	m.reqSel, m.choice = 0, accept
	m.unreadReq = 0
	m.seenReq = m.trust.waiting()
	return nil
}

func (m *Model) selectRequest(i int) {
	if i != m.reqSel {
		m.choice = accept // what is decided is chosen anew for each entry
	}
	if m.trust.waiting() == 0 {
		m.reqSel = 0
		return
	}
	m.reqSel = min(max(i, 0), m.trust.waiting()-1)
}

func (m *Model) decideRequest(accept bool) tea.Cmd {
	if m.reqSel >= m.trust.waiting() {
		return nil
	}
	if m.reqSel >= len(m.trust.Incoming) {
		return m.decideProposal(m.trust.Proposals[m.reqSel-len(m.trust.Incoming)], accept)
	}
	pub := m.trust.Incoming[m.reqSel].PublicKey
	verb, failed := "declined", "rejecting failed"
	if accept {
		verb, failed = "accepted", "accepting failed"
		if m.trust.Incoming[m.reqSel].Proposed {
			verb = "sent" // to the peer, where a human decides
		}
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

// decideProposal accepts or refuses a change of the configuration the agent
// proposed.
func (m *Model) decideProposal(p plugin.Proposal, accept bool) tea.Cmd {
	return func() tea.Msg {
		if !accept {
			if err := m.backend.RefuseProposal(p.ID); err != nil {
				return actionMsg{err: err}
			}
			return actionMsg{done: "proposal refused, " + p.Field + " stays as it is"}
		}
		if err := m.backend.AcceptProposal(p.ID); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{done: p.Field + " changed"}
	}
}

func (m *Model) applyContacts(trust Contacts) {
	m.trust = trust
	if trust.waiting() > m.seenReq {
		m.unreadReq += trust.waiting() - m.seenReq
	}
	if m.screen == screenRequests {
		m.unreadReq = 0
	}
	m.seenReq = trust.waiting()
	m.selectRequest(m.reqSel)
}

// setup opens the setup of the node. The node keeps running until what the
// setup saved is there to start it with.
func (m *Model) setup() tea.Cmd {
	m.openSetup(!m.status.Configured)
	if m.onboard == nil {
		return nil
	}
	return m.animate()
}

func (m *Model) send() tea.Cmd {
	c := &m.chat
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}
	if isCommandLine(text) {
		return m.runCommand(text)
	}
	text = strings.TrimPrefix(text, "/") // of the two a message starts with
	if !m.status.Configured {
		m.setNotice("set up the node first: /setup", true)
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
