// Package claudesessions is the plugin that gives the agents the tools to
// search and read the transcripts Claude Code keeps of its conversations on
// this machine: claude_sessions, claude_search, claude_read and claude_tools.
// The transcripts are below ~/.claude/projects, see package claudesessions;
// Claude Code owns them and this plugin only reads them.
//
// They are the owner's own work, so only the owner reaches them; a peer gets
// an error. What was read is logged without what was said in it.
package claudesessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/claudesessions"
	"ouroboros/plugin"
)

// The names of the tools.
const (
	Sessions = "claude_sessions"
	Search   = "claude_search"
	Read     = "claude_read"
	Calls    = "claude_tools"
)

// Default is where Claude Code keeps the transcripts of its sessions.
func Default() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// Options is where the plugin reads and logs.
type Options struct {
	// Dir is the directory of the transcripts, Default() if empty.
	Dir string
	// Log is where a call is logged, without what it searched or found.
	Log func(format string, args ...any)
}

// Plugin contributes the tools that read the transcripts of Claude Code.
func Plugin(o Options) plugin.Plugin {
	if o.Dir == "" {
		o.Dir = Default()
	}
	log := o.Log
	if log == nil {
		log = func(string, ...any) {}
	}
	return plugin.New("claude-sessions", func(ctx *plugin.Context, _ struct{}) error {
		s := &sessions{archive: claudesessions.Open(o.Dir), log: log}
		for _, tool := range s.tools() {
			plugin.Add(ctx, tool)
		}
		for name, doing := range map[string]string{
			Sessions: "listing the Claude Code sessions",
			Search:   "searching the Claude Code sessions",
			Read:     "reading a Claude Code session",
			Calls:    "looking at the calls of the Claude Code sessions",
		} {
			plugin.Add(ctx, plugin.ToolView{Tool: name, Show: func(json.RawMessage) string { return doing }, Detail: assistant.HowItEnded})
		}
		return nil
	})
}

// sessions is the transcripts and the log of the calls that read them.
type sessions struct {
	archive *claudesessions.Archive
	log     func(format string, args ...any)
}

// about is what every tool says of the transcripts. It is the same in every
// conversation, so that the prompt stays as it is for the cache.
const about = "Claude Code keeps a transcript of every session it ran on this machine, " +
	"below ~/.claude/projects: what was asked and answered, and every tool it called with its arguments and what came back. "

// hint is what the tools that show what a transcript holds say of it.
const hint = "What a session holds may be outdated: take it as a hint and check the code it speaks of."

type sessionArgs struct {
	Project string `json:"project"`
	Since   string `json:"since"`
	Until   string `json:"until"`
	Limit   int    `json:"limit"`
}

type searchArgs struct {
	Query   string `json:"query"`
	Project string `json:"project"`
	Role    string `json:"role"`
	Tool    string `json:"tool"`
	Since   string `json:"since"`
	Until   string `json:"until"`
	Limit   int    `json:"limit"`
}

type readArgs struct {
	Session string `json:"session"`
	From    int    `json:"from"`
	Count   int    `json:"count"`
	Offset  int    `json:"offset"`
	Only    string `json:"only"`
	Since   string `json:"since"`
	Until   string `json:"until"`
}

type callArgs struct {
	Session string `json:"session"`
	Tool    string `json:"tool"`
	Path    string `json:"path"`
	Project string `json:"project"`
	Since   string `json:"since"`
	Until   string `json:"until"`
	Limit   int    `json:"limit"`
}

// only is the kinds of record Read can keep.
const only = `"user","assistant","tool","result","all"`

