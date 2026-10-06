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
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"ouroboros/internal/tui"
	"ouroboros/plugin"
)

// name is the command that opens the UI, and what the front end is called.
const name = "tui"

// needs are the services the plugin uses.
type needs struct {
	Console   *plugin.Console
	Assistant *plugin.Assistant
}

// Plugin contributes the terminal UI as the command "tui" and as the front
// end of ouro.
//
// The UI is where the owner starts and stops the node and decides whom it
// trusts. No service offers that: it is what owner does, a part of the frame
// of ouro that is handed to this plugin where the plugins are listed.
func Plugin(owner *plugin.Owner) plugin.Plugin {
	return plugin.New(name, func(ctx *plugin.Context, n needs) error {
		u := &ui{Owner: owner, ctx: ctx, assistant: n.Assistant}
		const help = "chat with your agent in the terminal UI"
		plugin.Add(ctx, plugin.Command{Name: name, Help: help, Run: func(c context.Context, _ []string) error {
			if !n.Console.Interactive {
				return errors.New("tui needs a terminal")
			}
			return u.run(c)
		}})
		plugin.Add(ctx, plugin.FrontEnd{Name: name, Help: help, Run: u.run})
		return nil
	})
}

// ui is the terminal UI and what it controls: it implements tui.Backend with
// what the owner does and the assistant of the owner.
type ui struct {
	*plugin.Owner
	ctx       *plugin.Context // to read what the plugins contributed to the UI
	assistant *plugin.Assistant

	mu      sync.Mutex
	restart string // the binary a tool asked to start anew from
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
// it, and a node that was not running before is stopped again afterwards. An
// answer still on its way when the user quits is stopped, and the log says
// so before the UI is gone.
//
// When a tool asked for ouro to start anew, the UI quits once the answer is
// in, and run ends with plugin.Restarting: the new process goes on with the
// conversation, which it finds in the file named by the environment variable
// resumeEnv, and stops the node when it quits if this UI would have.
func (u *ui) run(ctx context.Context) error {
	wasRunning := u.Running()
	m := tui.New(u, plugin.All[plugin.Screen](u.ctx), plugin.All[plugin.Slash](u.ctx))
	if r, err := resumed(); err != nil {
		return err
	} else if r != nil {
		if err := u.assistant.Resume(r.Chat.ContextID, r.Memory); err != nil {
			return fmt.Errorf("the conversation to go on with: %w", err)
		}
		m.Resume(r.Chat)
		wasRunning = r.WasRunning
	}
	err := show(ctx, m)
	u.assistant.Stop()
	if m.Restarts() { // as the backend said: see Restarting
		chat := m.Chat()
		return handOver(u.restarting(), resume{chat, u.assistant.Conversation(chat.ContextID), wasRunning})
	}
	if !wasRunning {
		err = errors.Join(err, u.Stop())
	}
	return err
}

// resumeEnv names the file the UI of a new process goes on from.
const resumeEnv = "OURO_RESUME"

// resume is what the UI of a new process goes on from.
type resume struct {
	Chat       tui.Chat        `json:"chat"`
	Memory     json.RawMessage `json:"memory"` // what the agent remembers of the chat, see plugin.Assistant
	WasRunning bool            `json:"was_running"`
}

// handOver writes r to a file only the user can read and returns the
// plugin.Restarting that starts exe with it.
func handOver(exe string, r resume) error {
	f, err := os.CreateTemp("", "ouro-resume-*.json") // 0600
	if err != nil {
		return err
	}
	if err := errors.Join(json.NewEncoder(f).Encode(r), f.Close()); err != nil {
		os.Remove(f.Name())
		return err
	}
	return &plugin.Restarting{Exe: exe, Env: append(os.Environ(), resumeEnv+"="+f.Name())}
}

// resumed reads what the process that started this one handed over, nil if
// it was started otherwise. The file is read once: it is removed, and so is
// the variable that names it, which the node started later must not see.
func resumed() (*resume, error) {
	path, ok := os.LookupEnv(resumeEnv)
	if !ok {
		return nil, nil
	}
	os.Unsetenv(resumeEnv)
	b, err := os.ReadFile(path)
	os.Remove(path)
	if err != nil {
		return nil, fmt.Errorf("the conversation to go on with: %w", err)
	}
	var r resume
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("the conversation to go on with: %w", err)
	}
	return &r, nil
}

// restarting returns the binary a tool asked to start anew from, "" if none.
func (u *ui) restarting() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.restart
}

// Restarting reports whether a tool asked for ouro to start anew.
func (u *ui) Restarting() bool { return u.restarting() != "" }

// Context returns how long the context of the conversation contextID is.
func (u *ui) Context(contextID string) int { return u.assistant.Context(contextID) }

// Contents returns what the next call of the LLM in the conversation
// contextID is sent, message by message: the system prompt, every tool with
// its schema, and the conversation so far.
func (u *ui) Contents(contextID string) ([]tui.Message, error) {
	c, err := u.assistant.Contents(contextID)
	var msgs []tui.Message
	if c.System != "" {
		msgs = append(msgs, tui.Message{Role: "system", Text: c.System})
	}
	for _, t := range c.Tools {
		msgs = append(msgs, tui.Message{Role: "tool " + t.Function.Name,
			Text: t.Function.Description + "\nparameters: " + string(t.Function.Parameters)})
	}
	for _, m := range c.Messages {
		text := m.Content
		for _, call := range m.ToolCalls {
			text += "\n→ " + call.Function.Name + " " + call.Function.Arguments
		}
		msgs = append(msgs, tui.Message{Role: m.Role, Text: strings.TrimSpace(text)})
	}
	return msgs, err
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

// Ask lets the assistant answer. Of the tools it calls on the way, the UI is
// told what the plugins want shown: a call without a plugin.ToolView is not
// reported. Once the call ended, the line carries what the plugin of the tool
// says of its outcome. What the agent says along with the tools it asks for
// goes to say, so that the UI can show the whole loop.
//
// A tool may ask on the way for ouro to start anew, see plugin.Restart.
func (u *ui) Ask(ctx context.Context, contextID, text string, step func(tui.Step), say func(string)) (string, error) {
	ctx = plugin.WithRestart(ctx, func(exe string) {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.restart = exe
	})
	reply, err := u.assistant.Run(ctx, contextID, text, func(s plugin.Step) {
		line := plugin.ToolLine(u.ctx, s)
		if line == "" {
			return
		}
		if s.Done {
			line = plugin.ToolDetail(u.ctx, s, line)
		}
		step(tui.Step{Text: line, Done: s.Done})
	}, say)
	if errors.Is(err, plugin.ErrNotSetUp) {
		return reply, fmt.Errorf("%w, run: ouro start", err)
	}
	return reply, err
}

// Setup starts the setup of the node, which the UI goes through in a dialog
// of its own.
func (u *ui) Setup() (tui.Setup, error) {
	s, err := u.Owner.Setup()
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Models lists the models the provider of the node offers, for /model. A
// node that is not set up is told how to get there in the UI.
func (u *ui) Models(ctx context.Context) (tui.Models, error) {
	models, err := u.Owner.Models(ctx)
	if errors.Is(err, plugin.ErrNotSetUp) {
		return models, fmt.Errorf("%w, type /setup", err)
	}
	return models, err
}

// SetModel saves the model the owner picked in the model screen. A node that
// is not set up is told how to get there in the UI.
func (u *ui) SetModel(model string) error {
	err := u.Owner.SetModel(model)
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
