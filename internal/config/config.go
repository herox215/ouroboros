// Package config persists the node configuration.
package config

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ouroboros/internal/identity"
	"ouroboros/internal/printable"
	"ouroboros/internal/safefile"
)

const (
	fileName = "config.json"
	// lockName is the file that is locked while the configuration is changed.
	// The configuration itself cannot be: saving replaces the file.
	lockName = fileName + ".lock"
)

// Config is the persistent node configuration.
type Config struct {
	ContextProvider string `json:"context_provider,omitempty"`
	// Access is the ID of the access the agents reach their LLM with, one of
	// the credentials of the node (see package credentials), and Model the
	// model they answer with there.
	Access string `json:"access,omitempty"`
	Model  string `json:"model"`
	// PeerAccess and PeerModel are the access and the model the agent answers
	// peers with, if the owner chose others for them than for themselves.
	// Without both, peers are answered with Access and Model: see PeerLLM.
	PeerAccess string `json:"peer_access,omitempty"`
	PeerModel  string `json:"peer_model,omitempty"`
	// SessionAccess and SessionModel are the access and the model the agent
	// answers the sessions of the owner with, if the owner chose others for
	// them than for themselves. Without both, sessions are answered with
	// Access and Model: see SessionLLM.
	SessionAccess string `json:"session_access,omitempty"`
	SessionModel  string `json:"session_model,omitempty"`
	// ThreadAccess and ThreadModel are the access and the model the agent
	// answers the thread with, if the owner chose others for it than for
	// themselves. Without both, the thread is answered with Access and
	// Model: see ThreadLLM.
	ThreadAccess string `json:"thread_access,omitempty"`
	ThreadModel  string `json:"thread_model,omitempty"`
	// Fallbacks are what the agent goes on with, in this order, when the
	// access it answers the owner with cannot answer for now: see Fallback.
	// Without them, such an answer stops with why.
	Fallbacks []Fallback `json:"fallbacks,omitempty"`
	// Effort is how much the model thinks before it answers the owner, one
	// of Efforts: "" leaves it to the model. Peers are answered as the model
	// does by itself.
	Effort string `json:"effort,omitempty"`
	// BaseURL and APIKey are how a configuration from before there were
	// accesses names its LLM: an OpenAI-compatible endpoint and the key for
	// it. Such a configuration is still read. The node makes an access of
	// them and takes them out of this file, see home.Dir.Config.
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	Name    string `json:"name"`
	// Description is what the node says about itself in its agent card, ""
	// for the default, see About.
	Description string `json:"description,omitempty"`
	// MemoryInterval is how long after the last pass the archivist may run
	// again, as a Go duration like "24h"; "off" or "" turns it off. A
	// configuration that says nothing of it leaves the archivist off, see
	// MemoryArchive.
	MemoryInterval *string `json:"memory_interval,omitempty"`
	// PrivateKey is the base64 Ed25519 key that identifies this node to peers.
	// It is generated on first use, so older configs may not have it yet.
	// The ID of the node follows from it, see ID. Older configs also stored
	// a random "id"; it is ignored and gone when the file is written again.
	PrivateKey string `json:"private_key,omitempty"`
	// Tools says which tools the agent may use, by who it is talking to, and
	// how far they go. Without it the owner gets every tool and peers get none.
	Tools *Tools `json:"tools,omitempty"`
	// SelfImprove says what the agent of the owner does where ouro lacks what
	// a task needs, if it may build ouro: SelfImproveBuild, the default, or
	// SelfImprovePropose. Only the owner sets it: no proposal does.
	SelfImprove string `json:"self_improve,omitempty"`
	// Plugins says which plugins of the build are turned off.
	Plugins *Plugins `json:"plugins,omitempty"`
}

// What SelfImprove says.
const (
	// SelfImproveBuild lets the agent build into ouro what a task needs and
	// ouro lacks, when that is the better way than a workaround.
	SelfImproveBuild = "build"
	// SelfImprovePropose lets it propose that change and no more: it changes
	// ouro only when the owner asks.
	SelfImprovePropose = "propose"
)

