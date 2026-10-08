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

// Models are the models of one access of the node, as Models of a Backend
// lists them for the model screen (/model).
type Models = plugin.AccessModels

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
	// Ask lets the agent of this node answer text in the session id, and
	// returns once the answer is on its way: what happens on the way comes
	// from Watch. It fails with plugin.ErrBusy while an answer is on its way
	// in the session.
	Ask(ctx context.Context, id, text string) error
	// Watch returns what happened in the session id so far, and what
	// happens from now on, until ctx ends or the node stops: the whole loop
	// of every answer, not only its end.
	Watch(ctx context.Context, id string) (past []Event, live <-chan Event, err error)
	// Cancel stops the answer on its way in the session id.
	Cancel(ctx context.Context, id string) error
	// Sessions lists the sessions of the node that have a message, the one
	// with the latest event first.
	Sessions(ctx context.Context) ([]plugin.SessionInfo, error)
	// Delete removes the session id from the node, with its history, or the
	// conversation of a peer id as Peers names it. It fails with
	// plugin.ErrBusy while an answer is on its way in it.
	Delete(ctx context.Context, id string) error
	// Peers lists the conversations of peers with the agent of the node,
	// the latest first: only to read and delete.
	Peers(ctx context.Context) ([]plugin.SessionInfo, error)
	// Read returns what happened in the conversation of a peer id.
	Read(ctx context.Context, id string) ([]Event, error)
	// Contents returns what the next call of the LLM in the session id is
	// sent, message by message. With an error it returns what it has of it.
	Contents(ctx context.Context, id string) ([]Message, error)
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
	// Models lists the models of every access of the node, for the model
	// screen (/model): an access that does not answer comes with why, and
	// with what it listed the last time. It fails with plugin.ErrNotSetUp
	// for a node that is not set up.
	Models(ctx context.Context) ([]Models, error)
	// SetModel makes model of the access what the node answers with: the
	// agents do from the next message on. It reports whether they answer
	// through another access than before.
	SetModel(access, model string) (switched bool, err error)
	// SetDebug turns the debug mode of the node on or off: in it, the log
	// of the node tells much more of every call of the LLM. Status says
	// whether it is on.
	SetDebug(on bool) error
	// Settings returns the configuration of the node for the settings
	// screen (/settings), without its keys.
	Settings() (Settings, error)
	// ChangeSetting makes one change of the configuration, once the node
	// checked it, and says from when it holds.
	ChangeSetting(s Setting) (string, error)
}

