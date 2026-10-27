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
	// GatewayDirectory changes where the session works: its project directory,
	// the one its tools resolve relative paths against. Hosts may grant it
	// apart from the others, as acting for the owner in the conversation does
	// not have to mean moving it to another project.
	GatewayDirectory GatewayOperation = "directory"
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
	// SetDirectory sets the project directory of the session id, or resets it
	// to the profile workspace with "".
	SetDirectory(context.Context, string, string) error
}

var (
	ErrGatewayDenied      = errors.New("gateway access denied")
	ErrGatewaySession     = errors.New("no such gateway session")
	ErrGatewayAttachments = errors.New("gateway backend does not support attachments")
	ErrGatewayImages      = errors.New("gateway backend does not support image input")
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
// can be used, by the exact identity that opened them, and the thread, see
// Thread. Bindings are in memory
// unless the host explicitly saves Bindings and restores them with
// NewGatewayAdapterWithBindings. The host owns transport authentication, rate
// limits, protected persistence and node lifecycle.
type GatewayAdapter struct {
	sessions  GatewaySessions
	authorize GatewayAuthorize
	source    MessageSource
	mu        sync.Mutex
	bindings  map[string]GatewayIdentity
}

// GatewayOption configures host-controlled gateway attribution.
type GatewayOption func(*GatewayAdapter)

// WithGatewayName identifies the transport in model context and archived message
// sources. Use a stable public name (for example, matrix or whatsapp), not an
// account identifier or message text. Names accept ASCII letters, digits, dots,
// underscores and hyphens, up to 64 bytes. Invalid names are omitted.
// This is attribution only; it grants no permissions.
func WithGatewayName(name string) GatewayOption {
	return func(g *GatewayAdapter) {
		g.source = (MessageSource{Interface: "api", Gateway: name}).Normalized()
	}
}

// NewGatewayAdapter constructs a fail-closed adapter. A nil backend or policy denies
// all requests. No session or node is opened by construction.
func NewGatewayAdapter(s GatewaySessions, authorize GatewayAuthorize, options ...GatewayOption) *GatewayAdapter {
	g := &GatewayAdapter{sessions: s, authorize: authorize, source: MessageSource{Interface: "api"}, bindings: make(map[string]GatewayIdentity)}
	for _, option := range options {
		option(g)
	}
	return g
}

// NewGatewayAdapterWithBindings restores a snapshot from the trusted host's
// protected profile store. This is a host-only capability, not a transport API:
// bindings must never come from external requests, message text, or model output.
// Restoring a binding attaches an existing owner session, so the host must keep
// snapshots isolated by profile and protect their integrity as well as privacy.
// It validates and copies the entire snapshot without calling the backend or
// policy. A binding is not a grant: every operation still checks current policy.
func NewGatewayAdapterWithBindings(s GatewaySessions, authorize GatewayAuthorize, bindings map[string]GatewayIdentity, options ...GatewayOption) (*GatewayAdapter, error) {
	g := NewGatewayAdapter(s, authorize, options...)
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
	if id == ThreadID {
		return nil // every identity with the grant, see Thread
	}
	g.mu.Lock()
	owner, ok := g.bindings[id]
	g.mu.Unlock()
	if !ok || owner != who {
		return ErrGatewaySession
	}
	return nil
}

// Thread returns the ID of the thread, the one conversation of the owner, for
// who to talk in: the other operations take it as they take a session of
// Open. Unlike one of those it is no session of this adapter, nor of who
// alone: every identity granted OwnerSession uses the same thread, as the
// owner does in every other front end, and its snapshots hold all of it, also
// what was said in it elsewhere and what the sessions it hands messages to
// answered. That is no more than the grant gives already, as the agent of the
// owner reads its history. Send queues in the thread while its agent hands
// on, rather than failing with ErrBusy, and answers no question the thread
// asks: Reply does.
func (g *GatewayAdapter) Thread(ctx context.Context, who GatewayIdentity) (string, error) {
	if err := g.permit(ctx, who, GatewayOpen); err != nil {
		return "", err
	}
	return ThreadID, nil
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
// Source identifies the API and the host-configured gateway name, never the
// authenticated account identity. Caller source metadata is not inherited.
func (g *GatewayAdapter) Send(ctx context.Context, who GatewayIdentity, id, text string) error {
	if err := g.bound(ctx, who, GatewaySend, id); err != nil {
		return err
	}
	return g.sessions.Ask(g.messageContext(ctx), id, text)
}

// SendImages starts an answer with downloaded pictures, using the same binding
// and send authorization as Send. It never steers or approves a question. An
// unsupported backend fails explicitly rather than silently losing pictures.
// The host must authenticate and download attachments, validate/resize their
// content and bound resource use before calling. Images are not archived.
// No images is equivalent to Send, including on text-only backends.
func (g *GatewayAdapter) SendImages(ctx context.Context, who GatewayIdentity, id, text string, images []Image) error {
	if len(images) == 0 {
		return g.Send(ctx, who, id, text)
	}
	if err := g.bound(ctx, who, GatewaySend, id); err != nil {
		return err
	}
	backend, ok := g.sessions.(ImageSessions)
	if !ok {
		return ErrGatewayImages
	}
	return backend.AskImages(g.messageContext(ctx), id, text, images)
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
	return g.sessions.Steer(g.messageContext(ctx), id, text)
}

// ReplyImages delivers a conversational reply, including picture-only input.
// It uses GatewayReply authorization, not GatewaySend: the text-only fallback
// may approve a pending call. Image-bearing replies never grant permission.
// Like Reply, it has no atomic question-ID targeting. The host must prepare
// and bound attachments as for SendImages; unsupported backends fail explicitly.
func (g *GatewayAdapter) ReplyImages(ctx context.Context, who GatewayIdentity, id, text string, images []Image) error {
	if len(images) == 0 {
		return g.Reply(ctx, who, id, text)
	}
	if err := g.bound(ctx, who, GatewayReply, id); err != nil {
		return err
	}
	backend, ok := g.sessions.(ReplyImageSessions)
	if !ok {
		return ErrGatewayImages
	}
	return backend.SteerImages(g.messageContext(ctx), id, text, images)
}

// Cancel stops the answer and its delegated tasks, without deleting the session.
func (g *GatewayAdapter) Cancel(ctx context.Context, who GatewayIdentity, id string) error {
	if err := g.bound(ctx, who, GatewayCancel, id); err != nil {
		return err
	}
	return g.sessions.Cancel(ctx, id)
}

// SetDirectory sets the project directory of the session id, or resets it to
// the profile workspace with "". The gateway acts for the owner in the
// conversation, and what its agent does goes on in that project: relative
// paths resolve there, and so do the memories of its scope. A path that is no
// directory, and a session that answers or works on a delegated task, fail
// with the backend's error, ErrBusy among them.
func (g *GatewayAdapter) SetDirectory(ctx context.Context, who GatewayIdentity, id, path string) error {
	if err := g.bound(ctx, who, GatewayDirectory, id); err != nil {
		return err
	}
	return g.sessions.SetDirectory(ctx, id, path)
}

// SendAttachments delivers files under the same authorization and binding as
// Send. No files falls back to text; unsupported backends fail explicitly.
func (g *GatewayAdapter) SendAttachments(ctx context.Context, who GatewayIdentity, id, text string, files []Attachment) error {
	if len(files) == 0 {
		return g.Send(ctx, who, id, text)
	}
	if err := g.bound(ctx, who, GatewaySend, id); err != nil {
		return err
	}
	backend, ok := g.sessions.(AttachmentSessions)
	if !ok {
		return ErrGatewayAttachments
	}
	return backend.AskAttachments(g.messageContext(ctx), id, text, files)
}

// ReplyAttachments delivers conversational files with GatewayReply authority.
// File-bearing replies must never approve a pending permission question.
func (g *GatewayAdapter) ReplyAttachments(ctx context.Context, who GatewayIdentity, id, text string, files []Attachment) error {
	if len(files) == 0 {
		return g.Reply(ctx, who, id, text)
	}
	if err := g.bound(ctx, who, GatewayReply, id); err != nil {
		return err
	}
	backend, ok := g.sessions.(ReplyAttachmentSessions)
	if !ok {
		return ErrGatewayAttachments
	}
	return backend.SteerAttachments(g.messageContext(ctx), id, text, files)
}
