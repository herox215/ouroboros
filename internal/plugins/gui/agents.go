package gui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"ouroboros/internal/agents"
)

// The window lists the agents of its profile beside the thread: the agent of
// the thread, which is called as the profile is, the archivist, the
// orchestrator and the agents the owner made. Every session runs as one of
// them, see agents. The owner changes what each is called, what it is for
// and its soul, makes agents of their own and removes them again. Renaming
// the agent of the thread renames the profile, as the settings of the
// terminal UI do: the name is that of the node on the network too, and no two
// profiles share one. Everything else is kept in the file of the agents of
// the profile.

// threadAgent is the ID of the agent of the thread among the agents.
const threadAgent = agents.Thread

// Agent is an agent of the profile, as the window lists it.
type Agent struct {
	ID string `json:"id"`
	// Kind is what the agent is: "thread" for the agent of the thread,
	// "archivist" and "orchestrator" for the other built-in ones, "custom"
	// for one the owner made.
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Description is what the agent is for and Soul who it is, as the owner
	// wrote them: "" for nothing.
	Description string `json:"description,omitempty"`
	Soul        string `json:"soul,omitempty"`
	// Access and Model are the model of its own the agent answers with,
	// both "" for the model of the settings, see agents.Agent.
	Access string `json:"access,omitempty"`
	Model  string `json:"model,omitempty"`
	// Reserved says that the agent is kept for the work of the system, the
	// archivist and the orchestrator: what it is for is fixed, and no agent
	// hands it a task.
	Reserved bool `json:"reserved,omitempty"`
}

// AgentChange is what the owner says of an agent: its name, what it is for,
// its soul and the model of its own, both "" for that of the settings. A
// reserved agent takes no description, the agent of the thread no model.
type AgentChange struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Soul        string `json:"soul"`
	Access      string `json:"access"`
	Model       string `json:"model"`
}

// agentOf is the agent change says.
func agentOf(change AgentChange) agents.Agent {
	return agents.Agent{Name: change.Name, Description: change.Description, Soul: change.Soul, Access: change.Access, Model: change.Model}
}

// AccessModels are the models of an access of the node an agent may answer
// with, as the window offers them: Label is what the access is called, and
// Error why it did not list its models now; Models are then those it listed
// the last time, if it ever did.
type AccessModels struct {
	Access   string   `json:"access"`
	Label    string   `json:"label"`
	Provider string   `json:"provider"`
	Models   []string `json:"models"`
	Error    string   `json:"error,omitempty"`
}

// listTime is how long the window waits for the accesses to list their
// models; each gives up sooner, see setup.Session.Models.
const listTime = 30 * time.Second

// errNoModels is what a window answers that cannot list the models: one
// opened without the owner of the node, as tests do.
var errNoModels = errors.New("this window cannot list the models of the node")

// errNoRename is what a window answers that cannot rename the agent: one
// opened without the owner of the node, as tests do.
var errNoRename = errors.New("this window cannot rename the agent")

// Agents lists the agents of the profile, the agent of the thread first, as
// they are now.
func (w *Window) Agents() ([]Agent, error) { return w.profile.agents() }

// UpdateAgent changes the agent id as change says and returns it as it is
// then. The agent of the thread is renamed with its profile: its agent is
// told the new name with the next message.
func (w *Window) UpdateAgent(id string, change AgentChange) (Agent, error) {
	return w.profile.updateAgent(id, change)
}

// CreateAgent makes an agent of the owner's as change says and returns it.
func (w *Window) CreateAgent(change AgentChange) (Agent, error) { return w.profile.createAgent(change) }

// DeleteAgent removes the agent id the owner made.
func (w *Window) DeleteAgent(id string) error { return w.profile.deleteAgent(id) }

// AgentModels lists the accesses of the node with their models, which an
// agent may answer with.
func (w *Window) AgentModels() ([]AccessModels, error) { return w.profile.agentModels() }

// agentModels lists the models an agent may answer with, as
// Window.AgentModels does. An access whose provider is not part of this ouro
// is left out: nothing answers through it.
func (p *profile) agentModels() ([]AccessModels, error) {
	if p.models == nil {
		return nil, errNoModels
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTime)
	defer cancel()
	listed, err := p.models(ctx)
	if err != nil {
		return nil, err
	}
	out := []AccessModels{}
	for _, l := range listed {
		if l.Access.Missing {
			continue
		}
		out = append(out, AccessModels{Access: l.Access.ID, Label: l.Access.Label, Provider: l.Access.Provider, Models: append([]string{}, l.Models...), Error: l.Err})
	}
	return out, nil
}

