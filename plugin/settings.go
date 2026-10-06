package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/config"
	"ouroboros/internal/kernel"
	"ouroboros/internal/sandbox"
)

// What the owner sees and changes with /settings: the configuration of the
// node, without its secrets, with the defaults that hold for what is not set,
// and what this build of ouro has to choose from. Settings returns it, and
// Change changes one thing of it at a time, checked the way the node checks
// its configuration before anything is written.

// Audience is whom a list of tools is for.
type Audience int

// The two lists of tools.
const (
	ForOwner Audience = iota // h2a: the agent of the owner
	ForPeers                 // a2a: the agent that answers connected peers
)

// Settings is the configuration of the node as the owner sees it in a front
// end. The key of the node and the key of the API are not in it: HasKey says
// whether there is one for the API.
type Settings struct {
	Name, Endpoint, Model, ID string
	HasKey                    bool
	// ContextProvider is the one selected, "" for none; ContextProviders
	// are the ones this build has.
	ContextProvider  string
	ContextProviders []string
	// RebuildWithoutSandbox lets rebuild build where there is no sandbox;
	// Sandbox says why there is none here, "" if there is.
	RebuildWithoutSandbox bool
	Sandbox               string
	Plugins               []PluginState
	// PluginsKept says why the plugins the configuration turns off run all
	// the same, "" if they do not.
	PluginsKept string
	Tools       [2]ToolList // by Audience
	// Protected are the paths no tool for files touches; ProtectedDefault
	// says that the configuration has none and the default holds.
	Protected        []string
	ProtectedDefault bool
}

// PluginState is a plugin of this build as the owner sees it.
type PluginState struct {
	Name    string
	On      bool // the configuration does not turn it off
	Running bool // in this process: it differs from On until ouro starts anew
	// Fixed says why it cannot be turned off, "" if it can.
	Fixed string
	// Missing says that the configuration turns off a plugin that is not
	// part of this build, as after a rebuild without it.
	Missing bool
}

// ToolList is a list of tools as it applies.
type ToolList struct {
	All   bool // every tool, "*"; for peers every tool but those of the owner alone
	Unset bool // the configuration has no list: the default holds
	Tools []ToolState
}

// ToolState is a tool in a list.
type ToolState struct {
	Name, Description string
	Allowed           bool
	Listed            bool   // by its name, not only by "*"
	Settings          string // its settings as a JSON object, "" for none
	Configurable      bool   // it takes settings
	OwnerOnly         bool   // never for peers
}

// SettingKind is what a Setting changes.
type SettingKind int

// The kinds of a Setting.
const (
	SetName                  SettingKind = iota // Value
	SetContextProvider                          // Value, "" for none
	SetRebuildWithoutSandbox                    // On
	SetPlugin                                   // Name, On; a missing one is forgotten
	SetAllTools                                 // For, On
	SetTool                                     // For, Name, On
	SetToolSettings                             // For, Name, Value: a JSON object, "" for none
	SetProtected                                // Value: one path a line; On resets to the default
)

// Setting is one change the owner makes in the settings.
type Setting struct {
	Kind  SettingKind
	For   Audience
	Name  string
	On    bool
	Value string
}

// catalog is what this build has of plugins, see Activate.
type catalog struct {
	all     []string          // every plugin of the build
	fixed   map[string]string // those that cannot be turned off, with why
	check   func(names []string) error
	running []string // in this process
	kept    string   // why those turned off run all the same
}

// Activate returns the plugins of all that run: all but those the
// configuration of the node of owner turns off. fixed are the plugins that
// cannot be turned off, with why; the configuration does not turn them off.
// If the plugins left cannot start together, as one needs what one turned
// off provides, all of them run, and the settings of owner say why. It is
// for the list of plugins.
func Activate(owner *Owner, all []Plugin, fixed map[string]string) []Plugin {
	byName := map[string]Plugin{}
	names := make([]string, len(all))
	for i, p := range all {
		byName[p.Name], names[i] = p, p.Name
	}
	pick := func(names []string) []Plugin {
		picked := make([]Plugin, len(names))
		for i, n := range names {
			picked[i] = byName[n]
		}
		return picked
	}
	owner.plugins = catalog{all: names, fixed: fixed, check: func(names []string) error { return kernel.Check(pick(names)...) }}
	cfg, _ := owner.home.Config() // a configuration that cannot be read turns nothing off
	running := owner.plugins.on(cfg.PluginsOff())
	if err := owner.plugins.check(running); err != nil {
		owner.plugins.kept, running = err.Error(), names
	}
	owner.plugins.running = running
	return pick(running)
}