// Improves says whether the agent of the owner builds into ouro what a task
// needs and ouro lacks, see SelfImprove.
func (c Config) Improves() bool { return c.SelfImprove != SelfImprovePropose }

// ValidSelfImprove reports a value of self_improve that is none of its own.
func ValidSelfImprove(value string) error {
	if value != "" && value != SelfImproveBuild && value != SelfImprovePropose {
		return fmt.Errorf("self_improve is %q or %q, not %q", SelfImproveBuild, SelfImprovePropose, value)
	}
	return nil
}

// Plugins are the plugins of the build the owner turned off, by name. They
// are left out when ouro starts.
type Plugins struct {
	Off []string `json:"off"`
}

// PluginsOff are the names of the plugins that are turned off.
func (c Config) PluginsOff() []string {
	if c.Plugins == nil {
		return nil
	}
	return c.Plugins.Off
}

// AllTools in a list of tools allows every tool.
const AllTools = "*"

// Tools says what the agent may do, one list of tools per kind of
// conversation. The names are those the tools are registered with.
type Tools struct {
	// H2A (human to agent) applies when the owner asks. Missing or null
	// allows every tool.
	H2A []Entry `json:"h2a"`
	// A2A (agent to agent) applies when a peer asks. Missing allows none.
	A2A []Entry `json:"a2a"`
	// Peers are lists of tools of single peers, by the ID of the node of
	// each: the list of a peer here applies to it in place of A2A. Only such
	// a list gives a peer a trusted tool, like bash or rebuild.
	Peers map[string][]Entry `json:"peers,omitempty"`
	// Protected are paths no tool for files touches, for anybody. Missing (a
	// configuration from before there were such tools) stands for
	// DefaultProtected; an empty list is the owner's decision to protect
	// nothing.
	Protected *[]string `json:"protected,omitempty"`
	// Ask are the calls of tools the agent of the owner makes only once the
	// owner allowed them, see Rule. Missing stands for DefaultAsk; an empty
	// list is the owner's decision to be asked nothing.
	Ask *[]Rule `json:"ask,omitempty"`
	// Allowed are the calls the owner allowed for good when asked: they run
	// without asking again.
	Allowed []Call `json:"allowed,omitempty"`
}

// Rule says which calls of tools are asked about: those of the tool Tool,
// of every tool if it is "", whose text matches the regular expression Match.
// The text of a call of bash is its command, that of any other tool its
// arguments as JSON on one line.
type Rule struct {
	Tool  string `json:"tool,omitempty"`
	Match string `json:"match"`
}

// Call is one call of a tool: the tool and the text of the call, as a Rule
// sees it.
type Call struct {
	Tool string `json:"tool"`
	Call string `json:"call"`
}

// Entry is one tool in a list of Tools: its name alone, or an object with
// the name as "tool" and settings for that tool next to it.
//
//	"read"
//	{"tool": "read", "allow": ["~/share"]}
type Entry struct {
	Tool string // the name of the tool, or AllTools
	// Settings are the settings of the tool as a JSON object, nil for an
	// entry that is a name alone. What they mean is for the tool to say.
	Settings json.RawMessage
}

// Named returns the entries for tools that are listed by their name alone.
func Named(names ...string) []Entry {
	entries := make([]Entry, len(names))
	for i, name := range names {
		entries[i] = Entry{Tool: name}
	}
	return entries
}

// entryTool is the key that names the tool in an entry with settings.
const entryTool = "tool"

