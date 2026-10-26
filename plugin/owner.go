package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/config"
	"ouroboros/internal/contacts"
	"ouroboros/internal/home"
	"ouroboros/internal/llm"
	"ouroboros/internal/memory"
	"ouroboros/internal/modelsdev"
	"ouroboros/internal/node"
	"ouroboros/internal/peers"
	"ouroboros/internal/proposal"
	"ouroboros/internal/safefile"
	"ouroboros/internal/setup"
	"ouroboros/internal/suggestion"
)

// Owner is what the owner of the node does at a front end and nowhere else:
// start and stop the node, set it up, and decide whom it trusts and which
// change of the configuration its agent proposed is made.
//
// These are operations of the frame of ouro, so Owner is no service. A plugin
// cannot declare it as a need: the kernel refuses that like it refuses the
// contacts themselves, and a plugin that an agent wrote can therefore never
// accept a connection request on its own. A front end takes the Owner as an
// argument of the function that returns the plugin, and gets it where the
// plugins are listed (Plugins in internal/cli): whoever adds a front end to
// that list is the one who lets it act for the owner.
//
// It is narrow on purpose. It decides on the requests and proposals that are
// waiting and shows them; the contacts, the key of the node and its
// configuration are not to be had from it, and no change of the
// configuration but the model the owner picks in the model screen (/model),
// one that was proposed, one the owner makes in the setup, or one the owner
// makes in the settings (/settings, see Settings and Change), each checked
// before it is written. The settings show no key.
type Owner struct {
	home    home.Dir
	local   node.Local
	net     *peers.Network
	plugins catalog // of the build, see Activate
	models  *modelsdev.Catalog
}

// OwnerOf returns what the owner of the node in dir does, on its network. It
// is for the list of plugins.
func OwnerOf(dir home.Dir, network *peers.Network) *Owner {
	return &Owner{home: dir, local: node.Local{Home: dir}, net: network, models: modelsdev.Open(dir.Catalog(), dir.Log)}
}

type (
	// NodeStatus describes the node of this host.
	NodeStatus = node.Status
	// Request is a connection request that waits for a decision.
	Request = contacts.Request
	// Setup is one run of the setup of the node, see Owner.Setup.
	Setup = setup.Session
	// SetupModels are the models an access offers, as Setup.Login lists them.
	SetupModels = setup.Models
	// SetupProvider is a provider to sign in with, and SetupAccess an access
	// of the node as the owner sees it.
	SetupProvider = setup.Provider
	SetupAccess   = setup.Access
	// AccessModels are the models of one access, as Owner.Models lists them.
	AccessModels = setup.Listed
)

var (
	// ErrNotSetUp means the node was not set up yet: Owner.SetupCommand is
	// what does it.
	ErrNotSetUp = config.ErrIncomplete
	// ErrNotSaved means a decision on a connection request could not be
	// stored. Nothing was decided: the request is still waiting.
	ErrNotSaved = peers.ErrNotSaved
	// ErrNoRequest means there is no such connection request to decide on.
	ErrNoRequest = peers.ErrNoRequest
	// ErrStale means a proposal was not applied, and dropped, because the
	// configuration is no longer the one the change was proposed for.
	ErrStale = proposal.ErrStale
	// ErrNoProposal means there is no such proposal to decide on.
	ErrNoProposal = proposal.ErrGone
	// ErrNoSuggestion means there is no such suggestion to take or drop.
	ErrNoSuggestion = suggestion.ErrGone
	// ErrLastAccess means the only access of the node was to be removed.
	ErrLastAccess = setup.ErrLastAccess
)

// Proposal is a change of the configuration that the agent of the owner
// proposed and that waits for the owner to decide. Now and Then are JSON and
// come from the node: Now is read from the configuration as it is, Then is
// the value the node checked and would write. Only Reason is the text of the
// agent.
type Proposal struct {
	ID     string
	Field  string // which field of the configuration, like "tools.a2a"
	Now    string // what the field is
	Then   string // what it would be
	Reason string // why, as the agent says
	At     time.Time
	// Stale says that the field was changed since the proposal was made.
	// Such a proposal is not applied: accepting it drops it.
	Stale bool
}

// AllTools in the tools Setup.Rights names stands for every tool.
const AllTools = config.AllTools

