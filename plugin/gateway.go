package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// GatewayIdentity is a transport-authenticated external account. Gateway is the
// account namespace (for example, a bot installation), not a display name.
// The transport must construct both fields from authenticated metadata, never
// from message text or model output. Group conversations need their own trusted
// identity mapping; this adapter does not authenticate transport requests.
type GatewayIdentity struct {
	Gateway string
	Subject string
}

// GatewayOperation is the operation being authorized, on every request.
type GatewayOperation string

const (
	GatewayOpen   GatewayOperation = "open"
	GatewaySend   GatewayOperation = "send"
	GatewayPoll   GatewayOperation = "poll"
	GatewayCancel GatewayOperation = "cancel"
	GatewayReply  GatewayOperation = "reply"
)

// GatewayPermission is an explicit grant from trusted host policy, not a field
// an external request may supply. Its zero value denies access.
type GatewayPermission struct {
	// OwnerSession permits the requested operation on an owner session. It
	// must only be granted to an identity trusted to act as the node's owner.
	// In particular, sending can run owner tools and replying can approve a
	// permission question (including an approval kept for good). Allowlisting
	// an account deliberately grants the profile's existing agent tool authority,
	// not a restricted external role. Hosts must warn owners explicitly in their
	// allowlist UI; this grant does not expose an Owner administration API.
	OwnerSession bool
}

// GatewayAuthorize checks current host policy for an authenticated identity and
// operation. Returning nil error alone grants nothing: OwnerSession must also
// be true. The callback must be safe for concurrent calls. It may implement
// revocation or separate read, send, cancel and question-reply permissions.
type GatewayAuthorize func(context.Context, GatewayIdentity, GatewayOperation) (GatewayPermission, error)

// GatewaySessions is the part of Sessions needed by a gateway. Both Sessions
// implementations satisfy it. The adapter neither starts nor stops the node.
type GatewaySessions interface {
	New(context.Context) (string, error)
	Ask(context.Context, string, string) error
	Steer(context.Context, string, string) error
	Watch(context.Context, string) ([]SessionEvent, <-chan SessionEvent, error)
	Cancel(context.Context, string) error
}

var (
	ErrGatewayDenied  = errors.New("gateway access denied")
	ErrGatewaySession = errors.New("no such gateway session")
)

// GatewayAdapter adapts authenticated external gateways to existing owner Sessions.
// There is deliberately no implicit owner identity, default allow policy, raw
// session attachment, global session listing, or Owner administration API.
// Sessions does not carry an external security principal into agent execution:
// WithMessageSource is attribution, NOT a permission boundary. Consequently
// identities without an explicit OwnerSession grant are refused. An allowlist
// grant confers the profile's existing agent tool authority, not a sandbox. A
// restricted non-owner execution mode needs a different backend contract.
//
// Only sessions opened through this adapter (or restored by the trusted host)
// can be used, by the exact identity that opened them. Bindings are in memory
// unless the host explicitly saves Bindings and restores them with
// NewGatewayAdapterWithBindings. The host owns transport authentication, rate
// limits, protected persistence and node lifecycle.
type GatewayAdapter struct {
	sessions  GatewaySessions
	authorize GatewayAuthorize
	mu        sync.Mutex
	bindings  map[string]GatewayIdentity
}

// NewGatewayAdapter constructs a fail-closed adapter. A nil backend or policy denies
// all requests. No session or node is opened by construction.
func NewGatewayAdapter(s GatewaySessions, authorize GatewayAuthorize) *GatewayAdapter {
	return &GatewayAdapter{sessions: s, authorize: authorize, bindings: make(map[string]GatewayIdentity)}
}

// NewGatewayAdapterWithBindings restores a snapshot from the trusted host's
// protected profile store. This is a host-only capability, not a transport API:
// bindings must never come from external requests, message text, or model output.
// Restoring a binding attaches an existing owner session, so the host must keep
// snapshots isolated by profile and protect their integrity as well as privacy.
// It validates and copies the entire snapshot without calling the backend or
// policy. A binding is not a grant: every operation still checks current policy.
func NewGatewayAdapterWithBindings(s GatewaySessions, authorize GatewayAuthorize, bindings map[string]GatewayIdentity) (*GatewayAdapter, error) {
	g := NewGatewayAdapter(s, authorize)
	for id, who := range bindings {
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("invalid restored binding: %w", ErrGatewaySession)
		}
		if strings.TrimSpace(who.Gateway) == "" || strings.TrimSpace(who.Subject) == "" {
			return nil, fmt.Errorf("invalid restored identity: %w", ErrGatewayDenied)
		}
		g.bindings[id] = who
	}
	return g, nil
}