// on are the plugins of the build that off leaves on.
func (c catalog) on(off []string) []string {
	return slices.DeleteFunc(slices.Clone(c.all), func(name string) bool {
		return slices.Contains(off, name) && c.fixed[name] == ""
	})
}

// builder is the blueprint of the agents with tools, to check a
// configuration with.
func (o *Owner) builder(tools []Tool) assistant.Builder {
	return assistant.Builder{Tools: func() []agent.Tool { return tools }}.WithConfig(o.home.Config, o.home.Proposals())
}

// Settings returns the configuration of the node as the owner sees it, for
// tools and providers, which are the tools and context providers the
// plugins contributed.
func (o *Owner) Settings(tools []Tool, providers []ContextProvider) (Settings, error) {
	cfg, err := o.home.Config()
	if err != nil {
		return Settings{}, err
	}
	s := Settings{
		Name: cfg.Name, Endpoint: cfg.BaseURL, Model: cfg.Model, ID: cfg.ID(), HasKey: cfg.APIKey != "",
		ContextProvider: cfg.ContextProvider, RebuildWithoutSandbox: cfg.RebuildWithoutSandbox,
		PluginsKept: o.plugins.kept, Protected: cfg.Protected(),
		ProtectedDefault: cfg.Tools == nil || cfg.Tools.Protected == nil,
	}
	for _, p := range providers {
		if !slices.Contains(s.ContextProviders, p.Name) {
			s.ContextProviders = append(s.ContextProviders, p.Name)
		}
	}
	if err := sandbox.Available(); err != nil {
		s.Sandbox = strings.TrimPrefix(err.Error(), sandbox.ErrUnavailable.Error()+": ")
	}
	off := cfg.PluginsOff()
	on := o.plugins.on(off)
	for _, name := range o.plugins.all {
		state := PluginState{Name: name, On: slices.Contains(on, name),
			Running: slices.Contains(o.plugins.running, name), Fixed: o.plugins.fixed[name]}
		// One that others need is fixed while they are on.
		if state.On && state.Fixed == "" {
			if err := o.plugins.check(o.plugins.on(append(slices.Clone(off), name))); err != nil {
				state.Fixed = "a plugin that is on needs it: " + needs(err)
			}
		}
		s.Plugins = append(s.Plugins, state)
	}
	for _, name := range off {
		if !slices.Contains(o.plugins.all, name) {
			s.Plugins = append(s.Plugins, PluginState{Name: name, Missing: true})
		}
	}
	all := o.builder(tools).Available()
	s.Tools[ForOwner] = toolList(all, cfg.OwnerTools(), cfg.Tools == nil || cfg.Tools.H2A == nil, false)
	s.Tools[ForPeers] = toolList(all, cfg.PeerTools(), cfg.Tools == nil || cfg.Tools.A2A == nil, true)
	return s, nil
}

// toolList is the list entries of the tools all as it applies.
func toolList(all []agent.Tool, entries []config.Entry, unset, peers bool) ToolList {
	l := ToolList{Unset: unset}
	listed := map[string]config.Entry{}
	for _, e := range entries {
		if e.Tool == config.AllTools {
			l.All = true
		}
		listed[e.Tool] = e
	}
	for _, t := range all {
		e, named := listed[t.Name]
		state := ToolState{Name: t.Name, Description: t.Description, Listed: named, Configurable: t.Configure != nil, OwnerOnly: t.OwnerOnly}
		state.Allowed = (named || l.All) && !(peers && t.OwnerOnly)
		if settings := string(e.Settings); settings != "{}" { // none at all is ""
			state.Settings = settings
		}
		l.Tools = append(l.Tools, state)
	}
	return l
}

