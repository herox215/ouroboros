// Package matrix implements an allowlisted, encrypted-only Matrix gateway.
package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type Credentials struct{ Homeserver, UserID, Token, DeviceID string }
type Config struct {
	Homeserver, UserID, Token, DeviceID, StateDir string
	AllowedUsers                                  []string
}
type Image struct {
	MIMEType string
	Data     []byte
}

// Attachment is a downloaded, integrity-checked Matrix file.
type Attachment struct {
	Name, MIMEType string
	Data           []byte
}
type Message struct {
	RoomID, Sender, Text string
	Images               []Image
	Attachments          []Attachment
	// Metadata is an independent snapshot; location timestamps are Unix milliseconds.
	Metadata map[string]any
}

// Login exchanges a password for device credentials. Passwords are never retained.
func Login(ctx context.Context, homeserver, user, password string) (Credentials, error) {
	if err := ValidateServer(homeserver); err != nil {
		return Credentials{}, err
	}
	// NewClient only parses the URL, which ValidateServer did.
	cli, _ := mautrix.NewClient(homeserver, "", "")
	resp, err := cli.Login(ctx, &mautrix.ReqLogin{Type: mautrix.AuthTypePassword, Identifier: mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: user}, Password: password, InitialDeviceDisplayName: "Ouroboros"})
	if err != nil {
		return Credentials{}, errors.New("the Matrix password login failed")
	}
	if resp.AccessToken == "" || resp.UserID == "" || resp.DeviceID == "" {
		return Credentials{}, errors.New("incomplete Matrix login response")
	}
	return Credentials{homeserver, string(resp.UserID), resp.AccessToken, string(resp.DeviceID)}, nil
}

// ValidateServer checks the homeserver URL for both transport and settings.
// HTTP is allowed on loopback and, by explicit owner authorization, only at
// http://100.91.15.23:8008 on Tailscale. This is not a general exception for
// Tailscale addresses, other ports or URL paths, and does not alter TLS verification.
func ValidateServer(server string) error {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("the Matrix homeserver must be a URL without credentials, query or fragment")
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && (loopback || server == "http://100.91.15.23:8008")) {
		return errors.New("the Matrix homeserver must be HTTPS (HTTP is allowed only on loopback or the explicitly authorized http://100.91.15.23:8008 Tailscale endpoint)")
	}
	return nil
}

type cryptography interface {
	decrypt(context.Context, *event.Event) (*event.Event, error)
	encrypt(context.Context, id.RoomID, id.UserID, event.Type, any) (*event.EncryptedEventContent, error)
	close() error
}

// startCrypto sets up the encryption of a client that starts to run; tests
// replace it to hand Run decrypted events of their choosing.
var startCrypto = newCrypto

type Client struct {
	cfg     Config
	cli     *mautrix.Client
	allowed map[id.UserID]bool
	mu      sync.RWMutex
	status  string
	running bool
	crypto  cryptography
	sendMu  sync.Mutex
	polls   pollTracker
}

func New(cfg Config) (*Client, error) {
	if err := ValidateServer(cfg.Homeserver); err != nil {
		return nil, err
	}
	if cfg.UserID == "" || cfg.Token == "" || cfg.DeviceID == "" || cfg.StateDir == "" {
		return nil, errors.New("the Matrix client requires user, token, device and state directory")
	}
	// NewClient only parses the URL, which ValidateServer did.
	cli, _ := mautrix.NewClient(cfg.Homeserver, id.UserID(cfg.UserID), cfg.Token)
	cli.DeviceID = id.DeviceID(cfg.DeviceID)
	c := &Client{cfg: cfg, cli: cli, allowed: map[id.UserID]bool{}, status: "stopped"}
	for _, user := range cfg.AllowedUsers {
		if _, _, err := id.UserID(user).Parse(); err != nil {
			return nil, fmt.Errorf("invalid allowed Matrix user ID %q", user)
		}
		c.allowed[id.UserID(user)] = true
	}
	return c, nil
}
func (c *Client) Status() string     { c.mu.RLock(); defer c.mu.RUnlock(); return c.status }
func (c *Client) setStatus(s string) { c.mu.Lock(); c.status = s; c.mu.Unlock() }

// Close must be called after Run returns; cancel Run's context to stop syncing.
func (c *Client) Close() error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return errors.New("cancel and wait for Matrix Run before Close")
	}
	if c.crypto != nil {
		err := c.crypto.close()
		c.crypto = nil
		return err
	}
	return nil
}

