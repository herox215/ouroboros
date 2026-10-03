// Package tui is a terminal UI to control the local node and chat with its
// agent, which can ask the nodes on the network.
package tui

import (
	"cmp"
	"context"
	"os/exec"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"ouroboros/internal/a2a"
	"ouroboros/internal/contacts"
	"ouroboros/internal/discovery"
)

// refreshInterval is how often node status and the network are checked.
const refreshInterval = 10 * time.Second

// updateInterval is how often the newest version is looked up.
const updateInterval = time.Hour

// Status describes the local node.
type Status struct {
	Configured bool
	Running    bool
	ID         string
	Name       string
	Model      string
	Port       int
}

// Contacts is the local node's contact list.
type Contacts struct {
	Trusted  map[string]contacts.Contact
	Incoming []contacts.Request
	Outgoing []contacts.Request
}

// Backend is what the UI controls.
type Backend interface {
	Status() (Status, error)
	Start() error
	Stop() error
	Discover() ([]discovery.Node, error)
	// Ask lets the agent of this node answer text within the conversation
	// contextID. On the way it reports which peers it asks.
	Ask(ctx context.Context, contextID, text string, step func(Step)) (string, error)
	Contacts() (Contacts, error)
	Accept(publicKey string) error
	Reject(publicKey string) error
	// UpdateAvailable reports whether a newer version can be installed.
	UpdateAvailable() (bool, error)
	// Update installs the newest version and reports whether anything changed.
	Update() (bool, error)
	// SetupCommand returns the interactive setup, run with the terminal handed over.
	SetupCommand() (*exec.Cmd, error)
}

// Step reports that the agent asks a peer on the way to its answer and, with
// Done set, that the peer has answered. What they say stays between them.
type Step struct {
	Peer string
	Done bool
}

type focus int

const (
	focusList focus = iota // keys are commands
	focusInput
)

// screen is the top-level view: the chat or the connection requests.
type screen int

const (
	screenChat screen = iota
	screenRequests
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
	pending   bool   // waiting for a reply; one message at a time keeps the history in order
	asking    string // the peer the agent is waiting for
}

// Model is the UI state.
type Model struct {
	backend Backend
	now     func() time.Time

	status      Status
	chat        conversation
	nodes       []discovery.Node // sorted by name
	focus       focus
	greeted     bool // the input was focused once the node turned out to be configured
	discovering bool
	notice      string
	noticeErr   bool

	updateAvailable bool
	updating        bool
	restart         bool

	trust     Contacts
	screen    screen
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

// New returns the UI for backend.
func New(backend Backend) *Model {
	input := textarea.New()
	input.Placeholder = "Message… (Enter sends, Alt+Enter new line)"
	input.ShowLineNumbers = false
	input.Prompt = ""
	input.SetHeight(inputHeight)
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "shift+enter", "ctrl+j"))

	history := viewport.New()
	history.SoftWrap = true
	history.KeyMap = viewport.KeyMap{} // keys go to the input; scrolling is handled explicitly

	return &Model{
		backend:    backend,
		now:        time.Now,
		chat:       conversation{contextID: a2a.NewID()},
		showSplash: true,
		input:      input,
		history:    history,
		spinner:    spinner.New(spinner.WithSpinner(spinner.MiniDot)),
	}
}

type (
	statusMsg struct {
		status Status
		err    error
	}
	nodesMsg struct {
		nodes []discovery.Node
		err   error
	}
	contactsMsg struct {
		trust Contacts
		err   error
	}
	replyMsg struct {
		contextID, text string
		err             error
	}
	// stepMsg is a step of the answer that is on its way; next delivers what follows.
	stepMsg struct {
		contextID string
		step      Step
		next      <-chan tea.Msg
	}
	// actionMsg reports the outcome of start, stop or setup.
	actionMsg struct {
		done string
		err  error
	}
	setupMsg struct{ cmd *exec.Cmd }
	// updateCheckMsg carries the result of looking up the newest version.
	updateCheckMsg struct{ available bool }
	updateMsg      struct {
		updated bool
		err     error
	}
	tickMsg struct{}
	// splashMsg advances the splash animation.
	splashMsg struct{ at time.Time }
)

func (m *Model) Init() tea.Cmd {
	m.discovering = true
	m.splashStart = m.now()
	return tea.Batch(m.refreshStatus, m.discover, m.loadContacts, m.checkUpdate, tick(), m.spinner.Tick, tea.RequestBackgroundColor, splashTick())
}

// Restart reports whether the UI quit to continue as the updated binary.
func (m *Model) Restart() bool { return m.restart }

// checkUpdate looks up the newest version now and again after updateInterval.
// Failures are ignored: being offline is not worth a notice.
func (m *Model) checkUpdate() tea.Msg {
	available, _ := m.backend.UpdateAvailable()
	return updateCheckMsg{available}
}

