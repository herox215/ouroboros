package matrix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
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
	// SentVersion separates stable output IDs from the legacy whole-event hashes.
	SentVersion int
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
	data, _ := json.Marshal(c.entries) // plain data only: cannot fail
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
		if entry.SentVersion < 0 || entry.SentVersion > 1 {
			return nil, errors.New("unsupported gateway delivery state")
		}
		if len(entry.Sent) == 0 {
			entry.SentVersion = 1
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
	}, bindings, plugin.WithGatewayName("matrix"))
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
	text := strings.TrimSpace(msg.Text)
	if len(msg.Images) == 0 && text == "/new" {
		// Cancel before switching so an old answer cannot continue unnoticed.
		// Keep the old session and its history; only replace the room binding.
		if entry != nil {
			if err := c.adapter.Cancel(ctx, who, entry.Session); err != nil {
				return err
			}
		}
		id, err := c.adapter.Open(ctx, who)
		if err != nil {
			return err
		}
		next := conversation{Session: id, Identity: who, Room: msg.RoomID, SentVersion: 1}
		if entry == nil {
			c.entries[key] = &next
			if err := c.save(); err != nil {
				delete(c.entries, key)
				return err
			}
		} else {
			// Retain the pointer used by the ephemeral typing tracker.
			previous := *entry
			*entry = next
			if err := c.save(); err != nil {
				*entry = previous
				return err
			}
		}
		return conn.SendTo(ctx, msg.RoomID, msg.Sender, "New chat started. Your next message starts a fresh conversation; the previous chat is kept in Ouro.")
	}
	if entry == nil {
		id, err := c.adapter.Open(ctx, who)
		if err != nil {
			return err
		}
		entry = &conversation{Session: id, Identity: who, Room: msg.RoomID, SentVersion: 1}
		c.entries[key] = entry
		if err = c.save(); err != nil {
			delete(c.entries, key)
			return err
		}
	}
	if len(msg.Images) == 0 && text == "/cancel" {
		return c.adapter.Cancel(ctx, who, entry.Session)
	}
	events, err := c.adapter.Poll(ctx, who, entry.Session)
	if err != nil {
		return err
	}
	var pending *plugin.SessionEvent
	for i := range events {
		event := &events[i]
		switch event.Kind {
		case plugin.SessionAsked:
			pending = nil
			if !event.Done {
				pending = event
			}
		case plugin.SessionMessage, plugin.SessionAnswer, plugin.SessionFailed, plugin.SessionCanceled, plugin.SessionRestart, plugin.SessionRestarted:
			pending = nil
		}
	}
	explicit := len(msg.Images) == 0 && strings.HasPrefix(text, "/answer ")
	if explicit {
		text = strings.TrimSpace(strings.TrimPrefix(text, "/answer "))
	}
	if pending != nil {
		if len(msg.Images) != 0 {
			return conn.SendTo(ctx, msg.RoomID, msg.Sender, "Please answer the pending question with text first, then resend the image.")
		}
		options := pending.AsQuestion().Options
		if n, err := strconv.Atoi(text); err == nil && n > 0 && n <= len(options) {
			text = options[n-1]
		}
		return c.adapter.Reply(ctx, who, entry.Session, text)
	}
	if explicit {
		return conn.SendTo(ctx, msg.RoomID, msg.Sender, "There is no pending question to answer.")
	}
	err = c.adapter.SendImages(ctx, who, entry.Session, msg.Text, msg.Images)
	if errors.Is(err, plugin.ErrGatewayImages) {
		return conn.SendTo(ctx, msg.RoomID, msg.Sender, "This session backend does not support images.")
	}
	if errors.Is(err, plugin.ErrBusy) {
		return conn.SendTo(ctx, msg.RoomID, msg.Sender, "An answer is already in progress. Use /cancel to stop it. When a question is pending, just send your answer.")
	}
	return err
}

func presented(e plugin.SessionEvent) string {
	switch e.Kind {
	case plugin.SessionSay, plugin.SessionAnswer:
		return e.Text
	case plugin.SessionFailed:
		return "The agent could not complete this answer. Check the local Ouro session for details."
	case plugin.SessionCanceled:
		return "Answer canceled."
	case plugin.SessionRestart:
		return "Restarting…"
	case plugin.SessionRestarted:
		return "Restarted."
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
			text += "\n"
			for i, option := range q.Options {
				text += "\n" + strconv.Itoa(i+1) + ". " + option
			}
			return text + "\n\nReply with a number or your own answer (no command needed)."
		}
		return text + "\n\nJust send your answer (no command needed)."
	}
	return ""
}

// poll publishes only presentation events, never thoughts, tool arguments,
// tool output or another session's history. Output IDs survive archive replay;
// timestamps, usage metadata and snapshot positions are not delivery identities.
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
			// A failure waits for the next round; later output must not overtake it.
			_ = c.publish(ctx, conn, entry, events)
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// deliveryKeys uses persisted occurrence IDs, not the mutable event envelope.
// The fallback is for session backends without IDs: identical text occurrences
// remain distinct, while metadata and non-presentation events cannot move a key.
func deliveryKeys(events []plugin.SessionEvent) []string {
	keys := make([]string, len(events))
	occurrences := map[string]int{}
	for i, event := range events {
		text := presented(event)
		if text == "" {
			continue
		}
		if event.PresentationID != "" {
			keys[i] = "id:" + event.PresentationID
			continue
		}
		kind := string(event.Kind)
		if event.Kind == plugin.SessionSay || event.Kind == plugin.SessionAnswer {
			kind = "assistant"
		}
		data := kind + "\n" + text
		// Questions are live-only; completed ones are not reconstructed by replay.
		// Their occurrence times keep a later identical question distinct.
		if event.Kind == plugin.SessionAsked {
			data += "\n" + event.At.Format(time.RFC3339Nano)
		}
		hash := sha256.Sum256([]byte(data))
		fingerprint := hex.EncodeToString(hash[:])
		occurrences[fingerprint]++
		keys[i] = "text:" + fingerprint + ":" + strconv.Itoa(occurrences[fingerprint])
	}
	return keys
}

// publish is called with c.mu held and only after the adapter authorized Poll.
func (c *conversations) publish(ctx context.Context, conn connection, entry *conversation, events []plugin.SessionEvent) error {
	keys := deliveryKeys(events)
	if entry.SentVersion == 0 {
		// Legacy hashes cannot be inverted after replay changes the event metadata.
		// The owner chose to baseline existing history rather than replay it during
		// this one-time migration. Persist the baseline before delivering new output.
		previous := entry.Sent
		entry.Sent = map[string]bool{}
		for _, key := range keys {
			if key != "" {
				entry.Sent[key] = true
			}
		}
		entry.SentVersion = 1
		if err := c.save(); err != nil {
			entry.Sent, entry.SentVersion = previous, 0
			return err
		}
		return nil
	}
	if entry.Sent == nil {
		entry.Sent = map[string]bool{}
	}
	peer, _, _ := strings.Cut(entry.Identity.Subject, "\n")
	for i, key := range keys {
		if key == "" || entry.Sent[key] {
			continue
		}
		if err := conn.SendTo(ctx, entry.Room, peer, presented(events[i])); err != nil {
			return err
		}
		entry.Sent[key] = true
		if err := c.save(); err != nil {
			delete(entry.Sent, key)
			return err
		}
	}
	return nil
}