// Event is what happened in a session, as the UI shows it: see
// plugin.SessionEvent, whose Kind it has.
type Event struct {
	Kind plugin.SessionKind
	// Text is the message, what the agent said along with the tools it asks
	// for, the answer, why it failed, or the binary ouro starts anew from.
	// Of a call of a tool it is what is shown of it, like "asking beta", ""
	// for nothing; once Done, it may say more, like how long the call took.
	Text string
	Done bool
	At   time.Time
	// Context is how long the context of the session is: what the last call
	// of the LLM in it sent, in bytes. 0 if none was made.
	Context int
	// Cache is how much of what the LLM was sent in the session the
	// provider had cached.
	Cache plugin.SessionCache
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
	screenSettings // the configuration, for /settings
	screenSessions // the sessions, for /session
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

// conversation is a session of the chat with the agent of this node, as far
// as its events came in.
type conversation struct {
	contextID string
	entries   []entry
	pending   bool                // request submitted or answer on its way: one message at a time
	running   bool                // the node has said an answer is on its way
	doing     string              // what the agent is waiting for, like "asking beta"
	art       int                 // which of arts a wait is shown with: the next for every message
	round     int                 // the entry of the round of the loop that is on, counted from 1; 0 for none yet
	context   int                 // how long its context is, see Event.Context
	cache     plugin.SessionCache // what the provider had cached, see Event.Cache
	title     string              // named for its topic, else the first message of the owner
	last      time.Time
	draft     string // what is typed in the input for it while another one is shown
	watching  bool   // its events come in, or are asked for
	// peer is the peer a conversation of a peer with the agent is with: it
	// is only to read. "" for a session of the owner.
	peer string
}

// Model is the UI state.
type Model struct {
	backend Backend
	now     func() time.Time
	// ctx ends when the user quits; what the UI asks of the backend stops with it.
	ctx  context.Context
	quit context.CancelFunc

	status    Status
	chat      *conversation            // the session shown
	chats     map[string]*conversation // every session the UI knows, by its ID
	notice    string
	noticeErr bool

	restarts string // the binary the UI quit for ouro to start anew from

	sessionSel int             // the session selected on the sessions screen
	deleting   *conversation   // the session that d deletes when it is pressed again
	peerTab    bool            // the sessions screen lists the conversations of peers
	peers      []*conversation // the conversations of peers, as far as they were listed
	ticks      int             // of the spinner, which the sessions screen animates with

	trust     Contacts
	screen    screen
	commands  []command       // of the UI, then of the plugins
	suggest   int             // the command selected of those suggested while one is typed
	screens   []plugin.Screen // of the plugins, as far as they got their command
	open      int             // the screen of a plugin that is shown
	body      body            // what it shows
	reqSel    int
	choice    int // the action selected: accept or reject, or one of a screen
	unreadReq int
	seenReq   int

	models    []Models // what the accesses offer, once /model asked for them
	choices   []choice // the models of them all, to select from
	modelSel  int      // the one selected of choices
	modelBusy string   // what the model screen waits for, like "listing models"
	modelErr  error    // why the models are not there

	contents viewport.Model // the context of the conversation, for /context
	loaded   *contentsMsg   // the context that came in, to wrap again when the window changes

	settings    Settings        // the configuration, for /settings
	settingsErr error           // why it is not there
	rows        []row           // what the settings screen shows of it
	category    int             // the category selected, of sections
	opened      bool            // its rows are shown, not the list of categories
	settingsSel int             // the row selected, of those of the category
	settingsTop int             // the line of a category the settings screen starts at
	editing     *edit           // the text that is typed, if one is
	editInput   textinput.Model // where it is typed

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
	selected      *selection // what the mouse selected of the history
	pressed       *spot      // where the mouse went down, before the drag began
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
	history.KeyMap = viewport.KeyMap{} // keys go to the input; scrolling is handled explicitly

	ctx, quit := context.WithCancel(context.Background())
	m := &Model{
		backend:    backend,
		ctx:        ctx,
		quit:       quit,
		now:        time.Now,
		chats:      map[string]*conversation{},
		showSplash: true,
		input:      input,
		history:    history,
		spinner:    spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
	m.input.Focus() // the input is where everything is typed, commands too
	m.chat = m.conversation(plugin.NewID())
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
	return tea.Batch(m.goOnline, m.loadContacts, m.loadSessions, m.watch(m.chat), tick(), m.spinner.Tick, tea.RequestBackgroundColor, splashTick())
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

// refresh reloads the node status, the connection requests, the sessions
// and the screen of a plugin that is open.
func (m *Model) refresh() tea.Cmd {
	if m.screen == screenPlugin {
		return tea.Batch(m.refreshStatus, m.loadContacts, m.loadSessions, m.loadBody())
	}
	return tea.Batch(m.refreshStatus, m.loadContacts, m.loadSessions)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// The line below the footer comes and goes with what is pending, and
	// with it the room for everything above.
	defer func(rows int) {
		if m.pendingRows() != rows {
			m.layout()
		}
	}(m.pendingRows())
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
	case loginMsg:
		return m, m.loggedIn(msg)
	case accessesMsg:
		m.managed(msg)
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
	case settingsMsg:
		m.settingsLoaded(msg)
	case changedMsg:
		return m, m.changed(msg)
	case contentsMsg:
		m.contentsLoaded(msg)
	case watchedMsg:
		return m, m.watched(msg)
	case eventMsg:
		return m, m.event(msg)
	case unwatchedMsg:
		return m, m.unwatched(msg.id)
	case rewatchMsg:
		return m, m.watch(m.chats[msg.id])
	case askedMsg:
		m.asked(msg)
	case sessionsMsg:
		return m, m.sessionsLoaded(msg)
	case deletedMsg:
		return m, m.deleted(msg)
	case peersMsg:
		m.peersLoaded(msg)
	case readMsg:
		m.read(msg)
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
		if msg.ID == m.spinner.ID() {
			m.ticks++
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		return m, m.mouse(msg.(tea.MouseMsg))
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
		if m.editing != nil {
			m.editInput, cmd = m.editInput.Update(msg)
			return m, cmd
		}
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	m.notice = ""
	m.selected, m.pressed = nil, nil
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
	case screenSettings:
		return m.handleSettingsKey(msg)
	case screenSessions:
		return m.handleSessionsKey(msg)
	case screenHelp:
		if msg.String() == "esc" {
			m.screen = screenChat
		}
		return nil
	}
	if cmd, ok := m.handleSuggestionKey(msg); ok {
		return cmd
	}
	switch msg.String() {
	case "ctrl+s":
		return m.openSessions() // what is typed stays with the session shown
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
	typed := m.input.Value()
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != typed {
		m.suggest = 0 // other commands fit now
	}
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

// leave quits the UI and stops what it still asks of the backend, such as a
// decision on a connection request. The answers on their way are the
// node's: they go on.
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
	c := m.chat
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
	if c.peer != "" {
		m.setNotice("this is how "+c.peer+" and "+m.agentName()+" talked: it is only to read · /new", true)
		return nil
	}
	if c.pending {
		m.setNotice("wait for the reply first", true)
		return nil
	}
	// The message shows once the node says it is on its way; the agent is
	// thinking from now on.
	c.pending, c.round = true, 0
	c.art++
	m.input.Reset()
	m.renderHistory(true)
	id := c.contextID
	ask := func() tea.Msg { return askedMsg{id, text, m.backend.Ask(m.ctx, id, text)} }
	return tea.Batch(ask, m.watch(c))
}

// askedMsg is what the backend said to a message: whether the answer is on
// its way.
type askedMsg struct {
	id, text string
	err      error
}

// asked takes what the backend said to a message. One that is not on its
// way goes back into the input, unless something else was typed meanwhile.
func (m *Model) asked(msg askedMsg) {
	if msg.err == nil {
		return
	}
	c := m.chats[msg.id]
	c.pending = c.running
	if c == m.chat {
		if m.input.Value() == "" {
			m.input.SetValue(msg.text)
		}
		m.renderHistory(m.history.AtBottom())
	} else if c.draft == "" {
		c.draft = msg.text
	}
	m.setNotice(msg.err.Error(), true)
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

// add appends an entry to the conversation.
func (c *conversation) add(e entry) {
	c.entries = append(c.entries, e)
}

// Restarts reports whether the UI quit for ouro to start anew, and from
// which binary: the session it showed is Current, to go on with by Resume.
func (m *Model) Restarts() (exe string, ok bool) { return m.restarts, m.restarts != "" }

// Current is the ID of the session the UI shows.
// A conversation of a peer is none: then it is a new one.
func (m *Model) Current() string {
	if m.chat.peer != "" {
		return plugin.NewID()
	}
	return m.chat.contextID
}

// Resume makes the UI show the session id, as another UI did before ouro
// started anew, without the splash: it is to be called before the UI is
// shown.
func (m *Model) Resume(id string) {
	delete(m.chats, m.chat.contextID)
	m.chat = m.conversation(id)
	m.showSplash = false
	m.renderHistory(true)
	m.setNotice("restarted with the new build", false)
}

// newConversation starts a new session and shows it. The session that was
// shown goes on: an answer on its way there is given all the same, see
// /session.
func (m *Model) newConversation() tea.Cmd {
	m.chat.draft = m.input.Value()
	m.chat = m.conversation(plugin.NewID())
	m.input.Reset()
	m.renderHistory(true)
	m.setNotice("new conversation", false)
	return m.watch(m.chat)
}

// agentName is how the agent of this node is called in the chat.
func (m *Model) agentName() string {
	return cmp.Or(m.status.Name, "your agent")
}

func (m *Model) setNotice(text string, isErr bool) {
	m.notice, m.noticeErr = text, isErr
}
