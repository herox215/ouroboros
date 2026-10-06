// Package history is the plugin that archives every conversation of the
// agents in full and gives them the tools to search and read it again:
// history_search, history_read and history_sessions. What is sent to the LLM
// stays as it is; the archive only makes what the memory cut off, and the
// conversations before, reachable for the agent.
//
// The owner reaches all of its own conversations, a peer only the one it is
// in. The owner deletes a conversation from the archive with the command
// ouro history delete.
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

// needs are the services the plugin uses.
type needs struct {
	Console *plugin.Console
}

// Plugin contributes the hook that archives every answer of the agents of
// the node in dir, the tools that read the archive, what the owner is shown
// of their calls, and the command with which the owner lists and deletes
// what is archived. Every call of a tool is logged in dir, without what was
// searched or found.
func Plugin(dir home.Dir) plugin.Plugin {
	return plugin.New("history", func(ctx *plugin.Context, n needs) error {
		h := &archive{history.Open(dir.History()), dir.Log}
		plugin.Add(ctx, plugin.AnswerHook{Name: "history", After: h.after})
		for _, tool := range h.tools() {
			plugin.Add(ctx, tool)
		}
		for name, doing := range map[string]string{Search: "searching the history", Read: "reading the history", Sessions: "listing the history"} {
			plugin.Add(ctx, plugin.ToolView{Tool: name, Show: func(json.RawMessage) string { return doing }, Detail: assistant.HowItEnded})
		}
		plugin.Add(ctx, plugin.Command{Name: "history", Args: "[delete SESSION]",
			Help: "list the archived conversations, or delete one", Run: h.command(n.Console)})
		return nil
	})
}

// archive is the archive of the node and its log.
type archive struct {
	archive *history.Archive
	log     func(format string, args ...any)
}

// after keeps an answer that ended. A failure is the plugin's diagnostic,
// and leaves the answer as it is.
func (h *archive) after(_ context.Context, o plugin.AnswerOutcome) error {
	who := history.Who{Session: o.Conversation.ID}
	if o.Conversation.Peer {
		if o.Conversation.Origin.PublicKey == "" {
			return errors.New("a conversation of a peer without its key is not archived")
		}
		who.Peer = o.Conversation.Origin.PublicKey
	}
	return h.archive.Append(who, o.Messages, o.Err)
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
}

// about is what each tool says of the archive. The descriptions are the
// same in every conversation, so that the prompt stays the same for the
// cache of the provider.
const about = "Every conversation is archived in full: messages that are no longer in your context, " +
	"and earlier conversations, can be found there. "

// hint is what the tools say of what other conversations hold.
const hint = "What you find in another conversation may be outdated: take it as a hint to check, not as a fact."

func (h *archive) tools() []plugin.Tool {
	return []plugin.Tool{
		{
			Name: Search,
			Description: about + fmt.Sprintf("Search the archive for a text, ignoring case. Returns per hit the session, the time, "+
				"the record number and an excerpt; read the records around a hit with %s. At most %d hits. ", Read, history.MaxHits) +
				"The owner may search all of their conversations; a peer only the one it is in. " + hint,
			Schema: `{"type":"object","properties":{` +
				`"query":{"type":"string","description":"the text to find"},` +
				`"scope":{"type":"string","enum":["current","all"],"description":"current (the default): this conversation; all: every conversation of the owner"},` +
				`"limit":{"type":"integer","description":"how many hits at most, ` + strconv.Itoa(history.DefaultHits) + ` if left out"}},` +
				`"required":["query"]}`,
			Run: run(func(ctx context.Context, args searchArgs) (string, error) {
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
			Description: about + fmt.Sprintf("Read the records of an archived conversation in order, from a record number on: "+
				"at most %d per call, and fewer when they are long; a note at the end says where to continue. ", history.MaxRecords) + hint,
			Schema: `{"type":"object","properties":{` +
				`"session":{"type":"string","description":"the session, as ` + Search + ` and ` + Sessions + ` name it; this conversation if left out"},` +
				`"from":{"type":"integer","description":"the first record, 1 if left out"},` +
				`"count":{"type":"integer","description":"how many records at most, ` + strconv.Itoa(history.DefaultRecords) + ` if left out"}}}`,
			Run: run(func(ctx context.Context, args readArgs) (string, error) {
				w, err := who(ctx)
				if err != nil {
					return "", err
				}
				out, err := h.archive.Read(w, args.Session, args.From, args.Count)
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
				"and their first message: for the owner all of theirs (at most %d), for a peer the one it is in. ", history.MaxSessions) + hint,
			Schema: `{"type":"object","properties":{}}`,
			Run: run(func(ctx context.Context, _ struct{}) (string, error) {
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

// run makes f the run of a tool whose arguments are decoded into T.
func run[T any](f func(ctx context.Context, args T) (string, error)) plugin.RunFunc {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args T
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", errors.New("invalid arguments")
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

// command lists the archive, or deletes a session of it: ouro history
// [delete SESSION].
func (h *archive) command(term *plugin.Console) func(context.Context, []string) error {
	return func(_ context.Context, args []string) error {
		switch {
		case len(args) == 0:
			entries, err := h.archive.List()
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Fprintln(term.Out, "no conversation is archived")
			}
			for _, e := range entries {
				fmt.Fprintf(term.Out, "%s  %s  %s  %q\n", e.ID, e.Modified.Local().Format("2006-01-02 15:04"), size(e.Size), e.Title)
			}
			return nil
		case len(args) == 2 && args[0] == "delete":
			if err := h.archive.Delete(args[1]); err != nil {
				return err
			}
			h.log("history: the owner deleted a session")
			fmt.Fprintf(term.Out, "● deleted %s\n", args[1])
			return nil
		}
		return errors.New("usage: ouro history [delete SESSION]")
	}
}

// size is a number of bytes for people.
func size(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}
