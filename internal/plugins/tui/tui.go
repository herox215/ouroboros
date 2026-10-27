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
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"ouroboros/internal/tui"
	"ouroboros/internal/workdir"
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
		plugin.Add(ctx, u.usageScreen())
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
	m.SetInitialDirectory(workdir.Of(ctx))
	r, err := resumed()
	if err != nil {
		return err
	}
	if r != nil {
		m.SelectProfile(r.Profile)
		m.Resume(r.Session)
		if r.Draft != nil {
			m.SetDraft(*r.Draft)
		}
		wasRunning = r.WasRunning
	}
	defer m.CloseProfiles()
	err = show(ctx, m)
	if exe, ok := m.Restarts(); ok {
		goOn := resume{Session: m.Current(), WasRunning: wasRunning, Profile: m.Profile()}
		if d := m.Draft(); d.Text != "" {
			// Keys do not reach the input once the restart is announced: the
			// draft is what was typed before, to go on with in the new build.
			goOn.Draft = &d
		}
		return handOver(exe, goOn)
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

// maxResumeEnv is how many bytes resumeEnv holds at most. Linux takes no more
// than 128 KiB for one variable, and macOS 1 MiB for the arguments and the
// whole environment together: a process that is to start with more does not
// start, and ouro ends instead of starting anew. A longer draft goes in a
// file, see saveDraft.
const maxResumeEnv = 32 << 10

// draftPattern names the files of the drafts that go over in a file, in the
// directory for temporary files; draftPrefix is how such a name starts.
const (
	draftPrefix  = "ouro-draft-"
	draftPattern = draftPrefix + "*.json"
)

// resume is what the UI of a new process goes on from.
type resume struct {
	Profile    string     `json:"profile,omitempty"` // the ID of the profile shown, of several
	Session    string     `json:"session"`           // the session shown
	WasRunning bool       `json:"was_running"`
	Draft      *tui.Draft `json:"draft,omitempty"`      // what was typed into the input, not sent
	DraftFile  string     `json:"draft_file,omitempty"` // the file of a draft too long for the environment
}

// handOver returns the plugin.Restarting that starts exe with r. A draft that
// makes resumeEnv longer than maxResumeEnv goes in a file of its own, which
// resumeEnv names; without the file the draft stays behind, as ouro starts
// anew all the same.
func handOver(exe string, r resume) error {
	b, _ := json.Marshal(r) // plain data
	if len(b) > maxResumeEnv && r.Draft != nil {
		r.DraftFile = saveDraft(*r.Draft)
		r.Draft = nil
		b, _ = json.Marshal(r)
	}
	return &plugin.Restarting{Exe: exe, Env: append(os.Environ(), resumeEnv+"="+string(b))}
}

// saveDraft writes d into a new file that only the owner may read, and
// returns its path: "" if there is none. A file that is not written in full
// is not read back, see loadDraft.
func saveDraft(d tui.Draft) string {
	f, err := os.CreateTemp("", draftPattern)
	if err != nil {
		return ""
	}
	defer f.Close()
	json.NewEncoder(f).Encode(d) // plain data
	return f.Name()
}

// loadDraft reads the draft that saveDraft wrote into path, and removes the
// file: nil if it cannot be read, as the UI goes on without it. A path that
// names no such file is left alone.
func loadDraft(path string) *tui.Draft {
	if filepath.Dir(path) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(path), draftPrefix) {
		return nil
	}
	b, err := os.ReadFile(path)
	os.Remove(path)
	var d tui.Draft
	if err != nil || json.Unmarshal(b, &d) != nil {
		return nil
	}
	return &d
}

// resumed reads what the process that started this one handed over, nil if
// it was started otherwise. The variable is read once: the node started later
// must not see it. A draft that went over in a file is read from it, see
// loadDraft.
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
	if r.DraftFile != "" {
		r.Draft = loadDraft(r.DraftFile)
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
	shown := tui.Event{Kind: e.Kind, Text: e.Text, Done: e.Done, At: e.At, Context: e.Context, Cache: e.Cache, Routed: e.Routed}
	if e.Kind == plugin.SessionStep {
		s := e.AsStep()
		shown.Tool, shown.Failed = s.Tool, s.Done && s.Err != nil
		shown.Text = plugin.ToolLine(u.ctx, s)
		if shown.Text != "" && s.Done {
			shown.Text = plugin.ToolDetail(u.ctx, s, shown.Text)
		}
	}
	if e.Kind == plugin.SessionTask {
		shown.Text, shown.Failed = taskLine(e), e.Err != ""
	}
	if e.Kind == plugin.SessionCalling {
		shown.Text = size(e.N)
	}
	if e.Kind == plugin.SessionWorking {
		shown.N = e.N
	}
	if e.Kind == plugin.SessionRemembered {
		shown.Remembered = e.Args
	}
	if e.Kind == plugin.SessionAsked && !e.Done {
		q := e.AsQuestion()
		shown.Text, shown.Options, shown.Recommended = q.Question, q.Options, q.Recommended
	}
	return shown
}