// Bindings returns an independent snapshot for the trusted host to persist in
// its protected profile store. Never expose it to transports. Open does not
// persist automatically: hosts needing restart durability must save after Open
// succeeds and before publishing the session ID. Snapshots include no policy
// grants, and restoring them never bypasses authorization.
func (g *GatewayAdapter) Bindings() map[string]GatewayIdentity {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]GatewayIdentity, len(g.bindings))
	for id, who := range g.bindings {
		out[id] = who
	}
	return out
}

func (g *GatewayAdapter) permit(ctx context.Context, who GatewayIdentity, op GatewayOperation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if g == nil || g.sessions == nil || g.authorize == nil ||
		strings.TrimSpace(who.Gateway) == "" || strings.TrimSpace(who.Subject) == "" {
		return ErrGatewayDenied
	}
	grant, err := g.authorize(ctx, who, op)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrGatewayDenied, err)
	}
	if !grant.OwnerSession {
		return ErrGatewayDenied
	}
	return ctx.Err()
}

func (g *GatewayAdapter) bound(ctx context.Context, who GatewayIdentity, op GatewayOperation, id string) error {
	if err := g.permit(ctx, who, op); err != nil {
		return err
	}
	g.mu.Lock()
	owner, ok := g.bindings[id]
	g.mu.Unlock()
	if !ok || owner != who {
		return ErrGatewaySession
	}
	return nil
}

// Open creates a fresh session and binds it to who. It cannot resume or attach
// an arbitrary owner session, even for an otherwise authorized identity.
func (g *GatewayAdapter) Open(ctx context.Context, who GatewayIdentity) (string, error) {
	if err := g.permit(ctx, who, GatewayOpen); err != nil {
		return "", err
	}
	id, err := g.sessions.New(ctx)
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	// A backend must return fresh IDs. Do not transfer an existing binding
	// if a faulty backend returns the same ID twice.
	if id == "" {
		return "", ErrGatewaySession
	}
	if _, exists := g.bindings[id]; exists {
		return "", ErrGatewaySession
	}
	g.bindings[id] = who
	return id, nil
}

// Send starts an answer and returns at once. It preserves ErrBusy rather than
// steering: ordinary send permission must not also answer permission questions.
// Source remains api: the backend's fixed source vocabulary does not accept
// gateway. Attribution is not authorization and must not encode identity.
func (g *GatewayAdapter) Send(ctx context.Context, who GatewayIdentity, id, text string) error {
	if err := g.bound(ctx, who, GatewaySend, id); err != nil {
		return err
	}
	return g.sessions.Ask(WithMessageSource(ctx, MessageSource{Interface: "api"}), id, text)
}

// Poll returns a complete snapshot, including SessionAsked events. An Asked
// event with Done false describes a question; a later Done event closes it.
// Poll does not retain a watcher or promise transient streaming fragments. It
// has no numeric cursor: Sessions snapshots also contain mutable metadata.
func (g *GatewayAdapter) Poll(ctx context.Context, who GatewayIdentity, id string) ([]SessionEvent, error) {
	if err := g.bound(ctx, who, GatewayPoll, id); err != nil {
		return nil, err
	}
	watch, cancel := context.WithCancel(ctx)
	defer cancel()
	past, _, err := g.sessions.Watch(watch, id)
	if err != nil {
		return nil, err
	}
	// Sessions clones the event slice, but not the question/tool argument
	// bytes. Do not expose those mutable backend buffers to transports.
	out := append([]SessionEvent(nil), past...)
	for i := range out {
		out[i].Args = append([]byte(nil), out[i].Args...)
	}
	return out, nil
}

// Reply answers a pending question with text unchanged, including exact option
// values. Like Sessions.Steer, if the question ended concurrently it queues the
// text, or starts an answer when idle. This is not an atomic question-ID reply;
// transports needing that guarantee must not expose it without backend support.
// Reply has its own authorization operation because it can approve owner tools.
func (g *GatewayAdapter) Reply(ctx context.Context, who GatewayIdentity, id, text string) error {
	if err := g.bound(ctx, who, GatewayReply, id); err != nil {
		return err
	}
	return g.sessions.Steer(WithMessageSource(ctx, MessageSource{Interface: "api"}), id, text)
}

// Cancel stops the answer and its delegated tasks, without deleting the session.
func (g *GatewayAdapter) Cancel(ctx context.Context, who GatewayIdentity, id string) error {
	if err := g.bound(ctx, who, GatewayCancel, id); err != nil {
		return err
	}
	return g.sessions.Cancel(ctx, id)
}
