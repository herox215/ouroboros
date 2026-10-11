package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// Setup is the setup of the node, as the UI goes through it: see
// plugin.Setup, which keeps what the providers sign in with to itself.
type Setup interface {
	// Name is the name to offer.
	Name() string
	// Providers are the providers to sign in with, and Accesses the
	// accesses the node has.
	Providers() []plugin.SetupProvider
	Accesses() ([]plugin.SetupAccess, error)
	// Login signs in to a new access with a provider, which asks through d,
	// Again to the access id anew, and Use takes the access id as it is.
	// Each lists the models of the access, which Save then saves.
	Login(ctx context.Context, provider string, d plugin.Dialog) (plugin.SetupModels, error)
	Again(ctx context.Context, id string, d plugin.Dialog) (plugin.SetupModels, error)
	Use(ctx context.Context, id string) (plugin.SetupModels, error)
	// Rename and Remove manage the accesses. Remove returns what to tell
	// the owner of how it went.
	Rename(id, name string) error
	Remove(ctx context.Context, id string) (string, error)
	// Rights are the names of the tools for the owner and for peers, and
	// the paths that are protected, as they are once the setup is saved.
	Rights() (owner, peers, protected []string)
	// Save saves the access that was signed in to or taken, with model and
	// name.
	Save(model, name string) error
}

// The setup is a scene like the splash. The snake lies behind the dialog, a
// little more curled up with every step, until its tail is in its mouth.
// Then it turns once, shrinks to the name and the splash takes over.
const (
	curlFrom     = 255 * math.Pi / 180 // how far the body is curled up at the welcome
	curlLast     = 328 * math.Pi / 180 // at the last step: the tail is just short of the mouth
	curlDuration = 450 * time.Millisecond
	turnAt       = 350 * time.Millisecond // after the setup was saved
	turnDuration = 1300 * time.Millisecond
	setupDim     = 0.5 // how bright the snake is behind the dialog
	boxWidth     = 44
)

// step is one page of the setup.
type step int

const (
	stepWelcome step = iota
	// The pages of the access: the accesses the node has, the providers to
	// sign in with, and what the provider asks while it signs in. Which of
	// them are shown depends on the node: a new one with one provider goes
	// straight to what that provider asks.
	stepAccess
	stepProvider
	stepLogin
	stepModel
	stepName
	stepRights
)

// phases is how many parts the setup has after the welcome: the access, the
// model, the name and the rights.
const phases = 4

// phase is the part of the setup the page s belongs to, 0 for the welcome.
func (s step) phase() int {
	switch s {
	case stepWelcome:
		return 0
	case stepAccess, stepProvider, stepLogin:
		return 1
	}
	return int(s-stepModel) + 2
}

// onboarding is the setup of the node in the UI.
type onboarding struct {
	setup Setup
	first bool // the node was not set up: there is no chat to go back to
	step  step
	input textinput.Model

	accesses  []plugin.SetupAccess
	providers []plugin.SetupProvider
	sel       int    // the row selected on the pages of the accesses and the providers
	renaming  string // the ID of the access a name is typed for, "" if none

	login   *login            // what a provider is asking, while it signs in
	answers map[string]string // what was entered so far, by the title of the question, to offer again

	models plugin.SetupModels
	model  int // selected of models.Chat
	name   string
	busy   string // what it waits for, like "connecting"
	err    string
	note   string // what the provider told, or how removing an access went

	// The scene: when it opened, the time of its last frame, how far the
	// body is curled up since when, and when the setup was saved.
	opened, at time.Time
	curl       struct {
		from, to float64
		since    time.Time
	}
	saved time.Time
}

// login is one run of a provider signing in. The provider runs beside the
// UI and asks through it: what it asks, tells and ends with comes as
// loginMsg, and the answers go back through reply.
type login struct {
	events chan loginMsg
	cancel context.CancelFunc
	asked  *plugin.Question // what is asked right now, nil while the provider works
	reply  chan<- string
}