// UnmarshalJSON reads a name or an object with settings.
func (e *Entry) UnmarshalJSON(b []byte) error {
	if json.Unmarshal(b, &e.Tool) == nil {
		e.Settings = nil
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(b, &object); err != nil {
		return errors.New(`a tool is a name or an object like {"tool": name, …}`)
	}
	if json.Unmarshal(object[entryTool], &e.Tool) != nil || e.Tool == "" {
		return errors.New(`a tool with settings needs its name as "tool"`)
	}
	delete(object, entryTool)
	e.Settings, _ = json.Marshal(object) // values that were just read: cannot fail
	return nil
}

// MarshalJSON writes the entry the way it is read.
func (e Entry) MarshalJSON() ([]byte, error) {
	if e.Settings == nil {
		return json.Marshal(e.Tool)
	}
	object := map[string]any{}
	if err := json.Unmarshal(e.Settings, &object); err != nil {
		return nil, fmt.Errorf("settings of the tool %s: %w", e.Tool, err)
	}
	object[entryTool] = e.Tool
	return json.Marshal(object)
}

// DefaultProtected are the paths no tool for files touches unless the owner
// says otherwise: the usual places of keys and credentials, and nothing
// else. "~" is the home directory of the user.
func DefaultProtected() []string {
	return []string{
		"~/.ssh", "~/.gnupg", "~/.aws", "~/.kube", "~/.config/gcloud",
		"~/.docker/config.json", "~/.netrc", "~/.password-store", "~/.local/share/keyrings",
	}
}

// DefaultAsk are the calls the agent of the owner asks about unless the owner
// says otherwise: what deletes for good, what reaches beyond the machine or
// changes the system, what reads keys, ouro building itself anew, and typing
// into the pages of the browser, where forms are sent and people sign in.
func DefaultAsk() []Rule {
	return []Rule{
		{Tool: "bash", Match: `\brm\s+-\w*[rf]`},
		{Tool: "bash", Match: `\bgit\s+(push|reset\s+--hard|clean|branch\s+-D)`},
		{Tool: "bash", Match: `\bsudo\b|\b(pacman|yay|apt|apt-get|brew|pip|pip3)\s+(install|remove|uninstall|-S|-R)|\bnpm\s+(install|i)\s+-g`},
		{Tool: "bash", Match: `\b(systemctl|crontab|dd|mkfs)\b`},
		{Tool: "bash", Match: `~/\.(ssh|gnupg|aws)|\.env\b`},
		{Tool: "delete", Match: `.*`},
		{Tool: "rebuild", Match: `.*`},
		{Tool: "browser", Match: `"action":"type"`},
	}
}

// DefaultTools is what the setup writes: every tool for the owner, none for
// peers, and the paths that are protected and the calls that are asked
// about, for the owner to see and change.
func DefaultTools() *Tools {
	protected, ask := DefaultProtected(), DefaultAsk()
	return &Tools{H2A: Named(AllTools), A2A: []Entry{}, Protected: &protected, Ask: &ask}
}

// OwnerTools are the tools the agent may use for its owner.
func (c Config) OwnerTools() []Entry {
	if c.Tools == nil || c.Tools.H2A == nil {
		return Named(AllTools)
	}
	return c.Tools.H2A
}

// PeerTools are the tools the agent may use for a peer.
func (c Config) PeerTools() []Entry {
	if c.Tools == nil {
		return nil
	}
	return c.Tools.A2A
}

// ToolsOf are the tools the agent may use for the peer whose node has the
// ID id, and whether they are the list of that peer alone, in Tools.Peers.
func (c Config) ToolsOf(id string) ([]Entry, bool) {
	if c.Tools != nil {
		if entries, ok := c.Tools.Peers[id]; ok {
			return entries, true
		}
	}
	return c.PeerTools(), false
}

// Protected are the paths no tool for files touches.
func (c Config) Protected() []string {
	if c.Tools == nil || c.Tools.Protected == nil {
		return DefaultProtected()
	}
	return *c.Tools.Protected
}

// AskRules are the calls the agent of the owner asks about, see Tools.Ask.
func (c Config) AskRules() []Rule {
	if c.Tools == nil || c.Tools.Ask == nil {
		return DefaultAsk()
	}
	return *c.Tools.Ask
}

// AllowedCalls are the calls the owner allowed for good, see Tools.Allowed.
func (c Config) AllowedCalls() []Call {
	if c.Tools == nil {
		return nil
	}
	return c.Tools.Allowed
}

// Allow adds call to the calls the owner allowed for good, if it is not
// among them yet.
func (c *Config) Allow(call Call) {
	if slices.Contains(c.AllowedCalls(), call) {
		return
	}
	tools := Tools{}
	if c.Tools != nil {
		tools = *c.Tools // the Tools of the caller's copy are not changed
	}
	tools.Allowed = append(slices.Clone(tools.Allowed), call)
	c.Tools = &tools
}

// MaxDescription is how many characters the description of a node has at
// most: as many as a peer shows of it.
const MaxDescription = 300

// About is what the node says about itself in its agent card: its
// description, or the default that holds without one.
func (c Config) About() string {
	if c.Description == "" {
		return defaultAbout(c.Name)
	}
	return c.Description
}

// defaultAbout is the description of a node named name that has none.
func defaultAbout(name string) string { return "Ouroboros instance named " + name }

// ValidDescription reports whether s can be the description of a node. It is
// public, anyone reads it in the agent card, and peers show it in a line: it
// is one line of at most MaxDescription characters.
func ValidDescription(s string) error {
	if n := utf8.RuneCountInString(s); n > MaxDescription {
		return fmt.Errorf("description has %d characters, at most %d are kept", n, MaxDescription)
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return errors.New("description is one line, without tabs or other control characters")
	}
	return nil
}

// MaxName is how many characters the name of a node has at most.
const MaxName = 64

// ValidName reports whether s can be the name of a node or of a model. A
// name goes to peers, which show it in a line: it is one line of printable
// text, of at most MaxName characters for a node.
func ValidName(field, s string) error {
	switch {
	case strings.TrimSpace(s) == "":
		return fmt.Errorf("%s cannot be empty", field)
	case printable.Line(s, len(s)) != s:
		return fmt.Errorf("%s is one line, without tabs or other control characters", field)
	case field == FieldName && utf8.RuneCountInString(s) > MaxName:
		return fmt.Errorf("%s has %d characters, at most %d are allowed", field, utf8.RuneCountInString(s), MaxName)
	}
	return nil
}

// Files are the files in dir that hold the configuration, with the key of
// the node, and the file that is locked while it is changed.
func Files(dir string) []string {
	return []string{filepath.Join(dir, fileName), filepath.Join(dir, lockName)}
}

// ID is the ID of the node: the fingerprint of its public key. A node
// without a valid key has none yet.
func (c Config) ID() string {
	id, err := identity.FromPrivate(c.PrivateKey)
	if err != nil {
		return ""
	}
	return id.ID()
}

// MemoryArchive is how long the archivist waits after a pass, and whether it
// runs at all: "" or off turns it off, see MemoryInterval, and so does a
// configuration that says nothing of it: the archivist waits until an
// interval is set.
func (c Config) MemoryArchive() (time.Duration, bool) {
	if c.MemoryInterval == nil {
		return 0, false
	}
	v := strings.TrimSpace(*c.MemoryInterval)
	if v == "" || v == "off" {
		return 0, false
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// ValidMemoryInterval reports whether value can be memory_interval: off, ""
// or a positive duration.
func ValidMemoryInterval(value string) error {
	v := strings.TrimSpace(value)
	if v == "" || v == "off" {
		return nil
	}
	if d, err := time.ParseDuration(v); err != nil || d <= 0 {
		return fmt.Errorf("memory_interval is a duration like %q, or off", "24h")
	}
	return nil
}

// Efforts are the efforts the owner chooses from where the model is not
// known, from the least to the most: "" leaves it to the model, "none" has
// it not think at all.
var Efforts = []string{"", "none", "low", "medium", "high", "max"}

// AllEfforts are all efforts there are, from the least to the most: those a
// model may know besides Efforts too, as its API names them.
var AllEfforts = []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max"}

// ErrIncomplete means that the node was not set up yet.
var ErrIncomplete = errors.New("node is not configured")

// Fallback is an access of the node and a model of it that the agent answers
// the owner with when the one before it cannot for now: its limit or quota
// is reached, its service fails or is not reached. The owner lists them; no
// access is switched to that is not listed.
type Fallback struct {
	Access string `json:"access"`
	Model  string `json:"model"`
}

// PeerLLM returns the access and the model the agent answers peers with:
// those the owner chose for them, which own reports, or else those it
// answers the owner with.
func (c Config) PeerLLM() (access, model string, own bool) {
	return c.llm(c.PeerAccess, c.PeerModel)
}

// SessionLLM returns the access and the model the agent answers the sessions
// of the owner with: those the owner chose for them, which own reports, or
// else those it answers the owner with.
func (c Config) SessionLLM() (access, model string, own bool) {
	return c.llm(c.SessionAccess, c.SessionModel)
}

// ThreadLLM returns the access and the model the agent answers the thread
// with: those the owner chose for it, which own reports, or else those it
// answers the owner with.
func (c Config) ThreadLLM() (access, model string, own bool) {
	return c.llm(c.ThreadAccess, c.ThreadModel)
}

// llm is the access and the model of a scope that has its own, or else those
// the owner is answered with.
func (c Config) llm(access, model string) (string, string, bool) {
	if access != "" && model != "" {
		return access, model, true
	}
	return c.Access, c.Model, false
}

// Complete reports whether the node can be started with this configuration.
// The key is not needed for that: a node without one generates it.
func (c Config) Complete() bool {
	return (c.Access != "" || c.BaseURL != "") && c.Model != "" && c.Name != ""
}

// Load reads the configuration from dir. A missing file yields an empty
// Config. A file that is no configuration is named in the error, with the
// line of a mistake in its JSON.
func Load(dir string) (Config, error) {
	var c Config
	path := filepath.Join(dir, fileName)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			path += fmt.Sprintf(", line %d", 1+bytes.Count(b[:syntax.Offset], []byte("\n")))
		}
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if !slices.Contains(AllEfforts, c.Effort) {
		return c, fmt.Errorf("%s: effort %q is none of %s", path, c.Effort, strings.Join(AllEfforts[1:], ", "))
	}
	if c.MemoryInterval != nil {
		if err := ValidMemoryInterval(*c.MemoryInterval); err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
	}
	return c, nil
}

// Save writes the configuration to dir, readable only by the owner. The file
// is replaced in one step: a crash leaves the old configuration or the new,
// never a part of one.
func Save(dir string, c Config) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err == nil {
		err = safefile.Write(filepath.Join(dir, fileName), b, 0o600)
	}
	return err
}

// Update loads the configuration from dir, lets change alter it and saves
// the result, all under a lock that keeps other processes from changing it in
// between. Nothing is written if change fails or leaves it as it was.
func Update(dir string, change func(*Config) error) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	unlock, err := safefile.Lock(filepath.Join(dir, lockName))
	if err != nil {
		return err
	}
	defer unlock()
	old, err := Load(dir)
	if err != nil {
		return err
	}
	c := old
	if err := change(&c); err != nil || reflect.DeepEqual(c, old) {
		return err
	}
	return Save(dir, c)
}

