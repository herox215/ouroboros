package matrix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/safefile"
	"ouroboros/plugin"
)

type conversation struct {
	Session  string
	Identity plugin.GatewayIdentity
	Room     string
	Sent     map[string]bool
}
type conversations struct {
	store   store
	adapter *plugin.GatewayAdapter
	mu      sync.Mutex
	entries map[string]*conversation
}

func account(c configuration) string {
	hash := sha256.Sum256([]byte(c.Homeserver + "\n" + c.UserID))
	return "matrix:" + hex.EncodeToString(hash[:])
}
func (c *conversations) save() error {
	data, err := json.Marshal(c.entries)
	if err != nil {
		return err
	}
	return safefile.Write(c.store.path("conversations.json"), data, 0600)
}
func openConversations(s store, sessions plugin.GatewaySessions) (*conversations, error) {
	c := &conversations{store: s, entries: map[string]*conversation{}}
	if data, err := os.ReadFile(s.path("conversations.json")); err == nil {
		if err = json.Unmarshal(data, &c.entries); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	bindings := map[string]plugin.GatewayIdentity{}
	for _, entry := range c.entries {
		if entry == nil || entry.Session == "" || entry.Room == "" {
			return nil, errors.New("invalid gateway conversation state")
		}
		bindings[entry.Session] = entry.Identity
	}
	adapter, err := plugin.NewGatewayAdapterWithBindings(sessions, func(ctx context.Context, who plugin.GatewayIdentity, op plugin.GatewayOperation) (plugin.GatewayPermission, error) {
		cfg, err := s.load()
		if err != nil || !cfg.Enabled || cfg.Token == "" || who.Gateway != account(cfg) {
			return plugin.GatewayPermission{}, plugin.ErrGatewayDenied
		}
		sender, _, ok := strings.Cut(who.Subject, "\n")
		if !ok {
			return plugin.GatewayPermission{}, plugin.ErrGatewayDenied
		}
		for _, allowed := range cfg.AllowedUsers {
			if sender == allowed {
				// Saving an account into this local owner's allowlist deliberately
				// delegates the profile's agent tools, not its settings/trust UI.
				return plugin.GatewayPermission{OwnerSession: true}, nil
			}
		}
		return plugin.GatewayPermission{}, plugin.ErrGatewayDenied
	}, bindings)
	if err != nil {
		return nil, err
	}
	c.adapter = adapter
	return c, nil
}
func (c *conversations) receive(ctx context.Context, conn connection, msg incoming) error {
	cfg, err := c.store.load()
	if err != nil {
		return err
	}
	who := plugin.GatewayIdentity{Gateway: account(cfg), Subject: msg.Sender + "\n" + msg.RoomID}
	key := who.Gateway + "\n" + who.Subject
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		id, err := c.adapter.Open(ctx, who)
		if err != nil {
			return err
		}
		entry = &conversation{Session: id, Identity: who, Room: msg.RoomID}
		c.entries[key] = entry
		if err = c.save(); err != nil {
			delete(c.entries, key)
			return err
		}
	}
	text := strings.TrimSpace(msg.Text)
	switch {
	case text == "/cancel":
		return c.adapter.Cancel(ctx, who, entry.Session)
	case strings.HasPrefix(text, "/answer "):
		events, err := c.adapter.Poll(ctx, who, entry.Session)
		if err != nil {
			return err
		}
		pending := false
		for _, event := range events {
			if event.Kind == plugin.SessionAsked {
				pending = !event.Done
			}
		}
		if !pending {
			return conn.SendTo(ctx, msg.RoomID, msg.Sender, "There is no pending question to answer.")
		}
		return c.adapter.Reply(ctx, who, entry.Session, strings.TrimSpace(strings.TrimPrefix(text, "/answer ")))
	default:
		err = c.adapter.Send(ctx, who, entry.Session, msg.Text)
		if errors.Is(err, plugin.ErrBusy) {
			return conn.SendTo(ctx, msg.RoomID, msg.Sender, "An answer is already in progress. Use /cancel to stop it, or /answer TEXT to answer a pending question.")
		}
		return err
	}
}

func presented(e plugin.SessionEvent) string {
	switch e.Kind {
	case plugin.SessionSay, plugin.SessionAnswer:
		return e.Text
	case plugin.SessionFailed:
		return "The agent could not complete this answer. Check the local Ouro session for details."
	case plugin.SessionCanceled:
		return "Answer canceled."
	case plugin.SessionAsked:
		if e.Done {
			return ""
		}
		q := e.AsQuestion()
		if q.Question == "" {
			return ""
		}
		text := q.Question
		if len(q.Options) > 0 {
			text += "\n\n" + strings.Join(q.Options, "\n")
		}
		return text + "\n\nReply with /answer YOUR ANSWER"
	}
	return ""
}

// poll publishes only presentation events, never thoughts, tool arguments,
// tool output or another session's history. Event fingerprints survive restarts;
// numeric offsets would be unsafe because snapshot metadata can move.
func (c *conversations) poll(ctx context.Context, conn connection) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	typing := typing{}
	defer typing.stop(conn)
	for {
		c.mu.Lock()
		for _, entry := range c.entries {
			events, err := c.adapter.Poll(ctx, entry.Identity, entry.Session)
			if err != nil {
				typing.update(ctx, conn, entry, false, time.Now())
				continue
			}
			typing.update(ctx, conn, entry, answering(events), time.Now())
			if entry.Sent == nil {
				entry.Sent = map[string]bool{}
			}
			for _, event := range events {
				text := presented(event)
				if text == "" {
					continue
				}
				data, _ := json.Marshal(event)
				hash := sha256.Sum256(data)
				key := hex.EncodeToString(hash[:])
				if entry.Sent[key] {
					continue
				}
				peer, _, ok := strings.Cut(entry.Identity.Subject, "\n")
				if !ok {
					break
				}
				if err = conn.SendTo(ctx, entry.Room, peer, text); err != nil {
					break
				}
				entry.Sent[key] = true
				if err = c.save(); err != nil {
					delete(entry.Sent, key)
					break
				}
			}
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
