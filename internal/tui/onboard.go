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
// plugin.Setup, which keeps the key for the endpoint to itself.
type Setup interface {
	// Endpoint and Name are what to offer.
	Endpoint() string
	Name() string
	// HasKey reports whether a key is known for endpoint, which Saved uses.
	HasKey(endpoint string) bool
	// Connect lists the models of endpoint with key; it fails with
	// plugin.ErrKeyRejected if the endpoint refuses the key.
	Connect(ctx context.Context, endpoint, key string) (plugin.SetupModels, error)
	Saved(ctx context.Context, endpoint string) (plugin.SetupModels, error)
	// Rights are the names of the tools for the owner and for peers, and
	// the paths that are protected, as they are once the setup is saved.
	Rights() (owner, peers, protected []string)
	// Save saves what Connect accepted with model and name.
	Save(model, name string) error
}

// The setup is a scene like the splash. The snake lies behind the dialog, a
// little more curled up with every step, until its tail is in its mouth.
// Then it turns once, shrinks to the logo and the splash takes over: the jaws
// snap shut on the tail.
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
	stepEndpoint
	stepKey
	stepModel
	stepName
	stepRights
	steps // how many pages there are
)

// onboarding is the setup of the node in the UI.
type onboarding struct {
	setup Setup
	first bool // the node was not set up: there is no chat to go back to
	step  step
	input textinput.Model

	endpoint string
	models   plugin.SetupModels
	model    int // selected of models.Chat
	name     string
	busy     string // what it waits for, like "connecting"
	err      string

	// The scene: when it opened, the time of its last frame, how far the
	// body is curled up since when, and when the setup was saved.
	opened, at time.Time
	curl       struct {
		from, to float64
		since    time.Time
	}
	saved time.Time
}

type (
	// connectedMsg is the answer of the endpoint to the key.
	connectedMsg struct {
		models plugin.SetupModels
		err    error
	}
	// savedMsg reports how saving the setup went.
	savedMsg struct{ err error }
)

