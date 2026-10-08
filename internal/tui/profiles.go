package tui

import (
	"cmp"
	"reflect"
	"strconv"

	tea "charm.land/bubbletea/v2"
)

// The UI of a node with several profiles is a Model for each of them, made
// by WithProfiles: shift+tab shows the next one. Each keeps all it shows,
// the conversation, what is typed in it, the screen, and watches its
// sessions while another one is shown: an answer goes on, and comes in
// where it belongs. A Model is started the first time it is shown.
type profileViews struct {
	models  []*Model
	shown   int
	started []bool
	size    tea.WindowSizeMsg // of the terminal, for one that is shown anew
}

// profileMsg is a message of what the Model of a profile started: it goes to
// that Model, whichever is shown.
type profileMsg struct {
	profile int
	msg     tea.Msg
}

// WithProfiles returns the UI that shows the models of the profiles of a
// node, starting with models[shown]. Each model is told which of the profiles
// it is, so that it shows that at the top.
func WithProfiles(models []*Model, shown int) *Model {
	for i, m := range models {
		m.profileAt, m.profileOf = i+1, len(models)
	}
	return &Model{profiles: &profileViews{models: models, shown: shown, started: make([]bool, len(models))}}
}

func (p *profileViews) current() *Model { return p.models[p.shown] }

// init starts the Model shown.
func (p *profileViews) init() tea.Cmd {
	p.started[p.shown] = true
	return tagged(p.shown, p.current().Init())
}

// update hands msg to the Model it is for: the one that started what it is
// the outcome of, every one for the size of the terminal, the one shown for
// the rest. shift+tab shows the next one.
func (p *profileViews) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case profileMsg:
		_, cmd := p.models[msg.profile].Update(msg.msg)
		return tagged(msg.profile, cmd)
	case tea.WindowSizeMsg:
		p.size = msg
		cmds := make([]tea.Cmd, len(p.models))
		for i, m := range p.models {
			_, cmd := m.Update(msg)
			cmds[i] = tagged(i, cmd)
		}
		return tea.Batch(cmds...)
	case tea.KeyPressMsg:
		if msg.String() == "shift+tab" {
			return p.next()
		}
	}
	_, cmd := p.current().Update(msg)
	return tagged(p.shown, cmd)
}

// next shows the next profile, which is started if it was not yet: without
// the splash, which the first one showed.
func (p *profileViews) next() tea.Cmd {
	p.shown = (p.shown + 1) % len(p.models)
	m := p.current()
	var cmd tea.Cmd
	if !p.started[p.shown] {
		p.started[p.shown] = true
		cmd = m.Init()
		m.splash.show = false
	}
	_, resized := m.Update(p.size)
	who, err := m.backend.Status()
	if err != nil {
		m.setNotice("profile "+strconv.Itoa(p.shown+1)+" · shift+tab shows the next", false)
	} else {
		m.setNotice("profile "+cmp.Or(who.Name, who.ID)+" ("+who.ID+") · shift+tab shows the next", false)
	}
	return tagged(p.shown, tea.Batch(cmd, resized))
}

// tagged is cmd, whose messages go to the Model of profile: the messages of
// Bubble Tea itself, such as the one that quits, go to the program as they
// are.
func tagged(profile int, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		switch msg := cmd().(type) {
		case nil:
			return nil
		case tea.BatchMsg:
			for i, c := range msg {
				msg[i] = tagged(profile, c)
			}
			return msg
		default:
			if reflect.TypeOf(msg).PkgPath() == reflect.TypeFor[tea.BatchMsg]().PkgPath() {
				return msg
			}
			return profileMsg{profile, msg}
		}
	}
}

// Profile is the ID of the profile the UI shows, of several: "" for a UI of
// one, and while the node cannot tell.
func (m *Model) Profile() string {
	if m.profiles == nil {
		return ""
	}
	return m.profiles.current().id()
}

// id is the ID of the node the UI shows, as it tells it; "" while it cannot.
func (m *Model) id() string {
	s, err := m.backend.Status()
	if err != nil {
		return ""
	}
	return s.ID
}

// SelectProfile shows the profile id, before the UI is shown: the one it
// shows already if there is none.
func (m *Model) SelectProfile(id string) {
	if m.profiles == nil {
		return
	}
	for i, model := range m.profiles.models {
		if model.id() == id {
			m.profiles.shown = i
			return
		}
	}
}

// CloseProfiles ends what the models of the profiles still ask of the node,
// once the UI is gone.
func (m *Model) CloseProfiles() {
	if m.profiles == nil {
		return
	}
	for _, model := range m.profiles.models {
		model.quit()
	}
}