// safeState requires exactly the bot and one allowlisted joined user. Even a
// third invited or knocking user blocks all message handling and key sharing.
func (c *Client) safeState(events []*event.Event) (id.UserID, error) {
	members := map[id.UserID]event.Membership{}
	encrypted := false
	for _, e := range events {
		if e.StateKey == nil || (e.Type.Type != event.StateMember.Type && e.Type.Type != event.StateEncryption.Type) {
			continue
		}
		if err := e.Content.ParseRaw(e.Type); err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
			return "", errors.New("invalid room state")
		}
		switch e.Type.Type {
		case event.StateEncryption.Type:
			encrypted = e.Content.AsEncryption().Algorithm == id.AlgorithmMegolmV1
		case event.StateMember.Type:
			members[id.UserID(*e.StateKey)] = e.Content.AsMember().Membership
		}
	}
	if !encrypted || members[c.cli.UserID] != event.MembershipJoin {
		return "", errors.New("room is not an encrypted joined room")
	}
	var peer id.UserID
	for user, membership := range members {
		if user == c.cli.UserID {
			continue
		}
		switch membership {
		case event.MembershipLeave, event.MembershipBan:
			continue
		case event.MembershipJoin:
			if peer != "" || !c.allowed[user] {
				return "", errors.New("room is not an allowed 1:1")
			}
			peer = user
		default:
			return "", errors.New("room has invited or unknown members")
		}
	}
	if peer == "" {
		return "", errors.New("room has no allowed peer")
	}
	return peer, nil
}
func (c *Client) safeRoom(ctx context.Context, room id.RoomID) (id.UserID, error) {
	state, err := c.cli.StateAsArray(ctx, room)
	if err != nil {
		return "", errors.New("cannot verify Matrix room state")
	}
	peer, err := c.safeState(state)
	if err != nil {
		return "", err
	}
	for _, e := range state {
		e.RoomID = room
		c.cli.StateStoreSyncHandler(ctx, e)
	}
	return peer, nil
}

// Send sends to the room's current allowlisted peer. Use SendTo for replies
// bound to the sender of an earlier message.
func (c *Client) Send(ctx context.Context, room, text string) error {
	return c.send(ctx, room, "", text)
}

// SendTo sends only if the room still contains the expected allowlisted peer.
func (c *Client) SendTo(ctx context.Context, room, expectedPeer, text string) error {
	if expectedPeer == "" {
		return errors.New("the expected Matrix peer is required")
	}
	return c.send(ctx, room, id.UserID(expectedPeer), text)
}

// TypingTo publishes a short-lived typing notification only to the expected
// allowlisted peer. Matrix typing notifications are ephemeral, not encrypted.
func (c *Client) TypingTo(ctx context.Context, room, expectedPeer string, typing bool) error {
	if expectedPeer == "" {
		return errors.New("the expected Matrix peer is required")
	}
	peer, err := c.safeRoom(ctx, id.RoomID(room))
	if err != nil {
		return err
	}
	if peer != id.UserID(expectedPeer) {
		return errors.New("the peer of the Matrix room changed")
	}
	_, err = c.cli.UserTyping(ctx, id.RoomID(room), typing, 30*time.Second)
	return err
}

func (c *Client) send(ctx context.Context, room string, expectedPeer id.UserID, text string) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.sendContent(ctx, room, expectedPeer, &event.MessageEventContent{MsgType: event.MsgText, Body: text})
}

// sendContent is send for any message content; c.sendMu is held.
func (c *Client) sendContent(ctx context.Context, room string, expectedPeer id.UserID, content *event.MessageEventContent) error {
	_, err := c.sendEvent(ctx, room, expectedPeer, event.EventMessage, content, nil)
	return err
}

// sendEvent preserves the same key-sharing and membership checks for every
// encrypted event type. c.sendMu is held; check may reject a poll closed during
// key exchange, immediately before ciphertext publication.
func (c *Client) sendEvent(ctx context.Context, room string, expectedPeer id.UserID, kind event.Type, content any, check func() error) (*mautrix.RespSendEvent, error) {
	c.mu.RLock()
	crypt := c.crypto
	ready := c.running && crypt != nil
	c.mu.RUnlock()
	if !ready {
		return nil, errors.New("the Matrix transport is not running")
	}
	peer, err := c.safeRoom(ctx, id.RoomID(room))
	if err != nil {
		return nil, err
	}
	// Bind queued replies before any encryption or key sharing.
	if expectedPeer != "" && peer != expectedPeer {
		return nil, errors.New("the peer of the Matrix room changed")
	}
	if check != nil {
		if err := check(); err != nil {
			return nil, err
		}
	}
	encrypted, err := crypt.encrypt(ctx, id.RoomID(room), peer, kind, content)
	if err != nil {
		return nil, errors.New("the Matrix encryption failed")
	}
	// Recheck after key exchange, before publishing ciphertext.
	current, err := c.safeRoom(ctx, id.RoomID(room))
	if err != nil {
		return nil, err
	}
	if current != peer {
		return nil, errors.New("the peer of the Matrix room changed")
	}
	if check != nil {
		if err := check(); err != nil {
			return nil, err
		}
	}
	return c.cli.SendMessageEvent(ctx, id.RoomID(room), event.EventEncrypted, encrypted)
}