func (s *sessions) tools() []plugin.Tool {
	return []plugin.Tool{
		{
			Name: Sessions,
			Description: about + fmt.Sprintf("List the sessions Claude Code ran on this machine, the most recently written first: "+
				"when each ran, the working directory and the first thing that was asked, with as much of its ID as names it. "+
				"At most %d; narrow with project and since.", claudesessions.MaxSessions),
			Schema: `{"type":"object","properties":{` +
				`"project":{"type":"string","description":"only sessions of a project: a part of its path, like the name of the directory"},` +
				`"since":{"type":"string","description":"only sessions written at or after this day, 2006-01-02"},` +
				`"until":{"type":"string","description":"only sessions written at or before this day, 2006-01-02"},` +
				`"limit":{"type":"integer","description":"how many sessions at most, ` + strconv.Itoa(claudesessions.DefaultSessions) + ` if left out"}}}`,
			Run: run(Sessions, func(ctx context.Context, args sessionArgs) (string, error) {
				if err := owned(ctx); err != nil {
					return "", err
				}
				since, until, err := moment(args.Since, args.Until)
				if err != nil {
					return "", err
				}
				out, listed, err := s.archive.Sessions(claudesessions.Filter{Project: args.Project, Since: since, Until: until, Limit: args.Limit})
				s.log("%s: %d sessions%s", Sessions, listed, failed(err))
				return out, err
			}),
		},
		{
			Name: Search,
			Description: about + fmt.Sprintf("Search the transcripts for a text, ignoring case: what was said, and the commands, files "+
				"and outputs of the tools that were called. Returns per hit the session, the time, the record number and an excerpt; "+
				"read around a hit with %s. At most %d hits. Narrow with project, role, tool, since and until. ", Read, claudesessions.MaxHits) + hint,
			Schema: `{"type":"object","properties":{` +
				`"query":{"type":"string","description":"the text to find"},` +
				`"project":{"type":"string","description":"only sessions of a project: a part of its path, like the name of the directory"},` +
				`"role":{"type":"string","enum":["user","assistant","any"],"description":"whose records to look at: what was asked, what was answered, or any (the default)"},` +
				`"tool":{"type":"string","description":"only records of one tool, like Bash or Edit: its calls and what they returned"},` +
				`"since":{"type":"string","description":"only records from this day on, 2006-01-02"},` +
				`"until":{"type":"string","description":"only records up to this day, 2006-01-02"},` +
				`"limit":{"type":"integer","description":"how many hits at most, ` + strconv.Itoa(claudesessions.DefaultHits) + ` if left out"}},` +
				`"required":["query"]}`,
			Run: run(Search, func(ctx context.Context, args searchArgs) (string, error) {
				if err := owned(ctx); err != nil {
					return "", err
				}
				since, until, err := moment(args.Since, args.Until)
				if err != nil {
					return "", err
				}
				out, hits, err := s.archive.Search(claudesessions.Query{Text: args.Query, Project: args.Project, Role: args.Role,
					Tool: args.Tool, Since: since, Until: until, Limit: args.Limit})
				s.log("%s: %d hits%s", Search, hits, failed(err))
				return out, err
			}),
		},
		{
			Name: Read,
			Description: fmt.Sprintf("Read the records of a Claude Code session in order, from a record number on, as one record per line "+
				"of the transcript: at most %d records and %d KiB per call, with a note at the end that says where to continue. "+
				"Each record comes with its time; a call of a tool and what it returned are records of their own. "+
				"A long record comes in pieces: continue with from and offset from the note. "+
				"Narrow with since and until to the records of a day, which answers what was done on that day. ",
				claudesessions.MaxRecords, claudesessions.MaxReadBytes>>10) + hint,
			Schema: `{"type":"object","properties":{` +
				`"session":{"type":"string","description":"the session, as ` + Sessions + ` names it: its ID or as much of it as names one session"},` +
				`"from":{"type":"integer","description":"the record to begin with, 1 if left out; negative counts from the end: -20 reads the last 20 records"},` +
				`"count":{"type":"integer","description":"how many records at most, ` + strconv.Itoa(claudesessions.DefaultRecords) + ` if left out"},` +
				`"offset":{"type":"integer","description":"bytes to skip in the first record, from the continuation note; 0 if left out"},` +
				`"since":{"type":"string","description":"only records from this day on, 2006-01-02"},` +
				`"until":{"type":"string","description":"only records up to this day, 2006-01-02"},` +
				`"only":{"type":"string","enum":[` + only + `],"description":"which records: all (the default), what the user asked, what the assistant answered and thought, the calls of tools, or what the tools returned"}},` +
				`"required":["session"]}`,
			Run: run(Read, func(ctx context.Context, args readArgs) (string, error) {
				if err := owned(ctx); err != nil {
					return "", err
				}
				since, until, err := moment(args.Since, args.Until)
				if err != nil {
					return "", err
				}
				out, err := s.archive.Read(claudesessions.Read{Session: args.Session, From: args.From, Count: args.Count,
					Offset: args.Offset, Only: args.Only, Since: since, Until: until})
				s.log("%s%s", Read, failed(err))
				return out, err
			}),
		},
		{
			Name: Calls,
			Description: about + fmt.Sprintf("List the calls of tools in the transcripts, the most recent first: the tool, when, "+
				"its command or file, what it returned and whether it failed. Filter by path to find the sessions that touched a file, by tool for one tool, "+
				"and by session for one session. At most %d calls.", claudesessions.MaxCalls),
			Schema: `{"type":"object","properties":{` +
				`"session":{"type":"string","description":"only the calls of one session, as ` + Sessions + ` names it"},` +
				`"tool":{"type":"string","description":"only the calls of one tool, like Bash or Edit"},` +
				`"path":{"type":"string","description":"only calls that touched a file whose path holds this, like main.go"},` +
				`"project":{"type":"string","description":"only sessions of a project: a part of its path, like the name of the directory"},` +
				`"since":{"type":"string","description":"only calls from this day on, 2006-01-02"},` +
				`"until":{"type":"string","description":"only calls up to this day, 2006-01-02"},` +
				`"limit":{"type":"integer","description":"how many calls at most, ` + strconv.Itoa(claudesessions.DefaultCalls) + ` if left out"}}}`,
			Run: run(Calls, func(ctx context.Context, args callArgs) (string, error) {
				if err := owned(ctx); err != nil {
					return "", err
				}
				since, until, err := moment(args.Since, args.Until)
				if err != nil {
					return "", err
				}
				out, listed, err := s.archive.ToolCalls(claudesessions.Calls{Session: args.Session, Tool: args.Tool, Path: args.Path,
					Project: args.Project, Since: since, Until: until, Limit: args.Limit})
				s.log("%s: %d calls%s", Calls, listed, failed(err))
				return out, err
			}),
		},
	}
}

// owned refuses a peer: the transcripts are the owner's own work.
func owned(ctx context.Context) error {
	if _, peer := plugin.OriginFrom(ctx); peer {
		return errPeer
	}
	return nil
}

// moment reads the two ends of a time range as the owner writes them: a day,
// or a day with a time, or RFC 3339. An empty one is no bound; until a day
// alone means the end of that day.
func moment(since, until string) (time.Time, time.Time, error) {
	from, err := day(since, false)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := day(until, true)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return from, to, nil
}

// day reads one end of a time range.
func day(s string, end bool) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04", time.RFC3339} {
		t, err := time.ParseInLocation(layout, s, time.Local)
		if err != nil {
			continue
		}
		if end && layout == "2006-01-02" {
			t = t.Add(24*time.Hour - time.Nanosecond)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%q is no date: give 2006-01-02, 2006-01-02 15:04 or RFC 3339", s)
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

// errPeer is what a peer is told: the transcripts are the owner's own work.
var errPeer = errors.New("the Claude Code sessions are the work of the owner of this node, and only the owner reads them")
