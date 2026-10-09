// Package tui is the plugin that contributes the terminal UI: the command
// "tui" and the front end that ouro without a command opens.
//
// It is written against ouroboros/plugin like any other plugin. What it shows
// besides the chat is what the plugins contributed: their screens
// (plugin.Screen), their commands (plugin.Slash) and what is shown of a tool call
// (plugin.ToolView).
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"ouroboros/internal/tui"
	"ouroboros/plugin"
)

// name is the command that opens the UI, and what the front end is called.
const name = "tui"

// needs are the services the plugin uses.
type needs struct {
	Console  *plugin.Console
	Sessions plugin.Sessions
}

// Plugin contributes the terminal UI as the command "tui" and as the front
// end of ouro.
//
// The UI is where the owner starts and stops the node and decides whom it
// trusts. No service offers that: it is what owner does, a part of the frame
// of ouro that is handed to this plugin where the plugins are listed.
//
// profile, if not nil, is handed the Profile of the UI once the plugin is
// started: the frame of ouro shows the profiles of a node in one UI with it,
// see Together.
func Plugin(owner *plugin.Owner, profile func(*Profile)) plugin.Plugin {
	return plugin.New(name, func(ctx *plugin.Context, n needs) error {
		u := &ui{Owner: owner, ctx: ctx, sessions: n.Sessions}
		const help = "chat with your agent in the terminal UI"
		plugin.Add(ctx, plugin.Command{Name: name, Help: help, Run: func(c context.Context, _ []string) error {
			if !n.Console.Interactive {
				return errors.New("tui needs a terminal")
			}
			return u.run(c)
		}})
		plugin.Add(ctx, plugin.FrontEnd{Name: name, Help: help, Run: u.run})
		if profile != nil {
			profile(&Profile{u})
		}
		return nil
	})
}

// ui is the terminal UI and what it controls: it implements tui.Backend with
// what the owner does and the sessions of the owner, which the node holds.
type ui struct {
	*plugin.Owner
	ctx      *plugin.Context // to read what the plugins contributed to the UI
	sessions plugin.Sessions
	profiles []*ui // the profiles of the node the UI shows, if it has several
}

// Profile is the UI of one profile of a node, for the frame of ouro alone.
type Profile struct{ u *ui }

// Together lets the UI of each of profiles show all of them, in their
// order: shift+tab goes on to the next. The UI opens on the one it belongs
// to.
func Together(profiles []*Profile) {
	all := make([]*ui, len(profiles))
	for i, p := range profiles {
		all[i] = p.u
	}
	for _, p := range profiles {
		p.u.profiles = all
	}
}

// show runs the UI m on the terminal until the user quits; tests replace it
// to use the UI without a terminal.
var show = func(ctx context.Context, m *tui.Model) error {
	if ctx.Err() != nil {
		// A program whose context is already given up has nothing to show:
		// starting it reads stdin while it is closed again.
		return ctx.Err()
	}
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}

// run shows the terminal UI until the user quits, with the screens and commands
// the plugins contributed. While it is open the node is online: the UI starts
// it, and a node that was not running before is stopped again afterwards,
// once no answer is on its way. An answer still on its way when the user
// quits goes on in the node.
//
// When a tool asked for ouro to start anew, the UI quits, and run ends with
// plugin.Restarting: the UI of the new process shows the session this one
// showed, as the environment variable resumeEnv says, and stops the node when
// it quits if this UI would have.
func (u *ui) run(ctx context.Context) error {
	wasRunning := u.Running()
	m := u.model()
	r, err := resumed()
	if err != nil {
		return err
	}
	if r != nil {
		m.SelectProfile(r.Profile)
		m.Resume(r.Session)
		wasRunning = r.WasRunning
	}
	defer m.CloseProfiles()
	err = show(ctx, m)
	if exe, ok := m.Restarts(); ok {
		return handOver(exe, resume{Session: m.Current(), WasRunning: wasRunning, Profile: m.Profile()})
	}
	if !wasRunning {
		err = errors.Join(err, u.sessions.Release(context.WithoutCancel(ctx)))
	}
	return err
}