// Proposals returns the changes of the configuration that wait for a
// decision, the oldest first.
func (o *Owner) Proposals() ([]Proposal, error) {
	waiting, err := o.home.Proposals().List()
	if err != nil {
		return nil, err
	}
	cfg, err := o.home.Config()
	if err != nil {
		return nil, err
	}
	list := make([]Proposal, len(waiting))
	for i, p := range waiting {
		now, _ := cfg.Field(p.Path) // a field that is none any more shows as empty, and stale
		list[i] = Proposal{ID: p.ID, Field: p.Path, Now: string(now), Then: string(p.Value), Reason: p.Reason, At: p.At,
			Stale: !bytes.Equal(now, p.Before)}
	}
	return list, nil
}

// AcceptProposal makes the change the proposal id asks for and drops the
// proposal. It fails with ErrStale, and nothing changed, for a proposal that
// was made for a configuration that is no longer the one of the node. If the
// configuration cannot be written the proposal still waits.
//
// The agents of the node are built from the changed configuration with the
// next message they get: no restart is needed.
func (o *Owner) AcceptProposal(id string) error {
	// A name is that of a profile too, which no other profile has.
	list, err := o.home.Proposals().List()
	if err != nil {
		return err
	}
	for _, p := range list {
		var name string
		if p.ID == id && p.Path == config.FieldName && json.Unmarshal(p.Value, &name) == nil {
			if err := o.home.NameFree(strings.TrimSpace(name)); err != nil {
				return err
			}
		}
	}
	p, err := o.home.Proposals().Accept(string(o.home), id)
	if err == nil {
		o.home.Log("the owner accepted a proposal: %s is now %s", p.Path, p.Value)
	}
	return err
}

// RefuseProposal drops the proposal id. Nothing is changed.
func (o *Owner) RefuseProposal(id string) error {
	_, err := o.home.Proposals().Refuse(id)
	return err
}

// Suggestion is a change of ouro the review of an answer suggested, which
// waits for the owner to take or drop it. Text is the text of an LLM;
// Signals are what the node measured of the answer.
type Suggestion = suggestion.Suggestion

// SuggestedTask is what a front end asks the agent of the owner to make the
// suggestion s the owner took: in a session of its own, as the answer to it
// is not reviewed.
func SuggestedTask(s Suggestion) string { return assistant.SuggestedTask(s.Text, s.Signals) }

// Suggestions returns the changes of ouro the reviews of answers suggested
// that wait for the owner, the oldest first.
func (o *Owner) Suggestions() ([]Suggestion, error) { return o.home.Suggestions().List() }

// TakeSuggestion drops the suggestion id from those that wait and returns
// it, for the owner to have the agent make the change: taking it changes
// nothing by itself.
func (o *Owner) TakeSuggestion(id string) (Suggestion, error) {
	s, err := o.home.Suggestions().Remove(id)
	if err == nil {
		o.home.Log("the owner took a suggestion: %q", s.Text)
	}
	return s, err
}

// DropSuggestion drops the suggestion id. Nothing is changed.
func (o *Owner) DropSuggestion(id string) error {
	s, err := o.home.Suggestions().Remove(id)
	if err == nil {
		o.home.Log("the owner dropped a suggestion: %q", s.Text)
	}
	return err
}

// Remembered is a memory the review of an answer kept by itself, as the
// Args of a SessionRemembered tell it.
type Remembered = assistant.Remembered

// UndoRemembered takes back the memory the review of an answer kept, which
// args of a SessionRemembered tell: one it created is forgotten, one it
// replaced is as it was before. It fails with ErrMemoryChanged if the memory
// was changed since.
func (o *Owner) UndoRemembered(args json.RawMessage) error {
	var r Remembered
	if err := json.Unmarshal(args, &r); err != nil {
		return fmt.Errorf("no memory to take back: %w", err)
	}
	store, err := memory.Open(o.home.History())
	if err != nil {
		return err
	}
	defer store.Close()
	if err = store.Undo(context.Background(), r.Memory, r.Was); err == nil {
		o.home.Log("the owner took back a memory: %q", r.Memory.Key)
	}
	return err
}

// ErrMemoryChanged means that a memory was changed or forgotten since what
// is to be taken back of it.
var ErrMemoryChanged = memory.ErrConflict