// Change makes the change s of the configuration of the node and returns
// from when it holds. tools and providers are those the plugins contributed.
// The configuration that would result is checked the way the node checks it
// when it starts: what would keep a node from starting or an agent from
// answering is refused, and nothing is written. The keys are no setting:
// the setup changes them.
func (o *Owner) Change(s Setting, tools []Tool, providers []ContextProvider) (string, error) {
	b := o.builder(tools)
	var holds string
	err := config.Update(string(o.home), func(c *config.Config) error {
		var err error
		if holds, err = o.change(c, s, b.Available(), providers); err != nil {
			return err
		}
		return b.Check(*c)
	})
	if err != nil {
		return "", err
	}
	o.home.Log("the owner changed the settings: %s", s)
	return holds, nil
}

// String names a change for the log: what it changes, not to what.
func (s Setting) String() string {
	what := [...]string{"the name", "the context provider", "rebuild without a sandbox", "the plugin", "all tools", "the tool", "the settings of the tool", "the protected paths"}[s.Kind]
	if s.Name != "" {
		what += " " + s.Name
	}
	if s.Kind >= SetAllTools && s.Kind <= SetToolSettings {
		what += [...]string{" of the owner", " for peers"}[s.For]
	}
	return what
}

// From when a change holds.
const (
	holdsNow     = "saved: it holds from the next message, for you and for peers"
	holdsRebuild = "saved: it holds for the next rebuild"
	holdsRestart = "saved: it holds once ouro starts anew: quit and start ouro, /stop and /start the node"
)

// change makes the change s of c, with the tools all and providers.
func (o *Owner) change(c *config.Config, s Setting, all []agent.Tool, providers []ContextProvider) (string, error) {
	switch s.Kind {
	case SetName:
		name, _ := json.Marshal(strings.TrimSpace(s.Value)) // a text: cannot fail
		return "saved: the node is called so from now on, also on the network", c.SetField(config.FieldName, name)
	case SetContextProvider:
		if s.Value != "" && !slices.ContainsFunc(providers, func(p ContextProvider) bool { return p.Name == s.Value }) {
			return "", fmt.Errorf("this build has no context provider %q", s.Value)
		}
		c.ContextProvider = s.Value
		return holdsNow, nil
	case SetRebuildWithoutSandbox:
		c.RebuildWithoutSandbox = s.On
		return holdsRebuild, nil
	case SetPlugin:
		return holdsRestart, o.switchPlugin(c, s.Name, s.On)
	case SetAllTools, SetTool, SetToolSettings:
		entries, err := changeTools(listOf(c, s.For), s, all)
		if err != nil {
			return "", err
		}
		setList(c, s.For, entries)
		return holdsNow, nil
	case SetProtected:
		if s.On {
			tools := copyTools(c)
			tools.Protected = nil
			return holdsNow, nil
		}
		var paths []string
		for line := range strings.Lines(s.Value) {
			if line = strings.TrimSpace(line); line != "" && !slices.Contains(paths, line) {
				paths = append(paths, line)
			}
		}
		value, _ := json.Marshal(append([]string{}, paths...)) // texts: cannot fail
		return holdsNow, c.SetField(config.FieldProtected, value)
	}
	return "", fmt.Errorf("no such setting: %d", s.Kind)
}

// switchPlugin turns the plugin name of the build on or off in c, or
// forgets one that the build does not have.
func (o *Owner) switchPlugin(c *config.Config, name string, on bool) error {
	off := slices.DeleteFunc(slices.Clone(c.PluginsOff()), func(n string) bool { return n == name })
	if !slices.Contains(o.plugins.all, name) {
		if !on {
			return fmt.Errorf("this build has no plugin %q", name)
		}
	} else {
		if why := o.plugins.fixed[name]; why != "" && !on {
			return fmt.Errorf("%s cannot be turned off: %s", name, why)
		}
		if !on {
			off = append(off, name)
		}
		if err := o.plugins.check(o.plugins.on(off)); err != nil {
			return fmt.Errorf("so ouro cannot start: %s", needs(err))
		}
	}
	slices.Sort(off)
	c.Plugins = &config.Plugins{Off: off}
	if len(off) == 0 {
		c.Plugins = nil
	}
	return nil
}