// size is n bytes as the chat tells them: "512 bytes", "3.2 KiB".
func size(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/1024)
}

// taskLine is what the chat shows of the report of a task the agent
// delegated: its number and what it was, and how it ended.
func taskLine(e plugin.SessionEvent) string {
	line := fmt.Sprintf("task %d · %s", e.N, e.Remark)
	if e.Err != "" {
		return line + " ✗ " + strings.Join(strings.Fields(e.Err), " ")
	}
	return line + " ✓ " + e.Took.Round(time.Second).String()
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

// SetSessionDirectory changes the project directory of the session id. The
// sessions service validates it and rejects changes while an answer is busy.
func (u *ui) SetSessionDirectory(ctx context.Context, id, path string) error {
	return u.sessions.SetDirectory(ctx, id, path)
}

// Sessions lists the sessions of the node.
func (u *ui) Sessions(ctx context.Context) ([]plugin.SessionInfo, error) { return u.sessions.List(ctx) }

// Delete removes the session id from the node.
func (u *ui) Delete(ctx context.Context, id string) error { return u.sessions.Delete(ctx, id) }

// Peers lists the conversations of peers the node has.
func (u *ui) Peers(ctx context.Context) ([]plugin.SessionInfo, error) { return u.sessions.Peers(ctx) }

// Find returns the sessions whose content holds query, as Sessions and Peers
// name them.
func (u *ui) Find(ctx context.Context, query string) ([]string, error) {
	return u.sessions.Find(ctx, query)
}

// Jobs lists the commands that run in the background for the answers on
// their way.
func (u *ui) Jobs(ctx context.Context) ([]plugin.Job, error) { return u.sessions.Jobs(ctx) }

// StopJob stops the job n of the session or conversation of a peer id.
func (u *ui) StopJob(ctx context.Context, id string, n int) error {
	return u.sessions.StopJob(ctx, id, n)
}

// Services lists the services that run past the answers.
func (u *ui) Services(ctx context.Context) ([]plugin.ServiceInfo, error) {
	return u.sessions.Services(ctx)
}

// StopService stops the service id.
func (u *ui) StopService(ctx context.Context, id string) error {
	return u.sessions.StopService(ctx, id)
}

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

// Gateways reads contributions at use time, after all plugins have started.
func (u *ui) Gateways(ctx context.Context) ([]plugin.GatewaySettings, error) {
	out := []plugin.GatewaySettings{}
	for _, g := range plugin.All[plugin.Gateway](u.ctx) {
		if g.Settings == nil {
			continue
		}
		s, err := g.Settings(ctx)
		if err != nil {
			return nil, err
		}
		s.Name, s.Title = g.Name, g.Title
		out = append(out, s)
	}
	return out, nil
}

func (u *ui) SaveGateway(ctx context.Context, name string, change plugin.GatewayChange) error {
	for _, g := range plugin.All[plugin.Gateway](u.ctx) {
		if g.Name == name && g.Save != nil {
			return g.Save(ctx, change)
		}
	}
	return fmt.Errorf("gateway %q is not available", name)
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

// SetSessionModel saves the model the owner picked for the sessions in the
// model screen, like SetPeerModel.
func (u *ui) SetSessionModel(access, model string) error {
	err := u.Owner.SetSessionModel(access, model)
	if errors.Is(err, plugin.ErrNotSetUp) {
		return fmt.Errorf("%w, type /setup", err)
	}
	return err
}

// SetThreadModel saves the model the owner picked for the thread in the
// model screen, like SetPeerModel.
func (u *ui) SetThreadModel(access, model string) error {
	err := u.Owner.SetThreadModel(access, model)
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
	suggestions, err := u.Suggestions()
	if err != nil {
		return tui.Contacts{}, err
	}
	return tui.Contacts{Incoming: incoming, Outgoing: outgoing, Proposals: proposals, Suggestions: suggestions}, nil
}