// openSetup opens the setup. first says that the node was not set up yet.
// If the setup cannot be had, the UI says so in its footer.
func (m *Model) openSetup(first bool) {
	s, err := m.backend.Setup()
	if err != nil {
		m.setNotice("setup: "+err.Error(), true)
		return
	}
	input := textinput.New()
	input.Prompt = "› "
	input.SetWidth(boxWidth - 8)
	input.SetStyles(textinput.DefaultStyles(!m.light))
	now := m.now()
	o := &onboarding{setup: s, first: first, input: input, opened: now, at: now}
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

// goTo turns to the page s. The snake curls up with every page.
func (o *onboarding) goTo(s step) tea.Cmd {
	o.step, o.err, o.busy = s, "", ""
	o.curlTo(curlFrom + (curlLast-curlFrom)*float64(s)/float64(steps-1))
	o.input.Reset()
	o.input.EchoMode = textinput.EchoNormal
	o.input.Placeholder = ""
	switch s {
	case stepEndpoint:
		o.input.SetValue(cmp.Or(o.endpoint, o.setup.Endpoint()))
	case stepKey:
		o.input.EchoMode = textinput.EchoPassword
		o.input.EchoCharacter = '•'
		o.input.Placeholder = "Enter for none"
		if o.setup.HasKey(o.endpoint) {
			o.input.Placeholder = "Enter keeps the saved key"
		}
	case stepName:
		o.input.SetValue(cmp.Or(o.name, o.setup.Name()))
	default:
		o.input.Blur()
		return nil
	}
	o.input.CursorEnd()
	return o.input.Focus()
}

// typing reports whether the page is one to type on.
func (o *onboarding) typing() bool {
	return o.step == stepEndpoint || o.step == stepKey || o.step == stepName
}

// onboardKey handles a key while the setup is open.
func (m *Model) onboardKey(msg tea.KeyPressMsg) tea.Cmd {
	o := m.onboard
	if msg.String() == "ctrl+c" {
		return m.leave()
	}
	if !o.saved.IsZero() {
		m.closeSetup() // any key skips the scene
		return nil
	}
	if o.busy != "" {
		return nil
	}
	switch msg.String() {
	case "esc":
		if o.step > stepWelcome {
			return o.goTo(o.step - 1)
		}
		if o.first {
			return m.leave()
		}
		m.closeSetup()
		return nil
	case "enter":
		return m.next()
	}
	if o.step == stepModel {
		switch msg.String() {
		case "up", "k":
			o.model = max(0, o.model-1)
		case "down", "j":
			o.model = min(len(o.models.Chat)-1, o.model+1)
		}
		return nil
	}
	if o.typing() {
		var cmd tea.Cmd
		o.input, cmd = o.input.Update(msg)
		return cmd
	}
	return nil
}

// next leaves the page with what was entered on it.
func (m *Model) next() tea.Cmd {
	o := m.onboard
	value := strings.TrimSpace(o.input.Value())
	switch o.step {
	case stepEndpoint:
		if value == "" {
			o.err = "the endpoint is needed"
			return nil
		}
		o.endpoint = strings.TrimRight(value, "/")
	case stepKey:
		o.busy = "connecting"
		endpoint, setup, ctx := o.endpoint, o.setup, m.ctx
		return func() tea.Msg {
			if value == "" && setup.HasKey(endpoint) {
				models, err := setup.Saved(ctx, endpoint)
				return connectedMsg{models, err}
			}
			models, err := setup.Connect(ctx, endpoint, value)
			return connectedMsg{models, err}
		}
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

// connected takes the answer of the endpoint: on to the models, or the key
// once more.
func (m *Model) connected(msg connectedMsg) tea.Cmd {
	o := m.onboard
	if o == nil || o.step != stepKey {
		return nil
	}
	o.busy = ""
	switch {
	case errors.Is(msg.err, plugin.ErrKeyRejected):
		o.err = "API key rejected, try again"
		o.input.Reset()
		return nil
	case msg.err != nil:
		o.err = msg.err.Error()
		return nil
	}
	o.models = msg.models
	o.model = max(0, slices.Index(msg.models.Chat, msg.models.Default))
	return o.goTo(stepModel)
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
// shrunk to the logo, the splash takes over where the jaws snap shut.
func (m *Model) frame(at time.Time) {
	o := m.onboard
	o.at = at
	if o.saved.IsZero() || at.Sub(o.saved) < turnAt+turnDuration {
		return
	}
	m.closeSetup()
	m.showSplash = true
	m.splashStart = at.Add(-biteAt)
	m.splashElapsed = biteAt
	m.skyShift = at.Sub(o.opened) - biteAt
}

// closeSetup leaves the setup for the chat.
func (m *Model) closeSetup() {
	m.onboard = nil
	m.showSplash = false
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
// dialog, and once the setup is saved on its way to the logo of the splash.
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
	_, _, left, top, _ := splashLayout(w, h)
	p.turn = 2 * math.Pi * f
	p.scale = s + (1-s)*f
	p.x += (float64(left*2) + ringX - p.x) * f
	p.y += (float64(top*4) + ringY - p.y) * f
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

// pages are the titles of the pages after the welcome.
var pages = [steps]string{"", "LLM endpoint", "API key", "Model", "Node name", "What your agent may do"}

// dialog is the page of the setup that is open, in its box.
func (o *onboarding) dialog(spin string, light bool) string {
	inner := boxWidth - 6 // border and padding
	title := "Welcome to ouroboros"
	if o.step > stepWelcome {
		title = fmt.Sprintf("%d/%d · %s", o.step, steps-1, pages[o.step])
	}
	lines := []string{boldStyle.Foreground(accent).Render(title), ""}
	text := func(s ...string) {
		for _, l := range s {
			lines = append(lines, mutedStyle.Render(l))
		}
	}
	hint := "enter next · esc back"
	switch o.step {
	case stepWelcome:
		text("Your own agent runs as a node here.", "Other nodes find it; it can ask them.",
			"", "Four questions, then you can chat.", "Change the answers later with /setup.")
		hint = "enter start · esc quit"
		if !o.first {
			hint = "enter start · esc back to the chat"
		}
	case stepEndpoint:
		text("Any OpenAI-compatible API:", "OpenAI, Ollama, LM Studio, vLLM, …", "")
		lines = append(lines, o.input.View())
	case stepKey:
		text("for "+ansi.Truncate(o.endpoint, inner-4, "…"), "")
		lines = append(lines, o.input.View())
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
		text("", "Change it in config.json, under tools.")
		hint = "enter finish · esc back"
	}
	switch {
	case o.busy != "":
		lines = append(lines, "", spin+mutedStyle.Render(" "+o.busy+"…"))
	case o.err != "":
		lines = append(lines, "", errStyle.Render("✗ "+o.err))
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

// progress shows how far the setup is: a dot per page after the welcome.
func (o *onboarding) progress() string {
	dots := make([]string, 0, steps-1)
	for s := stepEndpoint; s < steps; s++ {
		switch {
		case s < o.step:
			dots = append(dots, lipgloss.NewStyle().Foreground(accent).Render("●"))
		case s == o.step:
			dots = append(dots, lipgloss.NewStyle().Foreground(accent).Render("◉"))
		default:
			dots = append(dots, mutedStyle.Render("○"))
		}
	}
	return strings.Join(dots, " ")
}
