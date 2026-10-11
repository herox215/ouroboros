// Package tui is a terminal UI to control the local node and chat with its
// agent, which can ask the nodes on the network. The owner controls it with
// commands like /new, typed into the message input: see commands.go. Plugins
// add screens and commands to it: see plugin.Screen and plugin.Slash, and
// extend.go for how they are bound.
//
// Model is the root: it holds what every screen shares, the node it controls,
// the size of the window, the chat and which screen is shown, and hands the
// keys and the messages on to that screen. Each screen is a type of its own
// with its state, its keys and its view: modelScreen (/model),
// settingsScreen (/settings), sessionsScreen (/session), contextScreen
// (/context), requestsScreen (/pending), pluginScreen (what plugins added),
// splashScreen, and onboarding for the setup. A method of a screen takes the
// Model for what it shares.
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

// Fallback is a model of an access the node falls back on, see
// Backend.Fallbacks.
type Fallback = plugin.Fallback

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
	// Steer gives text to the answer on its way in the session id: it is
	// queued until the answer takes it, after the tools it waits for or as
	// the next message. Without an answer on its way it is Ask.
	Steer(ctx context.Context, id, text string) error
	// Unsteer takes back the last message that is queued in the session id
	// and returns it, "" if none is.
	Unsteer(ctx context.Context, id string) (string, error)
	// SavePicture keeps a picture the owner pasted into the chat as a file
	// of the profile, of the kind ext like "png", and returns its path.
	SavePicture(data []byte, ext string) (string, error)
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
	// Fallbacks are what the node goes on with, in their order, when the
	// access it answers the owner with cannot for now, and ToggleFallback
	// makes model of the access the last of them, or none of them if it is
	// one: it returns them as they are then.
	Fallbacks() ([]Fallback, error)
	ToggleFallback(access, model string) ([]Fallback, error)
	// SetPeerModel makes model of the access what the node answers peers
	// with, whatever it answers the owner with; with the access "", what it
	// answers the owner with again. Status says what it is.
	SetPeerModel(access, model string) error
	// SetDebug turns the debug mode of the node on or off: in it, the log
	// of the node tells much more of every call of the LLM. Status says
	// whether it is on.
	SetDebug(on bool) error
	// SetEffort makes effort, one of plugin.Efforts, how much the model
	// thinks before it answers the owner in a session that has none of its
	// own. Status says what it is.
	SetEffort(effort string) error
	// SetSessionEffort makes effort how much the model thinks in the
	// session id, whatever the node says, from its next call on: an Event
	// of the kind plugin.SessionEffort tells it.
	SetSessionEffort(ctx context.Context, id, effort string) error
	// Efforts returns the efforts the model the node answers with takes,
	// as models.dev lists them, from the least to the most, and whether it
	// lists the model at all: then /effort offers only those.
	Efforts() ([]string, bool)
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
	// Options and Recommended are of a question the agent asks the owner,
	// plugin.SessionAsked, whose Text is the question: the answers to
	// choose from, and the one of them the agent would choose, "" for none.
	// Once Done, Text is what the owner answered, "" if they did not.
	Options     []string
	Recommended string
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
	screenEffort   // the slider of the effort, for /effort
	screenKeys     // the keys of the chat, for /keys and ctrl+k
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
	pending   bool                // request submitted or answer on its way: what is sent meanwhile is queued
	queued    []string            // the messages that wait for the answer on its way to take them
	running   bool                // the node has said an answer is on its way
	doing     string              // what the agent is waiting for, like "asking beta"
	asking    *asking             // what the agent asks the owner and waits for; nil without a question
	since     time.Time           // when it began to wait for that
	art       int                 // which of arts a wait is shown with: the next for every message
	round     int                 // the entry of the round of the loop that is on, counted from 1; 0 for none yet
	context   int                 // how long its context is, see Event.Context
	cache     plugin.SessionCache // what the provider had cached, see Event.Cache
	title     string              // named for its topic, else the first message of the owner
	effort    string              // how much the model thinks in it, once efforted says it has its own
	efforted  bool
	last      time.Time
	draft     string // what is typed in the input for it while another one is shown
	watching  bool   // its events come in, or are asked for
	// peer is the peer a conversation of a peer with the agent is with: it
	// is only to read. "" for a session of the owner.
	peer string
	// images are the files of the pictures pasted into it, by their labels:
	// the first is [Image_1].
	images []string
}

