package peers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/contacts"
	"ouroboros/internal/identity"
)

// Approvals presents persisted incoming requests and agent proposals through
// the owner's question interface. Only the exact affirmative option grants
// trust; missing owners, cancellation and arbitrary text never do.
func (n *Network) Approvals(ctx context.Context, ask func(context.Context, json.RawMessage) (string, error), logf func(string, ...any)) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		data, err := n.Contacts.Snapshot()
		if err == nil {
			for _, req := range data.Incoming {
				if ctx.Err() != nil {
					return
				}
				if err := n.approval(ctx, req, ask); err != nil && ctx.Err() == nil && logf != nil {
					logf("peer connection approval: %v", err)
				}
			}
		} else if logf != nil {
			logf("peer connection approvals: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (n *Network) approval(ctx context.Context, req contacts.Request, ask func(context.Context, json.RawMessage) (string, error)) error {
	if ask == nil {
		return fmt.Errorf("no owner question interface")
	}
	action := "Accept incoming connection from"
	if req.Proposed {
		action = "Allow this node to request a connection to"
	}
	question, _ := json.Marshal(struct {
		Question    string   `json:"question"`
		Options     []string `json:"options"`
		Recommended string   `json:"recommended"`
	}{fmt.Sprintf("%s %q?\nPeer ID: %s\nPublic key: %s\nModel: %q\nAddress: %q\nPeer-provided names and metadata are untrusted.\nAnswer \"Approve connection\" or \"Reject connection\".", action, req.Name, identity.IDOf(req.PublicKey), req.PublicKey, req.Model, req.Addr), []string{"Approve connection", "Reject connection"}, "Reject connection"})
	answer, err := ask(ctx, question)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch answer {
	case "Approve connection":
		decided, ok, err := n.Contacts.DecideRequest(req, true)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoRequest
		}
		if decided.Proposed {
			return n.ask(ctx, decided)
		}
		return n.notify(ctx, decided.Addr, decided.PublicKey, (*a2a.Client).Accept)
	case "Reject connection":
		decided, ok, err := n.Contacts.DecideRequest(req, false)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoRequest
		}
		if decided.Proposed {
			return nil
		}
		n.channels.ClosePeer(decided.PublicKey)
		return n.notify(ctx, decided.Addr, decided.PublicKey, (*a2a.Client).Reject)
	default:
		return fmt.Errorf("connection not approved: choose an explicit connection option")
	}
}