// copyTools gives c a copy of its tools to change, which the configuration it
// was read as does not share.
func copyTools(c *config.Config) *config.Tools {
	tools := config.Tools{}
	if c.Tools != nil {
		tools = *c.Tools
	}
	c.Tools = &tools
	return c.Tools
}

// listOf is the list of tools for who in c, as it applies.
func listOf(c *config.Config, who Audience) []config.Entry {
	if who == ForOwner {
		return slices.Clone(c.OwnerTools())
	}
	return slices.Clone(c.PeerTools())
}

// setList makes entries the list of tools for who in c.
func setList(c *config.Config, who Audience, entries []config.Entry) {
	if entries == nil {
		entries = []config.Entry{} // none is an empty list: null would be the default
	}
	tools := copyTools(c)
	if who == ForOwner {
		tools.H2A = entries
	} else {
		tools.A2A = entries
	}
}

// changeTools returns entries, a list of tools of all, with the change s.
func changeTools(entries []config.Entry, s Setting, all []agent.Tool) ([]config.Entry, error) {
	peers := s.For == ForPeers
	every := slices.ContainsFunc(entries, func(e config.Entry) bool { return e.Tool == config.AllTools })
	if s.Kind == SetAllTools {
		switch {
		case s.On && !every:
			// A name next to "*" says nothing: the tools with settings stay.
			kept := slices.DeleteFunc(entries, func(e config.Entry) bool { return e.Settings == nil })
			return append([]config.Entry{{Tool: config.AllTools}}, kept...), nil
		case !s.On && every:
			return named(entries, all, peers, ""), nil
		}
		return entries, nil
	}
	at := slices.IndexFunc(all, func(t agent.Tool) bool { return t.Name == s.Name })
	if at < 0 {
		return nil, fmt.Errorf("there is no tool %q", s.Name)
	}
	if peers && all[at].OwnerOnly {
		return nil, fmt.Errorf("%s is for your agent alone: no peer gets it", s.Name)
	}
	listed := slices.IndexFunc(entries, func(e config.Entry) bool { return e.Tool == s.Name })
	if s.Kind == SetTool {
		switch {
		case s.On && listed < 0 && !every:
			return append(entries, config.Entry{Tool: s.Name}), nil
		case !s.On && every:
			return named(entries, all, peers, s.Name), nil
		case !s.On && listed >= 0:
			return slices.Delete(entries, listed, listed+1), nil
		}
		return entries, nil
	}
	settings, err := settingsOf(s.Value)
	switch {
	case err != nil:
		return nil, err
	case settings == nil && listed >= 0 && every:
		return slices.Delete(entries, listed, listed+1), nil
	case settings == nil && listed >= 0:
		entries[listed].Settings = nil
	case settings != nil && listed >= 0:
		entries[listed].Settings = settings
	case settings != nil:
		entries = append(entries, config.Entry{Tool: s.Name, Settings: settings})
	}
	return entries, nil
}

// named is entries without "*", with every tool of all that "*" gave named
// instead, but except: so that the tools it gave are the same, but except.
func named(entries []config.Entry, all []agent.Tool, peers bool, except string) []config.Entry {
	out := slices.DeleteFunc(entries, func(e config.Entry) bool { return e.Tool == config.AllTools || e.Tool == except })
	for _, t := range all {
		if t.Name == except || peers && t.OwnerOnly || slices.ContainsFunc(out, func(e config.Entry) bool { return e.Tool == t.Name }) {
			continue
		}
		out = append(out, config.Entry{Tool: t.Name})
	}
	return out
}

// settingsOf reads the settings of a tool as the owner typed them: a JSON
// object, nil for none.
func settingsOf(value string) (json.RawMessage, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "{}" {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &object); err != nil {
		return nil, errors.New(`the settings of a tool are a JSON object, like {"allow": ["~/share"]}`)
	}
	compact, _ := json.Marshal(object) // read just now: cannot fail
	return compact, nil
}

// needs is why plugins cannot start together, without the advice to the one
// who lists them.
func needs(err error) string {
	why, _, _ := strings.Cut(err.Error(), ": add a plugin that provides it")
	return why
}