// checkModel reports why next cannot answer with the model it names, nil if
// it can: an access of the node that offers it. A model the agent answers
// with already, as was, stays, also while its access does not list it.
func (p *profile) checkModel(next agents.Agent, was Agent) error {
	access, model := strings.TrimSpace(next.Access), strings.TrimSpace(next.Model)
	if access == "" || model == "" || access == was.Access && model == was.Model {
		return nil
	}
	list, err := p.agentModels()
	if err != nil {
		return fmt.Errorf("the models of the node cannot be listed: %w", err)
	}
	at := slices.IndexFunc(list, func(l AccessModels) bool { return l.Access == access })
	if at < 0 {
		return fmt.Errorf("the node has no access %q", access)
	}
	l := list[at]
	if !slices.Contains(l.Models, model) {
		if len(l.Models) == 0 && l.Error != "" {
			return fmt.Errorf("the models of %s cannot be listed: %s", l.Label, l.Error)
		}
		return fmt.Errorf("%s does not offer the model %q", l.Label, model)
	}
	return nil
}

// agentStore is where the profile keeps its agents; nil for a window
// without a directory, as tests open, whose agents are the built-in ones
// with their defaults.
func (p *profile) agentStore() *agents.Store {
	if p.dir == "" {
		return nil
	}
	return p.dir.Agents()
}

// agents lists the agents of the profile, as Window.Agents does.
func (p *profile) agents() ([]Agent, error) {
	thread, err := p.threadAgent()
	if err != nil {
		return nil, err
	}
	list, err := p.agentStore().List(thread.Name)
	out := make([]Agent, 0, len(list))
	for _, a := range list {
		out = append(out, Agent(a))
	}
	return out, err
}

// updateAgent changes the agent id, as Window.UpdateAgent does.
func (p *profile) updateAgent(id string, change AgentChange) (Agent, error) {
	now, err := p.agent(id)
	if err != nil {
		return Agent{}, err
	}
	thread, err := p.threadAgent()
	if err != nil {
		return Agent{}, err
	}
	next := agentOf(change)
	if err := p.checkModel(next, now); err != nil {
		return Agent{}, err
	}
	if id != threadAgent || next.Description != now.Description || next.Soul != now.Soul || next.Access != "" || next.Model != "" {
		if err := p.agentStore().Set(id, next, thread.Name); err != nil {
			return Agent{}, err
		}
	}
	if id == threadAgent && strings.TrimSpace(change.Name) != now.Name {
		if p.rename == nil {
			return Agent{}, errNoRename
		}
		if err := p.rename(change.Name); err != nil {
			return Agent{}, err
		}
	}
	return p.agent(id)
}

// createAgent makes an agent of the owner's, as Window.CreateAgent does.
func (p *profile) createAgent(change AgentChange) (Agent, error) {
	thread, err := p.threadAgent()
	if err != nil {
		return Agent{}, err
	}
	next := agentOf(change)
	if err := p.checkModel(next, Agent{}); err != nil {
		return Agent{}, err
	}
	made, err := p.agentStore().Create(next, thread.Name)
	if err != nil {
		return Agent{}, err
	}
	return Agent(made), nil
}

// deleteAgent removes an agent of the owner's, as Window.DeleteAgent does.
func (p *profile) deleteAgent(id string) error {
	if _, err := p.agent(id); err != nil {
		return err
	}
	return p.agentStore().Delete(id)
}

// agent is the agent id of the profile, as it is called now.
func (p *profile) agent(id string) (Agent, error) {
	list, err := p.agents()
	if err != nil {
		return Agent{}, err
	}
	for _, a := range list {
		if a.ID == id {
			return a, nil
		}
	}
	return Agent{}, fmt.Errorf("the profile has no agent %q", id)
}

// threadAgent is the agent of the thread of the profile, as it is called now.
func (p *profile) threadAgent() (Agent, error) {
	status, err := p.status()
	if err != nil {
		return Agent{}, fmt.Errorf("the profile of the window: %w", err)
	}
	if status.Name == "" {
		return Agent{}, errors.New("the profile is not set up yet")
	}
	return Agent{ID: threadAgent, Kind: agents.Thread, Name: status.Name}, nil
}

// label is what the profile is called now, or fallback while that cannot be
// told: the name the window opened with stays until the profile is renamed.
func (p *profile) label(fallback string) string {
	if p.status == nil {
		return fallback
	}
	if status, err := p.status(); err == nil && status.Name != "" {
		return status.Name
	}
	return fallback
}
