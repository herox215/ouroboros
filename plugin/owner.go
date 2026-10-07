package plugin

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/contacts"
	"ouroboros/internal/home"
	"ouroboros/internal/node"
	"ouroboros/internal/peers"
	"ouroboros/internal/proposal"
	"ouroboros/internal/setup"
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
	offer   setup.Offer
	plugins catalog // of the build, see Activate
}

// OwnerOf returns what the owner of the node in dir does, on its network. The
// setup offers what offer says until the node has its own. It is for the
// list of plugins.
func OwnerOf(dir home.Dir, network *peers.Network, offer setup.Offer) *Owner {
	return &Owner{home: dir, local: node.Local{Home: dir}, net: network, offer: offer}
}

type (
	// NodeStatus describes the node of this host.
	NodeStatus = node.Status
	// Request is a connection request that waits for a decision.
	Request = contacts.Request
	// Setup is one run of the setup of the node, see Owner.Setup.
	Setup = setup.Session
	// SetupModels are the models an endpoint offers, as Setup.Connect lists them.
	SetupModels = setup.Models
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
	// ErrKeyRejected means the endpoint refused the API key the setup tried.
	ErrKeyRejected = setup.ErrKeyRejected
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

// Status reports how the node is configured and whether it is running.
func (o *Owner) Status() (NodeStatus, error) { return o.local.Status() }

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
// dialog of its own: SetupCommand is the one on the terminal. What the setup
// saves holds for the node once it is started again. The key for the
// endpoint stays in the Setup: it tells whether there is one, never which.
func (o *Owner) Setup() (*Setup, error) { return setup.Open(o.home, o.offer) }

// Models lists the models the provider of the node offers, to choose from
// with /model in a front end: they are listed at the endpoint of the node,
// with the key it has. It fails with ErrNotSetUp for a node that is not set
// up, and with ErrKeyRejected if the provider refuses the key.
func (o *Owner) Models(ctx context.Context) (SetupModels, error) {
	s, err := setup.Open(o.home, o.offer)
	if err != nil {
		return SetupModels{}, err
	}
	return s.Configured(ctx)
}

// SetModel saves model as the model of the node, with the endpoint and the
// key it has. The agents of the node answer with it from the next message
// they get: no restart is needed.
func (o *Owner) SetModel(model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return errors.New("the model cannot be empty")
	}
	return config.Update(string(o.home), func(c *config.Config) error {
		if !c.Complete() {
			return ErrNotSetUp
		}
		c.Model = model
		return nil
	})
}

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