// Status reports how the node is configured and whether it is running.
func (o *Owner) Status() (NodeStatus, error) { return o.local.Status() }

// Appearances are how the terminal UI can look: as the background of the
// terminal is, or light or dark whatever it is.
var Appearances = []string{"system", "light", "dark"}

// Appearance is how the terminal UI looks, one of Appearances: "system"
// until another was chosen.
func (o *Owner) Appearance() string {
	data, _ := os.ReadFile(o.home.Appearance())
	if a := strings.TrimSpace(string(data)); slices.Contains(Appearances, a) {
		return a
	}
	return Appearances[0]
}

// SetAppearance keeps how the terminal UI looks, one of Appearances, for
// all of its profiles and the next time it opens.
func (o *Owner) SetAppearance(a string) error {
	if !slices.Contains(Appearances, a) {
		return fmt.Errorf("no appearance %q: %s", a, strings.Join(Appearances, ", "))
	}
	return safefile.Write(o.home.Appearance(), []byte(a+"\n"), 0o600)
}

// SetDebug turns the debug mode of the node on or off: in it, the log tells
// much more of every call of the LLM. It holds from the next call on.
func (o *Owner) SetDebug(on bool) error {
	if err := o.home.SetDebug(on); err != nil {
		return err
	}
	if on {
		o.home.Log("debug mode on")
	} else {
		o.home.Log("debug mode off")
	}
	return nil
}

// Running reports whether the node is running.
func (o *Owner) Running() bool {
	_, running := o.local.Running()
	return running
}

// Start runs the node in the background unless it is running already. A node
// that was not set up yet fails with ErrNotSetUp.
func (o *Owner) Start() error {
	_, err := o.local.Start()
	return err
}

// Stop stops the node, which may not have been running.
func (o *Owner) Stop() error {
	_, err := o.local.Stop()
	return err
}

// SetupCommand returns the interactive setup, to be run with the terminal
// handed over. It starts the node afterwards.
func (o *Owner) SetupCommand() (*exec.Cmd, error) {
	exe, err := o.local.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(exe, "start", "--setup"), nil
}

// Setup starts the setup of the node, for a front end that asks for it in a
// dialog of its own: SetupCommand is the one on the terminal. providers are
// the providers the plugins contributed. What the setup saves holds for the
// node once it is started again. What a provider signs in with stays in the
// Setup: it tells which accesses there are, never what they hold.
func (o *Owner) Setup(providers []Provider) (*Setup, error) {
	return setup.Open(o.home, func() []Provider { return providers })
}

// Models lists the models of every access of the node, to choose from with
// /model in a front end, for the providers the plugins contributed. An access
// that does not answer shows with why, and with the models it listed the last
// time. It fails with ErrNotSetUp for a node that is not set up.
func (o *Owner) Models(ctx context.Context, providers []Provider) ([]AccessModels, error) {
	s, err := o.Setup(providers)
	if err != nil {
		return nil, err
	}
	return s.Models(ctx)
}

// SetModel makes model of the access what the agents of the node answer
// with, from the next message they get: no restart is needed. It reports
// whether they answer through another access than before, whose provider
// has cached nothing of their conversations yet.
func (o *Owner) SetModel(access, model string) (switched bool, err error) {
	s, err := o.Setup(nil)
	if err != nil {
		return false, err
	}
	if switched, err = s.Pick(access, model); err == nil {
		o.home.Log("the owner picked the model %s of the access %s", model, access)
	}
	return switched, err
}

// Efforts are the efforts the owner chooses from with SetEffort, from the
// least to the most: "" leaves it to the model, "none" has it not think.
var Efforts = config.Efforts

// AllEfforts are all efforts there are, from the least to the most: those a
// model may know besides Efforts too, see Owner.Efforts.
var AllEfforts = config.AllEfforts

// SetEffort makes effort, one of Efforts, how much the model thinks before
// it answers the owner, from the next call of it on: see Effort for how it
// gets to the model.
func (o *Owner) SetEffort(effort string) error {
	s, err := o.Setup(nil)
	if err != nil {
		return err
	}
	if err = s.SetEffort(effort); err == nil {
		o.home.Log("the owner set the effort to %q", effort)
	}
	return err
}