// Model is the UI state.
type Model struct {
	profiles *profileViews

	// profileAt is which of the profileOf profiles of the node this UI shows:
	// 0 for a UI of one. among are the UIs of all of them, of which one is
	// shown: nil for a UI of one.
	profileAt, profileOf int
	among                *profileViews

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

	resuming *conversation // the resumed chat waits for its history before showing empty artwork
	restarts string        // the binary the UI quit for ouro to start anew from

	sessions sessionsScreen // the list of the sessions, for /session
	ticks    int            // of the spinner, which the sessions screen animates with

	requests requestsScreen // what waits for a decision, for /pending
	screen   screen
	commands []command    // of the UI, then of the plugins
	suggest  int          // the command selected of those suggested while one is typed
	entered  []string     // what the owner entered, the latest last, for up and down
	recall   recall       // where up and down are in it
	plugins  pluginScreen // the screens the plugins contributed, and the one that is open
	choice   int          // the action selected: accept or reject, or one of a screen

	picker modelScreen  // the models of the accesses, for /model
	effort effortScreen // the slider of the effort, for /effort

	context contextScreen // the context of the conversation, for /context

	config settingsScreen // the configuration, for /settings

	width, height int
	splash        splashScreen // the splash, and the frames of what moves
	onboard       *onboarding  // the setup, while it is open
	light         bool         // the background of the terminal is light
	input         textarea.Model
	history       viewport.Model
	glide         *glide     // the viewport on its way to a line, if one is
	selected      *selection // what the mouse selected of the history
	pressed       *spot      // where the mouse went down, before the drag began
	spinner       spinner.Model

	// nameAnimation is how the name of the agent moves in an empty chat.
	nameAnimation nameAnimation
}

