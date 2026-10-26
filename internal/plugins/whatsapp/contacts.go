package whatsapp

import (
	"context"
	"sort"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

// contact exposes only locally cached names and locally known PN/LID mappings.
// Aliases includes JID itself: readers must check every alias, not guess which
// identifier WhatsApp used when the text was retained.
type contact struct {
	JID          string   `json:"jid"`
	Aliases      []string `json:"aliases"`
	FirstName    string   `json:"first_name,omitempty"`
	FullName     string   `json:"full_name,omitempty"`
	PushName     string   `json:"push_name,omitempty"`
	BusinessName string   `json:"business_name,omitempty"`
}

func (c *client) Contacts(ctx context.Context, query string, limit int) ([]contact, error) {
	out := []contact{}
	// A new, unpaired device has no initialized contact store. Never connect or
	// request pairing to populate it; an existing saved device is usable offline.
	if c.cli.Store.ID == nil {
		return out, nil
	}
	all, err := c.cli.Store.Contacts.GetAllContacts(ctx)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(query)
	jids := make([]types.JID, 0, len(all))
	for jid, info := range all {
		if strings.Contains(strings.ToLower(info.FirstName), query) ||
			strings.Contains(strings.ToLower(info.FullName), query) ||
			strings.Contains(strings.ToLower(info.PushName), query) ||
			strings.Contains(strings.ToLower(info.BusinessName), query) {
			if _, err := exactJID(jid.String()); err == nil && (jid.Server == types.DefaultUserServer || jid.Server == types.HiddenUserServer) {
				jids = append(jids, jid)
			}
		}
	}
	// Stable ordering makes bounded results deterministic, not a relevance or
	// identity ranking. Multiple hits must never imply a selected recipient.
	sort.Slice(jids, func(i, j int) bool { return jids[i].String() < jids[j].String() })
	if len(jids) > limit {
		jids = jids[:limit]
	}
	for _, jid := range jids {
		info := all[jid]
		v := contact{JID: jid.String(), Aliases: []string{jid.String()}, FirstName: info.FirstName, FullName: info.FullName, PushName: info.PushName, BusinessName: info.BusinessName}
		var alias types.JID
		if jid.Server == types.DefaultUserServer {
			alias, err = c.cli.Store.LIDs.GetLIDForPN(ctx, jid)
		} else {
			alias, err = c.cli.Store.LIDs.GetPNForLID(ctx, jid)
		}
		if err != nil {
			return nil, err
		}
		if !alias.IsEmpty() && alias != jid {
			if _, err = exactJID(alias.String()); err != nil {
				return nil, err
			}
			v.Aliases = append(v.Aliases, alias.String())
		}
		out = append(out, v)
	}
	return out, nil
}