type (
	// loginMsg is what a provider that signs in wants of the UI: a question
	// asked, a line told, an answer rejected, a URL opened, or done, with
	// the models of the access or why it failed.
	loginMsg struct {
		from   *login
		ask    *plugin.Question
		reply  chan<- string
		tell   string
		reject string
		open   string
		done   bool
		models plugin.SetupModels
		err    error
	}
	// connectedMsg is what an access that is taken as it is answered with.
	connectedMsg struct {
		id     string
		models plugin.SetupModels
		err    error
	}
	// accessesMsg reports how renaming or removing an access went, with
	// the accesses there are now.
	accessesMsg struct {
		accesses []plugin.SetupAccess
		note     string
		err      error
	}
	// savedMsg reports how saving the setup went.
	savedMsg struct{ err error }
)

// dialog is the plugin.Dialog of a login: it hands what the provider wants to
// the UI and waits for the answer.
type dialog struct {
	ctx context.Context // of the login: given up when the owner leaves it
	l   *login
}

// send hands msg to the UI unless the login was given up.
func (d dialog) send(msg loginMsg) bool {
	msg.from = d.l
	select {
	case d.l.events <- msg:
		return true
	case <-d.ctx.Done():
		return false
	}
}

func (d dialog) Ask(ctx context.Context, q plugin.Question) (string, error) {
	reply := make(chan string, 1)
	if !d.send(loginMsg{ask: &q, reply: reply}) {
		return "", d.ctx.Err()
	}
	select {
	case answer := <-reply:
		return answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-d.ctx.Done():
		return "", d.ctx.Err()
	}
}

func (d dialog) Tell(text string)  { d.send(loginMsg{tell: text}) }
func (d dialog) Reject(why string) { d.send(loginMsg{reject: why}) }
func (d dialog) Open(url string)   { d.send(loginMsg{open: url}) }

// browse shows a URL in the browser of the user; tests replace it.
var browse = plugin.Browse

// openSetup opens the setup. first says that the node was not set up yet.
// If the setup cannot be had, the UI says so in its footer.
func (m *Model) openSetup(first bool) {
	s, err := m.backend.Setup()
	if err != nil {
		m.setNotice("setup: "+err.Error(), true)
		return
	}
	accesses, err := s.Accesses()
	if err != nil {
		m.setNotice("setup: "+err.Error(), true)
		return
	}
	input := textinput.New()
	input.Prompt = "› "
	input.SetWidth(boxWidth - 8)
	input.SetStyles(textinput.DefaultStyles(!m.light))
	now := m.now()
	o := &onboarding{setup: s, first: first, input: input, opened: now, at: now,
		accesses: accesses, providers: s.Providers(), answers: map[string]string{}}
	o.curl.from, o.curl.to, o.curl.since = curlFrom, curlFrom, now
	m.onboard = o
}

// curled is how far the body is curled up at the time at.
func (o *onboarding) curled(at time.Time) float64 {
	f := ramp(at.Sub(o.curl.since), 0, curlDuration)
	return o.curl.from + (o.curl.to-o.curl.from)*(1-(1-f)*(1-f))
}

// curlTo lets the body curl up to curl, from where it is now.
func (o *onboarding) curlTo(curl float64) {
	o.curl.from, o.curl.to, o.curl.since = o.curled(o.at), curl, o.at
}

// goTo turns to the page s. The snake curls up with every part of the setup.
func (o *onboarding) goTo(s step) tea.Cmd {
	o.stop()
	o.step, o.err, o.busy, o.renaming, o.note = s, "", "", "", ""
	o.curlTo(curlFrom + (curlLast-curlFrom)*float64(s.phase())/phases)
	switch s {
	case stepAccess:
		o.sel = max(0, slices.IndexFunc(o.accesses, func(a plugin.SetupAccess) bool { return a.Active }))
	case stepProvider:
		o.sel = 0
	case stepName:
		return o.type_(cmp.Or(o.name, o.setup.Name()), "", false)
	}
	o.input.Blur()
	return nil
}