// model is the UI of u, with the screens and the commands the plugins
// contributed: of every profile it shows, if it shows several.
func (u *ui) model() *tui.Model {
	if len(u.profiles) < 2 {
		return tui.New(u, plugin.All[plugin.Screen](u.ctx), plugin.All[plugin.Slash](u.ctx))
	}
	models := make([]*tui.Model, len(u.profiles))
	selected := 0
	for i, p := range u.profiles {
		models[i] = tui.New(p, plugin.All[plugin.Screen](p.ctx), plugin.All[plugin.Slash](p.ctx))
		if p == u {
			selected = i
		}
	}
	return tui.WithProfiles(models, selected)
}

// resumeEnv says what the UI of a new process goes on from.
const resumeEnv = "OURO_RESUME"

// resume is what the UI of a new process goes on from.
type resume struct {
	Profile    string `json:"profile,omitempty"` // the ID of the profile shown, of several
	Session    string `json:"session"`           // the session shown
	WasRunning bool   `json:"was_running"`
}

// handOver returns the plugin.Restarting that starts exe with r.
func handOver(exe string, r resume) error {
	b, _ := json.Marshal(r) // plain data
	return &plugin.Restarting{Exe: exe, Env: append(os.Environ(), resumeEnv+"="+string(b))}
}

// resumed reads what the process that started this one handed over, nil if
// it was started otherwise. The variable is read once: the node started later
// must not see it.
func resumed() (*resume, error) {
	v, ok := os.LookupEnv(resumeEnv)
	if !ok {
		return nil, nil
	}
	os.Unsetenv(resumeEnv)
	var r resume
	if err := json.Unmarshal([]byte(v), &r); err != nil || r.Session == "" {
		return nil, fmt.Errorf("the conversation to go on with: %q is none", v)
	}
	return &r, nil
}

// Ask lets the agent answer text in the session id. A node that is not set
// up, or not running, is told how to get there in the UI.
func (u *ui) Ask(ctx context.Context, id, text string) error {
	err := u.sessions.Ask(ctx, id, text)
	switch {
	case errors.Is(err, plugin.ErrNotSetUp):
		return fmt.Errorf("%w, type /setup", err)
	case errors.Is(err, plugin.ErrNotRunning):
		return fmt.Errorf("%w, type /start", err)
	}
	return err
}

