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

// Models is what the provider of the node offers, as Models of a Backend
// lists them for the model screen (/model).
type Models = plugin.SetupModels

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
	// contextID. On the way it reports what it is waiting for, in step, and
	// what the agent says along with the tools it asks for, in say: the whole
	// loop, not only its end. say comes once for every round of the loop that
	// asks for tools, with "" if the agent said nothing, before its steps.
	Ask(ctx context.Context, contextID, text string, step func(Step), say func(string)) (string, error)
	// Context returns how long the context of the conversation contextID is,
	// in bytes: what the last call of the LLM in it sent. 0 if none was made.
	Context(contextID string) int
	// Contents returns what the next call of the LLM in the conversation
	// contextID is sent, message by message. With an error it returns what
	// it has of it.
	Contents(contextID string) ([]Message, error)
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
	// Models lists the models the configured provider offers, for the model
	// screen (/model). It fails with plugin.ErrNotSetUp for a node that is
	// not set up, and with plugin.ErrKeyRejected if the provider refuses the
	// key of the node.
	Models(ctx context.Context) (Models, error)
	// SetModel saves model as the model of the node: the agents answer with
	// it from the next message on.
	SetModel(model string) error
	// Restarting reports whether ouro is to be started anew, as a tool asked
	// on the way to the answer that came in: the UI then quits, see Restarts.
	Restarting() bool
}

// Step reports what the agent is waiting for on the way to its answer, like
// "asking beta". It is reported twice: once on its own, when the agent starts
// to wait, and again with Done set, when it no longer does. Text may say more
// the second time, like how long the call took.
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
	screenModel  // the models of the provider, for /model
	screenContext
)

// entry is one message in the conversation: from the user, from the agent, or
// one round of the loop to its answer: what the agent said, if anything, and
// the tool calls it asked for.
type entry struct {
	mine   bool
	text   string
	at     time.Time
	failed bool
	calls  []call // of a round of the loop; none for a message
}

// call is a tool call of the loop as the history shows it.
type call struct {
	text string // like "asking beta"; once done, with what became of it: "asking beta ✓ 1.2s"
	done bool
}

// conversation is the chat with the agent of this node.
type conversation struct {
	contextID string
	entries   []entry
	pending   bool               // waiting for a reply; one message at a time keeps the history in order
	doing     string             // what the agent is waiting for, like "asking beta"
	round     int                // the entry of the round of the loop that is on, counted from 1; 0 for none yet
	cancel    context.CancelFunc // stops the answer that is pending
	context   int                // how long its context is, see Backend.Context
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

	restarts bool // the UI quit for ouro to start anew

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

	models    Models // what the provider offers, once /model asked for them
	modelSel  int    // the model selected of models.Chat
	modelBusy string // what the model screen waits for, like "listing models"
	modelErr  error  // why the models are not there

	contents viewport.Model // the context of the conversation, for /context

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
	input.SetStyles(inputStyles(false))
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
	// sayMsg is what the agent says along with the tools it asks for; next
	// delivers what follows.
	sayMsg struct {
		contextID, text string
		next            <-chan tea.Msg
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
		m.input.SetStyles(inputStyles(m.light))
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
	case modelsMsg:
		m.modelsLoaded(msg.models, msg.err)
	case contentsMsg:
		m.contentsLoaded(msg)
	case stepMsg:
		m.step(msg)
		return m, func() tea.Msg { return <-msg.next }
	case sayMsg:
		m.say(msg)
		return m, func() tea.Msg { return <-msg.next }
	case replyMsg:
		return m, m.receive(msg)
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
		if m.screen == screenContext {
			m.contents, cmd = m.contents.Update(msg)
			return m, cmd
		}
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
	case screenModel:
		return m.handleModelKey(msg)
	case screenContext:
		return m.handleContextKey(msg)
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
	c.pending, c.round = true, 0
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
			}, func(what string) {
				select {
				case events <- sayMsg{contextID, what, events}:
				case <-ctx.Done():
				}
			})
			events <- replyMsg{contextID, reply, err, ctx.Err() != nil}
		}()
		return <-events
	}
}

// step notes what the agent is waiting for while the answer is pending. A
// call that starts goes into the entry of its round, which a round the agent
// said nothing of gets now; one that ended takes the place of its start,
// with what became of it.
func (m *Model) step(msg stepMsg) {
	c := &m.chat
	if c.contextID != msg.contextID {
		return // step of a conversation that was reset
	}
	if c.round == 0 {
		c.add(entry{at: m.now()})
		c.round = len(c.entries)
	}
	c.context = m.backend.Context(c.contextID)
	e := &c.entries[c.round-1]
	if !msg.step.Done {
		e.calls = append(e.calls, call{text: msg.step.Text})
	} else if i := e.started(msg.step.Text); i >= 0 {
		e.calls[i] = call{text: msg.step.Text, done: true}
	}
	c.doing = e.waitingFor()
	m.renderHistory(m.history.AtBottom())
}