// type_ makes the input ready for a text, with value in it.
func (o *onboarding) type_(value, hint string, secret bool) tea.Cmd {
	o.input.Reset()
	o.input.EchoMode = textinput.EchoNormal
	o.input.Placeholder = hint
	if secret {
		o.input.EchoMode = textinput.EchoPassword
		o.input.EchoCharacter = '•'
	}
	o.input.SetValue(value)
	o.input.CursorEnd()
	return o.input.Focus()
}

// stop gives up the login that is on its way, if one is.
func (o *onboarding) stop() {
	if o.login != nil {
		o.login.cancel()
		o.login = nil
	}
}

// typing reports whether the page is one to type on.
func (o *onboarding) typing() bool {
	return o.step == stepName || o.renaming != "" || o.step == stepLogin && o.login != nil && o.login.asked != nil
}

// enter is the page the access is picked on, as the node is: its accesses if
// it has some, the providers if there are several, and else what the only
// provider asks.
func (m *Model) enter() tea.Cmd {
	o := m.onboard
	if len(o.accesses) > 0 {
		return o.goTo(stepAccess)
	}
	return m.add()
}

// add starts a new access: with the only provider, or on the page of the
// providers.
func (m *Model) add() tea.Cmd {
	o := m.onboard
	switch len(o.providers) {
	case 0:
		o.err = "this ouro has no provider to sign in with: turn the plugin of one on in /settings"
		return nil
	case 1:
		return m.signIn(o.providers[0].Name, "")
	}
	return o.goTo(stepProvider)
}

// signIn lets a provider sign in: to a new access with the provider called
// provider, or anew to the access again.
func (m *Model) signIn(provider, again string) tea.Cmd {
	o := m.onboard
	o.goTo(stepLogin)
	ctx, cancel := context.WithCancel(m.ctx)
	l := &login{events: make(chan loginMsg), cancel: cancel}
	o.login, o.busy = l, "one moment"
	setup, d := o.setup, dialog{ctx, l}
	go func() {
		var models plugin.SetupModels
		var err error
		if again != "" {
			models, err = setup.Again(ctx, again, d)
		} else {
			models, err = setup.Login(ctx, provider, d)
		}
		d.send(loginMsg{done: true, models: models, err: err})
	}()
	return l.next
}

// next waits for what the provider wants next.
func (l *login) next() tea.Msg { return <-l.events }

// loggedIn takes what the provider that signs in wants: a question to show,
// a line to tell, and in the end the models of the access.
func (m *Model) loggedIn(msg loginMsg) tea.Cmd {
	o := m.onboard
	if o == nil || o.login == nil || o.login != msg.from {
		return nil // of a login that was given up
	}
	l := o.login
	switch {
	case msg.done:
		o.login, o.busy = nil, ""
		l.cancel()
		if msg.err != nil {
			o.err = msg.err.Error()
			return nil
		}
		return m.offer(msg.models)
	case msg.ask != nil:
		l.asked, l.reply, o.busy = msg.ask, msg.reply, ""
		value := ""
		if !msg.ask.Secret {
			value = cmp.Or(o.answers[msg.ask.Title], msg.ask.Default)
		}
		return tea.Batch(o.type_(value, msg.ask.Hint, msg.ask.Secret), l.next)
	case msg.reject != "":
		o.err = msg.reject
	case msg.tell != "":
		o.note = msg.tell
	case msg.open != "":
		o.note = "The page opens in your browser. Its link is on the clipboard too."
		url := msg.open
		return tea.Batch(tea.SetClipboard(url), l.next, func() tea.Msg {
			_ = writeClipboard(url) // the terminal may have it anyway
			_ = browse(url)         // without a browser here, the link is what there is
			return nil
		})
	}
	return l.next
}