// The fields of the configuration by the names a proposal of the agent uses
// for them, see Field.
const (
	FieldName        = "name"
	FieldDescription = "description"
	FieldModel       = "model"
	FieldOwner       = "tools.h2a"
	FieldPeers       = "tools.a2a"
	FieldProtected   = "tools.protected"
)

// Fields are the fields the agent of the owner may propose a change of.
func Fields() []string {
	return []string{FieldName, FieldDescription, FieldModel, FieldOwner, FieldPeers, FieldProtected}
}

// reserved are the fields no proposal changes: who the node is, and where the
// conversations of the owner go. A proposal a peer slipped in must not send
// them to another server.
var reserved = []string{"private_key", "access", "api_key", "base_url"}

// field returns the value of the field path as it applies: one that is not
// set has the default that holds for it.
func (c Config) field(path string) (any, error) {
	switch path {
	case FieldName:
		return c.Name, nil
	case FieldDescription:
		return c.About(), nil
	case FieldModel:
		return c.Model, nil
	case FieldOwner:
		return c.OwnerTools(), nil
	case FieldPeers:
		return append([]Entry{}, c.PeerTools()...), nil // none is an empty list, not null
	case FieldProtected:
		return c.Protected(), nil
	}
	if slices.Contains(reserved, path) {
		return nil, fmt.Errorf("%s is changed by the owner alone, with ouro start --setup: no proposal changes it", path)
	}
	return nil, fmt.Errorf("the configuration has no field %q that can be proposed: there are %s", path, strings.Join(Fields(), ", "))
}