func (c *Client) Run(ctx context.Context, onMessage func(context.Context, Message)) error {
	c.mu.Lock()
	if c.running || c.crypto != nil {
		c.mu.Unlock()
		return errors.New("a Matrix client can only Run once; create a new client to restart")
	}
	c.running = true
	c.status = "starting"
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.running = false; c.status = "stopped"; c.mu.Unlock() }()
	who, err := c.cli.Whoami(ctx)
	if err != nil || who.UserID != c.cli.UserID || who.DeviceID != c.cli.DeviceID {
		return errors.New("the Matrix credentials do not match the configured user and device")
	}
	started := time.Now().UnixMilli()
	first := true
	syncer := mautrix.NewDefaultSyncer()
	c.cli.Syncer = syncer
	syncer.OnSync(func(ctx context.Context, resp *mautrix.RespSync, since string) bool {
		for room, data := range resp.Rooms.Invite {
			c.handleInvite(ctx, room, data.State.Events)
		}
		for _, data := range resp.Rooms.Join {
			joined := false
			for _, e := range data.Timeline.Events {
				if e.Type == event.StateMember && e.StateKey != nil && *e.StateKey == c.cfg.UserID {
					_ = e.Content.ParseRaw(e.Type)
					if e.Content.AsMember().Membership == event.MembershipJoin {
						joined = true
					}
				}
			}
			kept := data.Timeline.Events[:0]
			for _, e := range data.Timeline.Events {
				if e.StateKey != nil || (!first && !joined && e.Timestamp >= started) {
					kept = append(kept, e)
				}
			}
			data.Timeline.Events = kept
		}
		first = false
		return true
	})
	crypt, err := startCrypto(ctx, c)
	if err != nil {
		c.setStatus("crypto initialization failed")
		return err
	}
	status := "syncing"
	if signer, ok := crypt.(interface {
		crossSign(context.Context, *Client) error
	}); ok {
		// A device that is not cross-signed still works: the status tells the
		// owner why the clients of the allowed users warn about it.
		signCtx, cancel := context.WithTimeout(ctx, time.Minute)
		if err := signer.crossSign(signCtx, c); err != nil {
			status = "syncing; " + err.Error()
		}
		cancel()
	}
	c.mu.Lock()
	c.crypto = crypt
	c.mu.Unlock()
	seen := map[id.EventID]bool{}
	var seenMu sync.Mutex
	locations := &locationTracker{}
	polls := &c.polls
	syncer.OnEvent(func(ctx context.Context, e *event.Event) {
		isLocation := locationEvent(e)
		isPoll := pollEvent(e)
		if e.Type != event.EventMessage && !isLocation && !isPoll {
			return
		}
		// Beacon announcements are room state (and thus normally plaintext).
		// They may start/stop sharing but never supply coordinates. Accept only
		// sender-keyed state from joined-room sync; safeRoom below rechecks the
		// encrypted private room. Coordinates still require trusted decryption.
		announcement := beaconInfoType(e.Type.Type) && e.StateKey != nil && *e.StateKey == string(e.Sender) && e.Mautrix.EventSource&event.SourceJoin != 0 && e.Mautrix.EventSource&(event.SourceState|event.SourceTimeline) != 0
		if e.Sender == c.cli.UserID || !c.allowed[e.Sender] || e.ID == "" {
			return
		}
		if !announcement {
			if e.Mautrix.EventSource&event.SourceDecrypted == 0 || e.Mautrix.EventSource&event.SourceTimeline == 0 || e.Timestamp < started {
				return
			}
			// Bind the claimed sender to the authenticated direct-session device.
			// Unknown, forwarded, imported or mismatching keys fail closed.
			if e.Mautrix.TrustSource == nil || e.Mautrix.TrustSource.UserID != e.Sender || e.Mautrix.ForwardedKeys || e.Mautrix.TrustState < id.TrustStateUnset || e.Mautrix.TrustState == id.TrustStateUnknownDevice || e.Mautrix.TrustState == id.TrustStateForwarded || e.Mautrix.TrustState == id.TrustStateBackup {
				return
			}
		}
		content := e.Content.AsMessage()
		if !isLocation && !isPoll && ((content.MsgType != event.MsgText && content.MsgType != event.MsgImage && content.MsgType != event.MsgFile && content.MsgType != event.MsgAudio && content.MsgType != event.MsgVideo) || (content.MsgType == event.MsgText && content.Body == "") || content.RelatesTo != nil && content.RelatesTo.Type == event.RelReplace) {
			return
		}
		peer, err := c.safeRoom(ctx, e.RoomID)
		if err != nil || peer != e.Sender {
			return
		}
		seenMu.Lock()
		if seen[e.ID] {
			seenMu.Unlock()
			return
		}
		seen[e.ID] = true
		seenMu.Unlock()
		if isLocation {
			locations.update(e, time.Now())
			return // Location-only events never invoke the assistant or send replies.
		}
		msg := Message{RoomID: string(e.RoomID), Sender: string(e.Sender), Text: content.Body}
		if isPoll {
			msg.Text = polls.update(e)
			if msg.Text == "" {
				return // Votes and closures update context without invoking the assistant.
			}
		}
		if !isPoll && content.MsgType != event.MsgText {
			if content.MsgType == event.MsgImage {
				img, err := c.downloadImage(ctx, content)
				if err != nil {
					_ = c.SendTo(ctx, msg.RoomID, msg.Sender, "The image could not be read: "+err.Error())
					return
				}
				msg.Images = []Image{img}
			} else {
				file, err := c.downloadAttachment(ctx, content)
				if err != nil {
					_ = c.SendTo(ctx, msg.RoomID, msg.Sender, "The attachment could not be read: "+err.Error())
					return
				}
				msg.Attachments = []Attachment{file}
			}
			msg.Text = content.GetCaption()
			// A download may take time; do not deliver after room membership changed.
			peer, err := c.safeRoom(ctx, e.RoomID)
			if err != nil || peer != e.Sender {
				return
			}
		}
		msg.Metadata = locations.snapshot(e.RoomID, e.Sender, time.Now())
		for snapshot := polls.snapshot(e.RoomID, e.Sender); len(snapshot) > 0; snapshot = snapshot[1:] {
			msg.Metadata["polls"] = snapshot
			raw, err := json.Marshal(msg.Metadata)
			if err == nil && len(raw) <= 16*1024 {
				break
			}
			// Preserve existing context if escaped text exceeds the shared budget.
			delete(msg.Metadata, "polls")
		}
		if onMessage != nil {
			onMessage(ctx, msg)
		}
	})
	// Retry a bounded number of undecryptable live events when delayed keys
	// arrive in a subsequent sync. No background requests and no history fetches.
	pending := map[id.EventID]*event.Event{}
	deliver := func(ctx context.Context, e *event.Event) bool {
		if ctx.Err() != nil {
			return true
		}
		decrypted, err := crypt.decrypt(ctx, e)
		if err != nil {
			return false
		}
		syncer.Dispatch(ctx, decrypted)
		return true
	}
	syncer.OnEventType(event.EventEncrypted, func(ctx context.Context, e *event.Event) {
		if e.Mautrix.EventSource&event.SourceTimeline == 0 || e.Mautrix.EventSource&event.SourceJoin == 0 || e.Timestamp < started || !c.allowed[e.Sender] || e.ID == "" {
			return
		}
		if !deliver(ctx, e) && len(pending) < 256 {
			pending[e.ID] = e
		}
	})
	syncer.OnSync(func(ctx context.Context, _ *mautrix.RespSync, _ string) bool {
		for key, e := range pending {
			if time.Now().UnixMilli()-e.Timestamp > 30000 || deliver(ctx, e) {
				delete(pending, key)
			}
		}
		return true
	})
	c.setStatus(status)
	return c.cli.SyncWithContext(ctx)
}
func (c *Client) handleInvite(ctx context.Context, room id.RoomID, state []*event.Event) {
	// Invite state is partial: it only authorizes joining, never processing a message.
	// The authoritative full room state is checked after joining and on every send.
	encrypted := false
	var inviter id.UserID
	for _, e := range state {
		if e.Type.Type == event.StateEncryption.Type {
			_ = e.Content.ParseRaw(e.Type)
			encrypted = e.Content.AsEncryption().Algorithm == id.AlgorithmMegolmV1
		}
		if e.Type.Type == event.StateMember.Type && e.StateKey != nil && *e.StateKey == c.cfg.UserID {
			_ = e.Content.ParseRaw(e.Type)
			if e.Content.AsMember().Membership == event.MembershipInvite {
				inviter = e.Sender
			}
		}
	}
	if !encrypted || inviter == "" || !c.allowed[inviter] {
		return
	}
	for _, e := range state {
		if e.Type.Type == event.StateMember.Type && e.StateKey != nil {
			_ = e.Content.ParseRaw(e.Type)
			m := e.Content.AsMember().Membership
			if (m == event.MembershipInvite || m == event.MembershipJoin || m == event.MembershipKnock) && *e.StateKey != c.cfg.UserID && *e.StateKey != string(inviter) {
				return
			}
		}
	}
	_, _ = c.cli.JoinRoomByID(ctx, room)
}