// offer shows the models of the access that was signed in to or taken.
func (m *Model) offer(models plugin.SetupModels) tea.Cmd {
	o := m.onboard
	if len(models.Chat) == 0 {
		o.err = "the access offers no model"
		return nil
	}
	o.models = models
	o.model = max(0, slices.Index(models.Chat, models.Default))
	return o.goTo(stepModel)
}

// onboardKey handles a key while the setup is open.
func (m *Model) onboardKey(msg tea.KeyPressMsg) tea.Cmd {
	o := m.onboard
	if msg.String() == "ctrl+c" {
		o.stop()
		return m.leave()
	}
	if !o.saved.IsZero() {
		m.closeSetup() // any key skips the scene
		return nil
	}
	if o.busy != "" && o.step != stepLogin {
		return nil
	}
	switch msg.String() {
	case "esc":
		return m.back()
	case "enter":
		return m.next()
	}
	if o.typing() {
		var cmd tea.Cmd
		o.input, cmd = o.input.Update(msg)
		return cmd
	}
	rows := 0
	switch o.step {
	case stepModel:
		switch msg.String() {
		case "up", "k":
			o.model = cycle(o.model, -1, len(o.models.Chat))
		case "down", "j":
			o.model = cycle(o.model, 1, len(o.models.Chat))
		}
		return nil
	case stepAccess:
		rows = len(o.accesses) + 1 // and the row that adds one
	case stepProvider:
		rows = len(o.providers)
	}
	switch msg.String() {
	case "up", "k":
		o.sel = cycle(o.sel, -1, rows)
	case "down", "j":
		o.sel = cycle(o.sel, 1, rows)
	}
	if o.step != stepAccess || o.sel >= len(o.accesses) {
		return nil
	}
	access := o.accesses[o.sel]
	switch msg.String() {
	case "l":
		return m.signIn("", access.ID)
	case "r":
		o.renaming, o.err, o.note = access.ID, "", ""
		return o.type_(access.Name, "Enter for no name", false)
	case "d":
		o.busy, o.err, o.note = "removing", "", ""
		setup, ctx := o.setup, m.ctx
		return func() tea.Msg {
			note, err := setup.Remove(ctx, access.ID)
			accesses, listErr := setup.Accesses()
			return accessesMsg{accesses, note, cmp.Or(err, listErr)}
		}
	}
	return nil
}

// back leaves the page for the one before it.
func (m *Model) back() tea.Cmd {
	o := m.onboard
	if o.renaming != "" {
		o.renaming = ""
		o.input.Blur()
		return nil
	}
	switch o.step {
	case stepWelcome:
		if o.first {
			return m.leave()
		}
		m.closeSetup()
		return nil
	case stepLogin:
		if len(o.providers) > 1 {
			return o.goTo(stepProvider)
		}
		fallthrough
	case stepProvider:
		if len(o.accesses) > 0 {
			return o.goTo(stepAccess)
		}
		fallthrough
	case stepAccess:
		return o.goTo(stepWelcome)
	case stepModel:
		return m.enter()
	}
	return o.goTo(o.step - 1)
}