// update installs the newest version; the UI restarts itself afterwards.
func (m *Model) update() tea.Cmd {
	if m.updating {
		return nil
	}
	m.updating = true
	return func() tea.Msg {
		updated, err := m.backend.Update()
		return updateMsg{updated, err}
	}
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

func (m *Model) discover() tea.Msg {
	nodes, err := m.backend.Discover()
	return nodesMsg{nodes, err}
}

func (m *Model) loadContacts() tea.Msg {
	trust, err := m.backend.Contacts()
	return contactsMsg{trust, err}
}

// rescan refreshes status, nodes and contacts unless a scan is already running.
func (m *Model) rescan() tea.Cmd {
	if m.discovering {
		return tea.Batch(m.refreshStatus, m.loadContacts)
	}
	m.discovering = true
	return tea.Batch(m.refreshStatus, m.discover, m.loadContacts)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
	case tea.BackgroundColorMsg:
		m.input.SetStyles(textarea.DefaultStyles(msg.IsDark()))
	case tickMsg:
		return m, tea.Batch(m.rescan(), tick())
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
				if m.focus == focusList {
					return m, m.toggleFocus()
				}
			}
		}
	case nodesMsg:
		m.discovering = false
		if msg.err != nil {
			m.setNotice("discovery: "+msg.err.Error(), true)
		} else {
			m.merge(msg.nodes)
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
	case updateCheckMsg:
		m.updateAvailable = msg.available
		return m, tea.Tick(updateInterval, func(time.Time) tea.Msg { return m.checkUpdate() })
	case updateMsg:
		m.updating = false
		switch {
		case msg.err != nil:
			m.setNotice("update: "+msg.err.Error(), true)
		case msg.updated:
			m.restart = true
			return m, tea.Quit
		default:
			m.updateAvailable = false
			m.setNotice("already up to date", false)
		}
	case actionMsg:
		if msg.err != nil {
			m.setNotice(msg.err.Error(), true)
		} else {
			m.setNotice(msg.done, false)
		}
		return m, m.rescan()
	case setupMsg:
		return m, tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
			if err != nil {
				return actionMsg{err: err}
			}
			return actionMsg{done: "node configured and started"}
		})
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
				return m, tea.Quit
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

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	m.notice = ""
	if m.screen == screenRequests {
		return m.handleRequestKey(msg)
	}
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit
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
		return tea.Quit
	case "enter", "i":
		return m.toggleFocus()
	case "r":
		m.setNotice("searching the network…", false)
		return m.rescan()
	case "s":
		return m.toggleNode()
	case "c":
		return m.setup()
	case "n":
		m.newConversation()
	case "u":
		return m.update()
	case "R":
		return m.openRequests()
	}
	return nil
}

// handleRequestKey handles keys on the connection requests screen.
func (m *Model) handleRequestKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit
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
	verb := "declined"
	if accept {
		verb = "accepted"
	}
	return func() tea.Msg {
		var err error
		if accept {
			err = m.backend.Accept(pub)
		} else {
			err = m.backend.Reject(pub)
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
		m.focus = focusList
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
	return func() tea.Msg {
		events := make(chan tea.Msg)
		go func() {
			reply, err := m.backend.Ask(context.Background(), contextID, text, func(s Step) {
				events <- stepMsg{contextID, s, events}
			})
			events <- replyMsg{contextID, reply, err}
		}()
		return <-events
	}
}

// step notes which peer the agent is waiting for while the answer is pending.
func (m *Model) step(msg stepMsg) {
	c := &m.chat
	if c.contextID != msg.contextID {
		return // step of a conversation that was reset
	}
	c.asking = msg.step.Peer
	if msg.step.Done {
		c.asking = ""
	}
}

func (m *Model) receive(msg replyMsg) {
	c := &m.chat
	if c.contextID != msg.contextID {
		return // reply to a conversation that was reset
	}
	c.pending, c.asking = false, ""
	e := entry{text: msg.text, at: m.now()}
	if msg.err != nil {
		e.text, e.failed = msg.err.Error(), true
	}
	c.entries = append(c.entries, e)
	m.renderHistory(m.history.AtBottom())
}

// newConversation starts over; the agent keeps no memory of the old context.
func (m *Model) newConversation() {
	m.chat = conversation{contextID: a2a.NewID()}
	m.renderHistory(true)
	m.setNotice("new conversation", false)
}

// merge replaces the node list with a discovery result.
func (m *Model) merge(nodes []discovery.Node) {
	m.nodes = slices.Clone(nodes)
	slices.SortFunc(m.nodes, func(a, b discovery.Node) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID))
	})
}

// agentName is how the agent of this node is called in the chat.
func (m *Model) agentName() string {
	return cmp.Or(m.status.Name, "your agent")
}

func (m *Model) setNotice(text string, isErr bool) {
	m.notice, m.noticeErr = text, isErr
}