// Watch returns what happened in the session id and what happens from now
// on. Of the tools the agent calls on the way, the UI is told what the
// plugins want shown: a call without a plugin.ToolView shows as nothing.
// Once the call ended, the line carries what the plugin of the tool says of
// its outcome.
func (u *ui) Watch(ctx context.Context, id string) ([]tui.Event, <-chan tui.Event, error) {
	past, live, err := u.sessions.Watch(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	shown := make([]tui.Event, len(past))
	for i, e := range past {
		shown[i] = u.event(e)
	}
	out := make(chan tui.Event)
	go func() {
		defer close(out)
		for e := range live {
			select {
			case out <- u.event(e):
			case <-ctx.Done():
				return
			}
		}
	}()
	return shown, out, nil
}

// event is e as the UI shows it.
func (u *ui) event(e plugin.SessionEvent) tui.Event {
	shown := tui.Event{Kind: e.Kind, Text: e.Text, Done: e.Done, At: e.At, Context: e.Context, Cache: e.Cache}
	if e.Kind == plugin.SessionStep {
		s := e.AsStep()
		shown.Text = plugin.ToolLine(u.ctx, s)
		if shown.Text != "" && s.Done {
			shown.Text = plugin.ToolDetail(u.ctx, s, shown.Text)
		}
	}
	return shown
}

// Steer gives text to the answer on its way in the session id.
func (u *ui) Steer(ctx context.Context, id, text string) error {
	return u.sessions.Steer(ctx, id, text)
}

// Unsteer takes back the last message that is queued in the session id.
func (u *ui) Unsteer(ctx context.Context, id string) (string, error) {
	return u.sessions.Unsteer(ctx, id)
}

// Cancel stops the answer on its way in the session id.
func (u *ui) Cancel(ctx context.Context, id string) error { return u.sessions.Cancel(ctx, id) }

// Efforts returns the efforts the model the node answers with takes, as
// models.dev lists them, and whether it lists the model.
func (u *ui) Efforts() ([]string, bool) { return u.Owner.Efforts(u.providers()) }

// SetSessionEffort makes effort how much the model thinks in the session id.
func (u *ui) SetSessionEffort(ctx context.Context, id, effort string) error {
	return u.sessions.SetEffort(ctx, id, effort)
}

// Sessions lists the sessions of the node.
func (u *ui) Sessions(ctx context.Context) ([]plugin.SessionInfo, error) { return u.sessions.List(ctx) }

// Delete removes the session id from the node.
func (u *ui) Delete(ctx context.Context, id string) error { return u.sessions.Delete(ctx, id) }

// Peers lists the conversations of peers the node has.
func (u *ui) Peers(ctx context.Context) ([]plugin.SessionInfo, error) { return u.sessions.Peers(ctx) }

// Read returns what happened in the conversation of a peer id, as the UI
// shows it.
func (u *ui) Read(ctx context.Context, id string) ([]tui.Event, error) {
	past, err := u.sessions.Read(ctx, id)
	events := make([]tui.Event, len(past))
	for i, e := range past {
		events[i] = u.event(e)
	}
	return events, err
}

// Contents returns what the next call of the LLM in the session id is sent,
// message by message: the system prompt, every tool with its schema, and the
// conversation so far.
func (u *ui) Contents(ctx context.Context, id string) ([]tui.Message, error) {
	lines, err := u.sessions.Contents(ctx, id)
	msgs := make([]tui.Message, len(lines))
	for i, l := range lines {
		msgs[i] = tui.Message{Role: l.Role, Text: l.Text}
	}
	return msgs, err
}

// Settings returns the configuration for the settings screen, with the
// tools and context providers the plugins contributed.
func (u *ui) Settings() (tui.Settings, error) {
	return u.Owner.Settings(plugin.All[plugin.Tool](u.ctx), plugin.All[plugin.ContextProvider](u.ctx))
}

// ChangeSetting makes a change the owner made on the settings screen.
func (u *ui) ChangeSetting(s tui.Setting) (string, error) {
	return u.Owner.Change(s, plugin.All[plugin.Tool](u.ctx), plugin.All[plugin.ContextProvider](u.ctx))
}

// Start starts the node; one that was not set up is told how to get there in
// the UI.
func (u *ui) Start() error {
	err := u.Owner.Start()
	if errors.Is(err, plugin.ErrNotSetUp) {
		return fmt.Errorf("%w, type /setup", err)
	}
	return err
}

// providers are the providers the plugins contributed.
func (u *ui) providers() []plugin.Provider { return plugin.All[plugin.Provider](u.ctx) }

// Setup starts the setup of the node, which the UI goes through in a dialog
// of its own, with the providers the plugins contributed.
func (u *ui) Setup() (tui.Setup, error) {
	s, err := u.Owner.Setup(u.providers())
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Models lists the models of every access of the node, for /model. A node
// that is not set up is told how to get there in the UI.
func (u *ui) Models(ctx context.Context) ([]tui.Models, error) {
	models, err := u.Owner.Models(ctx, u.providers())
	if errors.Is(err, plugin.ErrNotSetUp) {
		return models, fmt.Errorf("%w, type /setup", err)
	}
	return models, err
}

// SetModel saves the model the owner picked in the model screen. A node that
// is not set up is told how to get there in the UI.
func (u *ui) SetModel(access, model string) (bool, error) {
	switched, err := u.Owner.SetModel(access, model)
	if errors.Is(err, plugin.ErrNotSetUp) {
		return false, fmt.Errorf("%w, type /setup", err)
	}
	return switched, err
}

// SetPeerModel saves the model the owner picked for peers in the model
// screen, like SetModel.
func (u *ui) SetPeerModel(access, model string) error {
	err := u.Owner.SetPeerModel(access, model)
	if errors.Is(err, plugin.ErrNotSetUp) {
		return fmt.Errorf("%w, type /setup", err)
	}
	return err
}

func (u *ui) Contacts() (tui.Contacts, error) {
	incoming, outgoing, err := u.Requests()
	if err != nil {
		return tui.Contacts{}, err
	}
	proposals, err := u.Proposals()
	if err != nil {
		return tui.Contacts{}, err
	}
	return tui.Contacts{Incoming: incoming, Outgoing: outgoing, Proposals: proposals}, nil
}