// next leaves the page with what was entered on it.
func (m *Model) next() tea.Cmd {
	o := m.onboard
	value := strings.TrimSpace(o.input.Value())
	switch o.step {
	case stepWelcome:
		return m.enter()
	case stepAccess:
		setup, ctx := o.setup, m.ctx
		if id := o.renaming; id != "" {
			o.renaming = ""
			o.input.Blur()
			return func() tea.Msg {
				err := setup.Rename(id, value)
				accesses, listErr := setup.Accesses()
				return accessesMsg{accesses, "", cmp.Or(err, listErr)}
			}
		}
		if o.sel >= len(o.accesses) {
			return m.add()
		}
		id := o.accesses[o.sel].ID
		o.busy, o.err, o.note = "connecting", "", ""
		return func() tea.Msg {
			models, err := setup.Use(ctx, id)
			return connectedMsg{id, models, err}
		}
	case stepProvider:
		return m.signIn(o.providers[o.sel].Name, "")
	case stepLogin:
		l := o.login
		if l == nil {
			return m.enter() // it failed: once more
		}
		if l.asked == nil {
			return nil // the provider works
		}
		if value == "" {
			value = l.asked.Default
		} else if !l.asked.Secret {
			o.answers[l.asked.Title] = value
		}
		l.reply <- value
		l.asked, o.busy, o.err, o.note = nil, "one moment", "", ""
		o.input.Blur()
		return nil
	case stepModel:
		o.name = cmp.Or(o.name, o.setup.Name())
	case stepName:
		if value == "" {
			o.err = "the node needs a name"
			return nil
		}
		o.name = value
	case stepRights:
		o.busy = "saving"
		model, name, setup := o.models.Chat[o.model], o.name, o.setup
		return func() tea.Msg { return savedMsg{setup.Save(model, name)} }
	}
	return o.goTo(o.step + 1)
}

// connected takes the answer of an access that was taken as it is: on to its
// models, or to signing in to it anew if it refused its key.
func (m *Model) connected(msg connectedMsg) tea.Cmd {
	o := m.onboard
	if o == nil || o.step != stepAccess {
		return nil
	}
	o.busy = ""
	switch {
	case errors.Is(msg.err, plugin.ErrKeyRejected):
		cmd := m.signIn("", msg.id)
		o.err = "API key rejected: sign in again"
		return cmd
	case msg.err != nil:
		o.err = msg.err.Error()
		return nil
	}
	return m.offer(msg.models)
}

// managed takes how renaming or removing an access went.
func (m *Model) managed(msg accessesMsg) {
	o := m.onboard
	if o == nil || o.step != stepAccess {
		return
	}
	o.busy, o.note, o.err = "", msg.note, ""
	if msg.err != nil {
		o.err = msg.err.Error()
	}
	if msg.accesses != nil {
		o.accesses = msg.accesses
	}
	o.sel = min(o.sel, len(o.accesses))
}

// setupSaved starts the end of the scene once the setup is saved, and the
// node with what was saved.
func (m *Model) setupSaved(msg savedMsg) tea.Cmd {
	o := m.onboard
	if o == nil || o.step != stepRights {
		return nil
	}
	o.busy = ""
	if msg.err != nil {
		o.err = msg.err.Error()
		return nil
	}
	o.saved = o.at
	o.curlTo(bodyArc)
	return func() tea.Msg {
		if err := m.backend.Stop(); err != nil {
			return actionMsg{err: err}
		}
		if err := m.backend.Start(); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{done: "node set up and started"}
	}
}

// frame advances the scene to the time at. When the snake has turned and
// shrunk to the name, the splash takes over.
func (m *Model) frame(at time.Time) {
	o := m.onboard
	o.at = at
	if o.saved.IsZero() || at.Sub(o.saved) < turnAt+turnDuration {
		return
	}
	m.closeSetup()
	m.splash.show = true
	m.splash.start = at.Add(-biteAt)
	m.splash.elapsed = biteAt
}

// closeSetup leaves the setup for the chat.
func (m *Model) closeSetup() {
	m.onboard.stop()
	m.onboard = nil
	m.splash.show = false
}

// scale is how large the snake lies behind the dialog on a screen of w by h:
// as large as fits, uncurled as it is at the welcome.
func scale(w, h int) float64 {
	up, down := extent()
	wide := 2 * (pose{curl: curlFrom}.ring().r + reach)
	return max(0.6, min(float64(w*2)/wide, float64(h*4)/(up+down), 2.5))
}

// extent is how far the snake reaches above and below the center of the
// ring it curls up into, uncurled as it is at the welcome, in pixels of the
// logo. The wider ring of the snake that lies open has its bottom where the
// curled up one has it.
func extent() (up, down float64) {
	r := pose{curl: curlFrom}.ring().r
	return 2*r - ringR + reach, ringR + reach
}