// Efforts returns the efforts the model the node answers the owner with
// takes, as models.dev lists them, from the least to the most, and whether it
// lists the model: one it lists takes none of them when the list is empty.
// providers are those the plugins contributed. The list is fetched in the
// background once a day: until it is, nothing is known.
func (o *Owner) Efforts(providers []Provider) ([]string, bool) {
	s, err := o.Setup(providers)
	if err != nil {
		return nil, false
	}
	return s.Efforts(o.models)
}

// SetPeerModel makes model of the access what the agent of the node answers
// peers with, whatever it answers the owner with: from the next message of a
// peer on, without a restart. With the access "" peers are answered with what
// the owner is again, as they are until the owner chooses for them.
func (o *Owner) SetPeerModel(access, model string) error {
	s, err := o.Setup(nil)
	if err != nil {
		return err
	}
	if err = s.PickForPeers(access, model); err == nil {
		o.home.Log("the owner picked the model %q of the access %q for peers", model, access)
	}
	return err
}

// Fallback is an access of the node and a model of it that the agent goes on
// with when the one it answers the owner with cannot for now.
type Fallback = config.Fallback

// Fallbacks are the fallbacks of the node, in their order: what the agent
// answers the owner with when the access before cannot answer for now, as
// its limit is reached or its service is down.
func (o *Owner) Fallbacks() ([]Fallback, error) {
	cfg, err := o.home.Config()
	return cfg.Fallbacks, err
}

// ToggleFallback makes model of the access the last of the fallbacks of the
// node, or, if it is one of them, none any more, and returns them as they
// are then. It holds from the next message on.
func (o *Owner) ToggleFallback(access, model string) ([]Fallback, error) {
	s, err := o.Setup(nil)
	if err != nil {
		return nil, err
	}
	list, err := s.ToggleFallback(access, model)
	if err == nil {
		o.home.Log("the owner changed the fallbacks to %+v", list)
	}
	return list, err
}

// Browse shows url in the browser of the user, for a front end that is asked
// to by a provider that signs in. It fails where there is no browser to
// start: show the URL either way.
func Browse(url string) error { return llm.Browse(url) }

// Requests returns the connection requests that wait for a decision: those
// of other nodes and those the agent of this node proposed (Request.Proposed),
// which the owner decides on, and those this node sent.
func (o *Owner) Requests() (incoming, outgoing []Request, err error) {
	data, err := o.net.Contacts.Snapshot()
	return data.Incoming, data.Outgoing, err
}

// Accept trusts the peer with publicKey, which sent a connection request, and
// tells it so. It fails with ErrNotSaved if the decision could not be stored.
//
// A request the agent proposed is none of the peer: accepting it asks the
// peer to connect, and rejecting it drops it. This is the only way the node
// asks another one to connect.
func (o *Owner) Accept(ctx context.Context, publicKey string) error {
	return o.decided("accept", publicKey, o.net.Accept(ctx, publicKey))
}

// Reject refuses the connection request of the peer with publicKey and tells
// it so. Like Accept it fails with ErrNotSaved.
func (o *Owner) Reject(ctx context.Context, publicKey string) error {
	return o.decided("reject", publicKey, o.net.Reject(ctx, publicKey))
}

// decided hands on the outcome of a decision on the connection request of
// publicKey. Of one that could not be stored the front end only learns that
// it failed: the cause goes to the log of the node.
func (o *Owner) decided(action, publicKey string, err error) error {
	if errors.Is(err, ErrNotSaved) {
		o.home.Log("%s the connection request of %s: %v", action, publicKey, err)
		return ErrNotSaved
	}
	return err
}

// pasted is the directory of the workspace of a profile that keeps the
// pictures its owner pasted into the chat.
const pasted = "pasted"

// SavePicture keeps a picture the owner pasted into the chat, of the kind ext
// like "png", as a file and returns its path. It goes into the directory
// "pasted" of the workspace of the profile, where the agent works and its
// tools read: every profile has its own. The file is named after the time
// and stays until the owner or the agent removes it.
func (o *Owner) SavePicture(data []byte, ext string) (string, error) {
	workspace := o.home.WorkingDir()
	if workspace == "" {
		return "", ErrNotSetUp
	}
	dir := filepath.Join(workspace, pasted)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, time.Now().Format("2006-01-02-150405")+"-*."+ext)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.Write(data)
	return f.Name(), err
}
