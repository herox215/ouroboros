// Package tui is a terminal UI to control the local node and chat with the
// nodes on the network.
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
	Send(ctx context.Context, node discovery.Node, contextID, text string) (string, error)
	Contacts() (Contacts, error)
	Accept(publicKey string) error
	Reject(publicKey string) error
	// SetupCommand returns the interactive setup, run with the terminal handed over.
	SetupCommand() (*exec.Cmd, error)
}

type focus int

const (
	focusList focus = iota
	focusInput
)

// screen is the top-level view: the chat or the connection requests.
type screen int

const (
	screenChat screen = iota
	screenRequests
)

// entry is one message in a conversation.
type entry struct {
	mine   bool
	system bool // a local notice, not a chat message
	text   string
	at     time.Time
	failed bool
}

// conversation is the chat with one node.
type conversation struct {
	node      discovery.Node
	online    bool
	contextID string
	entries   []entry
	pending   bool // waiting for a reply; one message at a time keeps the remote history in order
	unread    int
}

// Model is the UI state.
type Model struct {
	backend Backend
	now     func() time.Time

	status      Status
	convs       map[string]*conversation
	order       []string // node IDs as listed
	selected    int
	focus       focus
	discovering bool
	notice      string
	noticeErr   bool

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
		convs:      map[string]*conversation{},
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
		nodeID, contextID, text string
		err                     error
	}
	// actionMsg reports the outcome of start, stop or setup.
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
	m.discovering = true
	m.splashStart = m.now()
	return tea.Batch(m.refreshStatus, m.discover, m.loadContacts, tick(), m.spinner.Tick, tea.RequestBackgroundColor, splashTick())
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
	case replyMsg:
		m.receive(msg)
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
	case "up", "k":
		m.selectConv(m.selected - 1)
	case "down", "j":
		m.selectConv(m.selected + 1)
	case "enter", "i":
		if m.current() != nil {
			return m.toggleFocus()
		}
	case "r":
		m.setNotice("searching the network…", false)
		return m.rescan()
	case "s":
		return m.toggleNode()
	case "c":
		return m.setup()
	case "n":
		m.newConversation()
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
	if m.current() == nil {
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
	c := m.current()
	text := strings.TrimSpace(m.input.Value())
	if c == nil || text == "" {
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

	node, contextID := c.node, c.contextID
	return func() tea.Msg {
		reply, err := m.backend.Send(context.Background(), node, contextID, text)
		return replyMsg{node.ID, contextID, reply, err}
	}
}

func (m *Model) receive(msg replyMsg) {
	c := m.convs[msg.nodeID]
	if c == nil || c.contextID != msg.contextID {
		return // reply to a conversation that was reset
	}
	c.pending = false
	if a2a.IsPending(msg.err) {
		c.entries = append(c.entries, entry{
			system: true,
			text:   "Connection request sent to " + c.node.Name + ". Waiting for approval.",
			at:     m.now(),
		})
	} else {
		e := entry{text: msg.text, at: m.now()}
		if msg.err != nil {
			e.text, e.failed = msg.err.Error(), true
		}
		c.entries = append(c.entries, e)
	}
	if c != m.current() {
		c.unread++
		return
	}
	m.renderHistory(m.history.AtBottom())
}

// newConversation starts over with the selected node, which then forgets the old context.
func (m *Model) newConversation() {
	c := m.current()
	if c == nil {
		return
	}
	c.contextID, c.entries, c.pending, c.unread = a2a.NewID(), nil, false, 0
	m.renderHistory(true)
	m.setNotice("new conversation with "+c.node.Name, false)
}

// merge updates the node list with a discovery result. Nodes that went away
// stay listed as offline so their conversation is kept.
func (m *Model) merge(nodes []discovery.Node) {
	selectedID := m.currentID()
	for _, c := range m.convs {
		c.online = false
	}
	for _, n := range nodes {
		if c, ok := m.convs[n.ID]; ok {
			c.node, c.online = n, true
		} else {
			m.convs[n.ID] = &conversation{node: n, online: true, contextID: a2a.NewID()}
		}
	}
	m.order = m.order[:0]
	for id, c := range m.convs {
		if c.online || len(c.entries) > 0 {
			m.order = append(m.order, id)
		}
	}
	slices.SortFunc(m.order, func(a, b string) int {
		ca, cb := m.convs[a], m.convs[b]
		return cmp.Or(strings.Compare(ca.node.Name, cb.node.Name), strings.Compare(a, b))
	})
	m.selected = max(0, slices.Index(m.order, selectedID))
	if m.currentID() != selectedID {
		m.selectConv(m.selected)
	}
	if m.current() == nil && m.focus == focusInput {
		m.toggleFocus()
	}
}

func (m *Model) selectConv(i int) {
	if len(m.order) == 0 {
		return
	}
	m.selected = min(max(i, 0), len(m.order)-1)
	m.current().unread = 0
	m.renderHistory(true)
}

func (m *Model) currentID() string {
	if m.selected < len(m.order) {
		return m.order[m.selected]
	}
	return ""
}

func (m *Model) current() *conversation {
	return m.convs[m.currentID()]
}

func (m *Model) setNotice(text string, isErr bool) {
	m.notice, m.noticeErr = text, isErr
}