// started returns the call of e that the call ended, which done tells of,
// started as: the last that is not done and whose text done begins with. A
// script runs calls within its own, which end before it does.
func (e *entry) started(done string) int {
	for i := len(e.calls) - 1; i >= 0; i-- {
		c := e.calls[i]
		if !c.done && (done == c.text || strings.HasPrefix(done, c.text+" ")) {
			return i
		}
	}
	return -1
}

// waitingFor is what the last call of e that has not ended is, "" if all have.
func (e *entry) waitingFor() string {
	for i := len(e.calls) - 1; i >= 0; i-- {
		if !e.calls[i].done {
			return e.calls[i].text
		}
	}
	return ""
}

// say begins a round of the loop: what the agent says along with the tools
// it asks for, which is not the answer, becomes part of the conversation, and
// the calls of those tools go below these words. A round the agent said
// nothing of gets its entry with its first call that is shown.
func (m *Model) say(msg sayMsg) {
	c := &m.chat
	if c.contextID != msg.contextID {
		return
	}
	c.round, c.doing = 0, ""
	c.context = m.backend.Context(c.contextID)
	if msg.text != "" {
		c.add(entry{text: msg.text, at: m.now()})
		c.round = len(c.entries)
	}
	m.renderHistory(m.history.AtBottom())
}

// add appends an entry to the conversation.
func (c *conversation) add(e entry) {
	c.entries = append(c.entries, e)
}

// receive shows the reply that came in. When a tool asked on the way for
// ouro to start anew, the UI quits for it.
func (m *Model) receive(msg replyMsg) tea.Cmd {
	c := &m.chat
	if c.contextID != msg.contextID || msg.cancelled {
		return nil // reply to a conversation that was reset, or stopped by quitting
	}
	c.cancel() // the answer is in; release its context
	c.pending, c.doing, c.cancel = false, "", nil
	c.round = 0 // the loop of this answer is over
	c.context = m.backend.Context(c.contextID)
	e := entry{text: msg.text, at: m.now()}
	if msg.err != nil {
		e.text, e.failed = msg.err.Error(), true
	}
	c.add(e)
	m.renderHistory(m.history.AtBottom())
	if m.backend.Restarting() {
		m.restarts = true
		return m.leave()
	}
	return nil
}

// Restarts reports whether the UI quit for ouro to start anew: what it
// showed is in Chat, to go on with by Resume.
func (m *Model) Restarts() bool { return m.restarts }

// Chat is the conversation of a UI, to go on with in another one.
type Chat struct {
	ContextID string  `json:"context_id"`
	Entries   []Entry `json:"entries"`
}

// Entry is a message of a Chat: from the owner, or from the agent. Calls are
// the tool calls of a round of the loop to an answer, as they ended; such an
// entry carries no text when the agent had nothing to say along with them.
type Entry struct {
	Mine   bool      `json:"mine,omitempty"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
	Failed bool      `json:"failed,omitempty"`
	Calls  []string  `json:"calls,omitempty"`
}

// Chat returns the conversation the UI shows.
func (m *Model) Chat() Chat {
	c := Chat{ContextID: m.chat.contextID, Entries: make([]Entry, len(m.chat.entries))}
	for i, e := range m.chat.entries {
		c.Entries[i] = Entry{e.mine, e.text, e.at.Round(0), e.failed, nil} // the reading of the clock is of this process
		for _, call := range e.calls {
			c.Entries[i].Calls = append(c.Entries[i].Calls, call.text)
		}
	}
	return c
}

// Resume makes the UI go on with the conversation c of another one, without
// the splash: it is to be called before the UI is shown.
func (m *Model) Resume(c Chat) {
	m.chat = conversation{contextID: c.ContextID}
	for _, e := range c.Entries {
		resumed := entry{e.Mine, e.Text, e.At, e.Failed, nil}
		for _, text := range e.Calls {
			resumed.calls = append(resumed.calls, call{text: text, done: true}) // a call that had not ended is not waited for any more
		}
		m.chat.entries = append(m.chat.entries, resumed)
	}
	m.showSplash = false
	m.renderHistory(true)
	m.setNotice("restarted with the new build", false)
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