// pose is where the snake lies at the time of the last frame: behind the
// dialog, and once the setup is saved on its way to the name of the splash.
func (o *onboarding) pose(w, h int) (p pose, bright float64) {
	s := scale(w, h)
	// The center of the ring once curled up, so that the snake is in the
	// middle of the screen when uncurled most.
	up, down := extent()
	p = pose{curl: o.curled(o.at), scale: s, x: float64(w), y: float64(h*2) + (up-down)*s/2}
	if o.saved.IsZero() {
		return p, setupDim
	}
	f := ramp(o.at.Sub(o.saved), turnAt, turnDuration)
	f = f * f * (3 - 2*f) // ease in and out
	// It shrinks to the name of the splash.
	_, _, top, block := splashLayout(w, h)
	y := float64(top*4 + block*2)
	p.turn = 2 * math.Pi * f
	p.scale = s + (1-s)*f
	p.x += (float64(w) - p.x) * f
	p.y += (y - p.y) * f
	return p, setupDim + (1-setupDim)*f
}

// onboardView draws the scene of the setup: the sky, the snake and the
// dialog in front of them.
func (m *Model) onboardView() string {
	o := m.onboard
	w, h := m.width, m.height
	p := colors(m.light)
	since := o.at.Sub(o.opened)
	c := &canvas{w: w, h: h, cells: make([]cell, w*h), back: p.back, alpha: ramp(since, 0, fadeIn)}
	drawStars(c, p, since)
	at, bright := o.pose(w, h)
	for row, line := range poseCells(at, 1, false, w, h) {
		for col, cl := range line {
			if cl.dots != 0 {
				hue := cl.along*0.9 - since.Seconds()*0.08
				c.set(col, row, 0x2800+cl.dots, mix(p.back, p.gradient(hue), bright), false)
			}
		}
	}
	scene := c.String()
	if !o.saved.IsZero() {
		return scene
	}
	box := o.dialog(m.spinner.View(), m.light)
	x, y := (w-lipgloss.Width(box))/2, int(at.y/4)-lipgloss.Height(box)/2
	y = max(0, min(y, h-lipgloss.Height(box)))
	layers := lipgloss.NewCompositor(lipgloss.NewLayer(scene), lipgloss.NewLayer(box).X(max(0, x)).Y(y).Z(1))
	return lipgloss.NewCanvas(w, h).Compose(layers).Render()
}

// titles are the titles of the pages that have a fixed one.
var titles = map[step]string{stepAccess: "Access", stepProvider: "Provider", stepLogin: "Sign in",
	stepModel: "Model", stepName: "Node name", stepRights: "What your agent may do"}

