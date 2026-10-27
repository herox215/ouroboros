// Package history is the plugin that gives the agents the tools to search
// and read the archive of their conversations again: history_search,
// history_read and history_sessions. The node archives every conversation in
// full, with or without this plugin (see the assistant plugin); what is sent
// to the LLM stays as it is, and the tools only make what the memory cut off,
// and the conversations before, reachable for the agent.
//
// The owner reaches all of its own conversations, a peer only the one it is
// in. The owner deletes a conversation in the sessions of the terminal UI.
package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/home"
	"ouroboros/plugin"
)

// The names of the tools.
const (
	Search   = "history_search"
	Read     = "history_read"
	Sessions = "history_sessions"
)

// Plugin contributes the tools that read the archive of the node in dir and
// what the owner is shown of their calls. Every call of a tool is logged in
// dir, without what was searched or found.
func Plugin(dir home.Dir) plugin.Plugin {
	return plugin.New("history", func(ctx *plugin.Context, _ struct{}) error {
		h := &archive{history.Open(dir.History()), dir.Log}
		ctx.OnStop(func() { h.archive.Close() })
		for _, tool := range h.tools() {
			plugin.Add(ctx, tool)
		}
		for name, doing := range map[string]string{Search: "searching the history", Read: "reading the history", Sessions: "listing the history"} {
			plugin.Add(ctx, plugin.ToolView{Tool: name, Show: func(json.RawMessage) string { return doing }, Detail: assistant.HowItEnded})
		}
		return nil
	})
}

// archive is the archive of the node and its log.
type archive struct {
	archive *history.Archive
	log     func(format string, args ...any)
}

// who is the party and the conversation a tool runs for.
func who(ctx context.Context) (history.Who, error) {
	w := history.Who{Session: plugin.ContextID(ctx)}
	if w.Session == "" {
		return w, errors.New("the history is only reachable within a conversation")
	}
	if origin, peer := plugin.OriginFrom(ctx); peer {
		if origin.PublicKey == "" {
			return w, history.ErrScope
		}
		w.Peer = origin.PublicKey
	}
	return w, nil
}

// party is how the log names whom a call was for.
func party(w history.Who) string {
	if w.Peer == "" {
		return "the owner"
	}
	return "a peer"
}

type searchArgs struct {
	Query string `json:"query"`
	Scope string `json:"scope"`
	Limit int    `json:"limit"`
}

type readArgs struct {
	Session string `json:"session"`
	From    int    `json:"from"`
	Count   int    `json:"count"`
	Offset  int    `json:"offset"`
}

// about is what the tools that find conversations say of the archive. The
// descriptions are the same in every conversation, so that the prompt stays
// the same for the cache of the provider.
const about = "Every conversation is archived in full: messages that are no longer in your context, " +
	"and earlier conversations, can be found there. "

// hint is what the tools that show what conversations hold say of the
// others.
const hint = "What you find in another conversation may be outdated: take it as a hint to check, not as a fact."

func (h *archive) tools() []plugin.Tool {
	return []plugin.Tool{
		{
			Name: Search,
			Description: about + fmt.Sprintf("Search the archive for a text, ignoring case. Returns per hit the session, the time, "+
				"the record number and an excerpt; read the records around a hit with %s. At most %d hits. ", Read, history.MaxHits) +
				"The owner may search all of their conversations; a peer only the one it is in. " +
				"When the user refers back to an earlier agreement (including UI or dialog design), search the conversation history before confirming specific details as agreed. " +
				"Use scope all for the owner's earlier conversations, and read the surrounding records with history_read to distinguish a proposal from an agreement. " +
				"If the reference remains ambiguous or the history is unavailable or inconclusive, state what is unverified; ask a focused question only if needed, rather than inventing details or giving blanket agreement. " + hint,
			Schema: `{"type":"object","properties":{` +
				`"query":{"type":"string","description":"the text to find"},` +
				`"scope":{"type":"string","enum":["current","all"],"description":"current (the default): this conversation; all: every conversation of the owner"},` +
				`"limit":{"type":"integer","description":"how many hits at most, ` + strconv.Itoa(history.DefaultHits) + ` if left out"}},` +
				`"required":["query"]}`,
			Run: run(Search, func(ctx context.Context, args searchArgs) (string, error) {
				w, err := who(ctx)
				if err != nil {
					return "", err
				}
				all := false
				switch args.Scope {
				case "", "current":
				case "all":
					all = true
				default:
					return "", fmt.Errorf("scope is current or all, not %q", args.Scope)
				}
				out, hits, err := h.archive.Search(w, args.Query, all, args.Limit)
				h.log("%s for %s, scope %s: %d hits%s", Search, party(w), map[bool]string{false: "current", true: "all"}[all], hits, failed(err))
				return out, err
			}),
		},
		{
			Name: Read,
			Description: fmt.Sprintf("Read the records of an archived conversation in order, from a record number on: "+
				"at most %d records and %d KiB per call including the note at the end that says where to continue. "+
				"A long record is returned in pieces: use from and offset from the note to read the rest. ", history.MaxRecords, history.MaxReadBytes>>10) + hint,
			Schema: `{"type":"object","properties":{` +
				`"session":{"type":"string","description":"the session, as ` + Search + ` and ` + Sessions + ` name it; this conversation if left out"},` +
				`"from":{"type":"integer","description":"the first record, 1 if left out; negative counts from the end: -20 reads the last 20 records, what to read first after ouro started anew"},` +
				`"count":{"type":"integer","description":"how many records at most, ` + strconv.Itoa(history.DefaultRecords) + ` if left out"},` +
				`"offset":{"type":"integer","description":"bytes to skip in the first formatted record, from the continuation note; 0 if left out"}}}`,
			Run: run(Read, func(ctx context.Context, args readArgs) (string, error) {
				w, err := who(ctx)
				if err != nil {
					return "", err
				}
				out, err := h.archive.Read(w, args.Session, args.From, args.Count, args.Offset)
				scope := "current"
				if args.Session != "" {
					scope = "a named session"
				}
				h.log("%s for %s, scope %s%s", Read, party(w), scope, failed(err))
				return out, err
			}),
		},
		{
			Name: Sessions,
			Description: about + fmt.Sprintf("List the archived conversations, the most recent first, with when they started "+
				"and their first message: for the owner all of theirs (at most %d), for a peer the one it is in.", history.MaxSessions),
			Schema: `{"type":"object","properties":{}}`,
			Run: run(Sessions, func(ctx context.Context, _ struct{}) (string, error) {
				w, err := who(ctx)
				if err != nil {
					return "", err
				}
				out, listed, err := h.archive.Sessions(w)
				h.log("%s for %s: %d sessions%s", Sessions, party(w), listed, failed(err))
				return out, err
			}),
		},
	}
}

// run makes f the run of the tool name, whose arguments are decoded into T.
func run[T any](name string, f func(ctx context.Context, args T) (string, error)) plugin.RunFunc {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args T
		if err := plugin.Decode(name, raw, &args); err != nil {
			return "", err
		}
		return f(ctx, args)
	}
}

// failed is what the log adds for a call that failed.
func failed(err error) string {
	if err == nil {
		return ""
	}
	return ", failed: " + err.Error()
}