// New returns the UI for backend, with the screens and commands the plugins
// contributed. A contribution whose name is taken is left out, which the UI
// says in its footer when it opens: see bind.
func New(backend Backend, screens []plugin.Screen, slashes []plugin.Slash) *Model {
	input := textarea.New()
	input.Placeholder = "Keep on dreaming"
	input.ShowLineNumbers = false
	input.Prompt = ""
	input.SetHeight(inputHeight)
	input.SetStyles(inputStyles(false))
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"))

	history := viewport.New()
	history.KeyMap = viewport.KeyMap{} // keys go to the input; scrolling is handled explicitly

	ctx, quit := context.WithCancel(context.Background())
	m := &Model{
		backend: backend,
		ctx:     ctx,
		quit:    quit,
		now:     time.Now,
		chats:   map[string]*conversation{},
		splash:  splashScreen{show: true},
		input:   input,
		history: history,
		spinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
	m.input.Focus() // the input is where everything is typed, commands too
	m.chat = m.conversation(plugin.NewID())
	var problems []string
	m.plugins.screens, slashes, problems = bind(ownNames(), screens, slashes)
	m.commands = append(m.commands, own...)
	for i, s := range m.plugins.screens {
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
	if m.profiles != nil {
		return m.profiles.init()
	}
	m.splash.start = m.now()
	if s, err := m.backend.Status(); err == nil && !s.Configured {
		m.splash.show = false
		m.openSetup(true)
	}
	m.splash.animating = true
	return tea.Batch(m.goOnline, m.loadContacts, m.loadSessions, m.watch(m.chat), tick(), m.spinner.Tick, tea.RequestBackgroundColor, splashTick())
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
		return tea.Batch(m.refreshStatus, m.loadContacts, m.loadSessions, m.plugins.load(m))
	}
	return tea.Batch(m.refreshStatus, m.loadContacts, m.loadSessions)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer func() {
		if m.profiles == nil {
			m.syncNameAnimation()
		}
	}()
	if m.profiles != nil {
		return m, m.profiles.update(msg)
	}
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
		return m, m.splash.update(msg.at)
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
			m.requests.apply(m, msg.trust)
		}
	case modelsMsg:
		m.picker.loaded(m, msg)
	case fallbacksMsg:
		m.picker.fallbacksSet(m, msg)
	case settingsMsg:
		m.config.loaded(m, msg)
	case changedMsg:
		return m, m.config.changed(m, msg)
	case contentsMsg:
		m.context.loaded(m, msg)
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
	case unsteeredMsg:
		m.unsteered(msg)
	case pastedMsg:
		m.pasted(msg)
	case sessionsMsg:
		return m, m.sessionsLoaded(msg)
	case deletedMsg:
		return m, m.deleted(msg)
	case peersMsg:
		m.sessions.peersLoaded(m, msg)
	case readMsg:
		m.read(msg)
	case bodyMsg:
		if m.screen == screenPlugin && m.plugins.open == msg.screen {
			m.plugins.body = msg.body
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
			if a := &m.nameAnimation; a.active || !a.fading.IsZero() {
				a.elapsed = m.now().Sub(a.start)
				if !a.active && a.faded(m.now()) == 0 {
					a.fading = time.Time{}
				}
				m.renderHistory(false)
			}
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.MouseClickMsg, tea.MouseMotionMsg, tea.MouseReleaseMsg:
		return m, m.mouse(msg.(tea.MouseMsg))
	case tea.MouseWheelMsg:
		if m.inDialog() {
			return m, nil
		}
		if m.screen == screenContext {
			m.wheel(&m.context.port, msg)
			return m, nil
		}
		m.wheel(&m.history, msg)
		return m, nil
	case scrollMsg:
		// A frame of a glide: see scrollBy.
		if m.glideOn(msg.at) {
			return m, scrollTick()
		}
		return m, nil
	case tea.KeyPressMsg:
		if m.onboard != nil {
			return m, m.onboardKey(msg)
		}
		if m.splash.show {
			return m, m.splash.handleKey(m, msg)
		}
		return m, m.handleKey(msg)
	default:
		var cmd tea.Cmd
		if m.onboard != nil {
			m.onboard.input, cmd = m.onboard.input.Update(msg)
			return m, cmd
		}
		if m.config.editing != nil {
			m.config.input, cmd = m.config.input.Update(msg)
			return m, cmd
		}
		if m.screen == screenContext && m.context.typing {
			return m, m.context.typed(m, msg) // what is pasted into the search, and the blink of its cursor
		}
		if pasted, ok := msg.(tea.PasteMsg); ok {
			return m, m.paste(pasted.Content)
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
		return m.requests.handleKey(m, msg)
	case screenPlugin:
		return m.plugins.handleKey(m, msg)
	case screenModel:
		return m.picker.handleKey(m, msg)
	case screenContext:
		return m.context.handleKey(m, msg)
	case screenSettings:
		return m.config.handleKey(m, msg)
	case screenSessions:
		return m.sessions.handleKey(m, msg)
	case screenEffort:
		return m.effort.handleKey(m, msg)
	case screenHelp, screenKeys:
		if msg.String() == "esc" {
			m.screen = screenChat
		}
		return nil
	}
	if cmd, ok := m.handleQuestionKey(msg); ok {
		return cmd
	}
	if m.handleRecallKey(msg) {
		return nil
	}
	if cmd, ok := m.handleSuggestionKey(msg); ok {
		return cmd
	}
	switch msg.String() {
	case "ctrl+s":
		return m.openSessions() // what is typed stays with the session shown
	case "ctrl+k":
		return m.openKeys() // what is typed stays in the input
	case "pgup":
		return m.scrollBy(&m.history, -m.history.Height())
	case "pgdown":
		return m.scrollBy(&m.history, m.history.Height())
	case "esc":
		// In the chat there is nothing to go back from: it stops the answer
		// on its way here, as /cancel does.
		if m.chat.pending {
			return m.cancel()
		}
		return nil
	case "enter":
		return m.send()
	case "ctrl+v":
		return m.pasteClipboard() // a picture too, which the input does not paste
	case "up":
		if m.input.Value() == "" && len(m.chat.queued) > 0 {
			return m.unsteer()
		}
	}
	var cmd tea.Cmd
	typed := m.input.Value()
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != typed {
		m.suggest = 0 // other commands fit now
		m.chat.asking.write()
	}
	return cmd
}

// requestsScreen is the state of the screen of what waits for a decision:
// the connection requests and the proposals.
type requestsScreen struct {
	trust  Contacts // what waits
	sel    int      // the entry selected
	unread int      // what came since the screen was last open
	seen   int      // how much waited when it was last looked at
}

// handleRequestKey handles keys on the connection requests screen: up and
// down select an entry, left and right whether to accept or reject it, and
// enter decides.
func (r *requestsScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.screen = screenChat
	case "up", "k":
		r.selectAt(m, r.sel-1)
	case "down", "j":
		r.selectAt(m, r.sel+1)
	case "left", "h":
		m.choice = accept
	case "right", "l":
		m.choice = reject
	case "enter":
		return r.decide(m, m.choice == accept)
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
	m.requests.sel, m.choice = 0, accept
	m.requests.unread = 0
	m.requests.seen = m.requests.trust.waiting()
	return nil
}

func (r *requestsScreen) selectAt(m *Model, i int) {
	if i != r.sel {
		m.choice = accept // what is decided is chosen anew for each entry
	}
	if r.trust.waiting() == 0 {
		r.sel = 0
		return
	}
	r.sel = cycle(i, 0, r.trust.waiting())
}

func (r *requestsScreen) decide(m *Model, accept bool) tea.Cmd {
	if r.sel >= r.trust.waiting() {
		return nil
	}
	if r.sel >= len(r.trust.Incoming) {
		return r.decideProposal(m, r.trust.Proposals[r.sel-len(r.trust.Incoming)], accept)
	}
	pub := r.trust.Incoming[r.sel].PublicKey
	verb, failed := "declined", "rejecting failed"
	if accept {
		verb, failed = "accepted", "accepting failed"
		if r.trust.Incoming[r.sel].Proposed {
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
func (r *requestsScreen) decideProposal(m *Model, p plugin.Proposal, accept bool) tea.Cmd {
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

func (r *requestsScreen) apply(m *Model, trust Contacts) {
	r.trust = trust
	if trust.waiting() > r.seen {
		r.unread += trust.waiting() - r.seen
	}
	if m.screen == screenRequests {
		r.unread = 0
	}
	r.seen = trust.waiting()
	r.selectAt(m, r.sel)
}

// setup opens the setup of the node. The node keeps running until what the
// setup saved is there to start it with.
func (m *Model) setup() tea.Cmd {
	m.openSetup(!m.status.Configured)
	if m.onboard == nil {
		return nil
	}
	return m.splash.animate()
}

func (m *Model) send() tea.Cmd {
	c := m.chat
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return nil
	}
	m.remember(text)
	if isCommandLine(text) {
		return m.runCommand(text)
	}
	text = c.expand(strings.TrimPrefix(text, "/")) // of the two a message starts with; with the paths of its pictures
	if !m.status.Configured {
		m.setNotice("set up the node first: /setup", true)
		return nil
	}
	if c.peer != "" {
		m.setNotice("this is how "+c.peer+" and "+m.agentName()+" talked: it is only to read · /new", true)
		return nil
	}
	id := c.contextID
	if c.pending {
		// The answer on its way takes it as soon as it can: until then it is
		// queued, as the node says.
		m.input.Reset()
		return func() tea.Msg { return askedMsg{id, text, m.backend.Steer(m.ctx, id, text)} }
	}
	// The message shows once the node says it is on its way; the agent is
	// thinking from now on.
	c.pending, c.round = true, 0
	c.art++
	m.input.Reset()
	m.renderHistory(true)
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
	c.asking.unsent()
	if c == m.chat {
		if m.input.Value() == "" {
			m.input.SetValue(c.labelled(msg.text))
		}
		m.renderHistory(m.history.AtBottom())
	} else if c.draft == "" {
		c.draft = c.labelled(msg.text)
	}
	m.setNotice(msg.err.Error(), true)
}

// unsteeredMsg is the last message that was queued in a session, taken back:
// "" if none was.
type unsteeredMsg struct {
	id, text string
	err      error
}

// unsteer takes back the last message that is queued in the session shown.
func (m *Model) unsteer() tea.Cmd {
	id := m.chat.contextID
	return func() tea.Msg {
		text, err := m.backend.Unsteer(m.ctx, id)
		return unsteeredMsg{id, text, err}
	}
}

// unsteered puts a message that was taken back into the input, before what
// was typed meanwhile: it is the owner's again, to change or to drop.
func (m *Model) unsteered(msg unsteeredMsg) {
	if msg.err != nil {
		m.setNotice(msg.err.Error(), true)
		return
	}
	if msg.text == "" {
		return // the answer took it meanwhile
	}
	c := m.chats[msg.id]
	text := c.labelled(msg.text)
	if c != m.chat {
		c.draft = strings.TrimSuffix(text+"\n"+c.draft, "\n")
		return
	}
	m.input.SetValue(strings.TrimSuffix(text+"\n"+m.input.Value(), "\n"))
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
func (m *Model) Restarts() (exe string, ok bool) {
	if m.profiles != nil {
		for _, model := range m.profiles.models {
			if exe, ok := model.Restarts(); ok {
				return exe, true
			}
		}
		return "", false
	}
	return m.restarts, m.restarts != ""
}

// Current is the ID of the session the UI shows.
// A conversation of a peer is none: then it is a new one.
func (m *Model) Current() string {
	if m.profiles != nil {
		return m.profiles.current().Current()
	}
	if m.chat.peer != "" {
		return plugin.NewID()
	}
	return m.chat.contextID
}

// Resume makes the UI show the session id, as another UI did before ouro
// started anew, without the splash: it is to be called before the UI is
// shown.
func (m *Model) Resume(id string) {
	if m.profiles != nil {
		m.profiles.current().Resume(id)
		return
	}
	delete(m.chats, m.chat.contextID)
	m.chat = m.conversation(id)
	m.resuming = m.chat
	m.splash.show = false
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