// Field returns the value of the field path as JSON, as it applies: a field
// that is not set has the default that holds for it. Two values are the same
// if what Field returns for them is.
func (c Config) Field(path string) (json.RawMessage, error) {
	value, err := c.field(path)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// SetField sets the field path to value, which is JSON. A value of the wrong
// type is an error and changes nothing. An empty description, or one that is
// the default, leaves the default to hold. Whether the tools a list names exist
// is not for the configuration to know.
func (c *Config) SetField(path string, value json.RawMessage) error {
	if _, err := c.field(path); err != nil {
		return err
	}
	var (
		text  string
		tools []Entry
		paths []string
	)
	into := map[string]any{FieldName: &text, FieldDescription: &text, FieldModel: &text, FieldOwner: &tools, FieldPeers: &tools, FieldProtected: &paths}[path]
	if err := json.Unmarshal(value, into); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if path == FieldDescription {
		if err := ValidDescription(text); err != nil {
			return err
		}
	} else if into == &text {
		if err := ValidName(path, text); err != nil {
			return err
		}
	}
	// Null is no list: the default it would stand for is written out instead.
	if (into == &tools && tools == nil) || (into == &paths && paths == nil) {
		return fmt.Errorf("%s has to be a list", path)
	}
	listed := into == &tools || into == &paths
	if c.Tools == nil && listed {
		c.Tools = &Tools{}
	} else if c.Tools != nil {
		copied := *c.Tools // the Tools of the caller's copy are not changed
		c.Tools = &copied
	}
	switch path {
	case FieldName:
		c.Name = text
	case FieldDescription:
		c.Description = text
		if text == defaultAbout(c.Name) {
			c.Description = ""
		}
	case FieldModel:
		c.Model = text
	case FieldOwner:
		c.Tools.H2A = tools
	case FieldPeers:
		c.Tools.A2A = tools
	case FieldProtected:
		c.Tools.Protected = &paths
	}
	return nil
}

// Public is the configuration as the agent of the owner gets to see it:
// without the key of the node and without its accesses, and with the defaults
// that hold for what is not set. provider is what the access in use is
// called, as the node tells it.
func (c Config) Public(provider string) json.RawMessage {
	protected, ask := c.Protected(), c.AskRules()
	tools := Tools{H2A: c.OwnerTools(), A2A: append([]Entry{}, c.PeerTools()...), Protected: &protected, Ask: &ask, Allowed: c.AllowedCalls()}
	if c.Tools != nil {
		tools.Peers = c.Tools.Peers
	}
	b, _ := json.MarshalIndent(struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Model       string `json:"model"`
		Provider    string `json:"provider"`
		Tools       Tools  `json:"tools"`
		SelfImprove string `json:"self_improve"`
	}{c.ID(), c.Name, c.About(), c.Model, provider, tools,
		cmp.Or(c.SelfImprove, SelfImproveBuild)}, "", "  ")
	return b
}