// dialog is the page of the setup that is open, in its box.
func (o *onboarding) dialog(spin string, light bool) string {
	inner := boxWidth - 6 // border and padding
	title := "Welcome to ouroboros"
	if o.step > stepWelcome {
		page := titles[o.step]
		if o.login != nil && o.login.asked != nil {
			page = o.login.asked.Title
		}
		title = fmt.Sprintf("%d/%d · %s", o.step.phase(), phases, page)
	}
	lines := []string{boldStyle.Foreground(accent).Render(title), ""}
	text := func(s ...string) {
		for _, l := range s {
			lines = append(lines, mutedStyle.Render(ansi.Truncate(l, inner, "…")))
		}
	}
	pick := func(i int, row string) {
		row = ansi.Truncate(row, inner-2, "…")
		if i == o.sel {
			lines = append(lines, lipgloss.NewStyle().Foreground(accent).Render("▸ "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}
	hint := "enter next · esc back"
	switch o.step {
	case stepWelcome:
		text("Your own agent runs as a node here.", "Other nodes find it; it can ask them.",
			"", "A few questions, then you can chat.", "Change the answers later with /setup.")
		hint = "enter start · esc quit"
		if !o.first {
			hint = "enter start · esc back to the chat"
		}
	case stepAccess:
		for i, a := range o.accesses {
			row := a.Label
			if a.About != "" {
				row += " · " + a.About
			}
			if a.Active {
				row += "  in use"
			}
			if a.Missing {
				row += "  (no " + a.Provider + " plugin)"
			}
			pick(i, row)
		}
		pick(len(o.accesses), "+ add another access")
		hint = "enter use · l sign in again\nr rename · d remove · esc back"
		if o.renaming != "" {
			text("", "What do you call this access?")
			lines = append(lines, o.input.View())
			hint = "enter save · esc back"
		}
	case stepProvider:
		text("Where does your agent get its model?", "")
		for i, p := range o.providers {
			pick(i, p.Title)
		}
		hint = "↑↓ choose · enter next · esc back"
	case stepLogin:
		if o.login != nil && o.login.asked != nil {
			text(o.login.asked.Text...)
			lines = append(lines, "", o.input.View())
		} else if o.login == nil {
			hint = "enter once more · esc back"
		}
	case stepModel:
		lines = append(lines, o.modelList(inner)...)
		hint = "↑↓ choose · enter next · esc back"
	case stepName:
		text("Other nodes see your agent by this name.", "")
		lines = append(lines, o.input.View())
	case stepRights:
		owner, peers, protected := o.setup.Rights()
		row := func(label string, names []string) {
			lines = append(lines, mutedStyle.Render(fmt.Sprintf("%-10s ", label))+ansi.Truncate(tools(names), inner-11, "…"))
		}
		row("For you", owner)
		row("For peers", peers)
		lines = append(lines, mutedStyle.Render("Protected  ")+ansi.Truncate(cmp.Or(strings.Join(protected, " "), "nothing"), inner-11, "…"))
		text("", "Change it in /settings.")
		hint = "enter finish · esc back"
	}
	if o.note != "" {
		lines = append(lines, "", mutedStyle.Width(inner).Render(o.note))
	}
	if o.err != "" {
		lines = append(lines, "", errStyle.Width(inner).Render("✗ "+o.err))
	}
	if o.busy != "" {
		lines = append(lines, "", spin+mutedStyle.Render(" "+o.busy+"…"))
	}
	lines = append(lines, "", o.progress(), mutedStyle.Render(hint))
	return box(true, light).Padding(1, 2).Width(boxWidth).Render(strings.Join(lines, "\n"))
}

// modelList shows a few of the models around the one selected.
func (o *onboarding) modelList(width int) []string {
	const shown = 6
	all := o.models.Chat
	first := max(0, min(o.model-shown/2, len(all)-shown))
	var lines []string
	for i := first; i < min(len(all), first+shown); i++ {
		name := ansi.Truncate(all[i], width-2, "…")
		if i == o.model {
			lines = append(lines, lipgloss.NewStyle().Foreground(accent).Render("▸ "+name))
		} else {
			lines = append(lines, "  "+name)
		}
	}
	return append(lines, mutedStyle.Render(fmt.Sprintf("%d of %d", o.model+1, len(all))))
}

// tools says which tools a list allows.
func tools(names []string) string {
	switch {
	case slices.Contains(names, plugin.AllTools):
		return "all tools"
	case len(names) == 0:
		return "nothing"
	}
	return strings.Join(names, ", ")
}

// progress shows how far the setup is: a dot per part after the welcome.
func (o *onboarding) progress() string {
	dots := make([]string, 0, phases)
	for p := 1; p <= phases; p++ {
		switch at := o.step.phase(); {
		case p < at:
			dots = append(dots, lipgloss.NewStyle().Foreground(accent).Render("●"))
		case p == at:
			dots = append(dots, lipgloss.NewStyle().Foreground(accent).Render("◉"))
		default:
			dots = append(dots, mutedStyle.Render("○"))
		}
	}
	return strings.Join(dots, " ")
}
