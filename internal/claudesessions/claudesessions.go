// Package claudesessions reads the transcripts that Claude Code keeps of its
// conversations on this machine, so that the agents can search them again:
// what was asked and answered, the tools that were called with their
// arguments, and what they returned.
//
// Claude Code writes one JSON object per line below ~/.claude/projects, one
// directory per project and one file per session, named by the ID of the
// session. The package only reads these files; Claude Code owns them.
package claudesessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxHits is how many hits one search gives at most.
	MaxHits = 50
	// DefaultHits is how many hits a search gives without a limit.
	DefaultHits = 20
	// MaxSessions is how many sessions one listing gives at most.
	MaxSessions = 50
	// DefaultSessions is how many sessions a listing gives without a limit.
	DefaultSessions = 20
	// MaxRecords is how many records one read gives at most.
	MaxRecords = 50
	// DefaultRecords is how many records a read gives without a count.
	DefaultRecords = 20
	// MaxCalls is how many tool calls one listing gives at most.
	MaxCalls = 100
	// DefaultCalls is how many tool calls a listing gives without a limit.
	DefaultCalls = 40
	// MaxReadBytes is how much text one read gives at most, the note at the
	// end that says where to continue included.
	MaxReadBytes = 24 << 10

	// maxSearchBytes bounds what one search writes.
	maxSearchBytes = 48 << 10
	// maxArgs is how much of the arguments of a call is kept for searching.
	maxArgs = 2 << 10
	// maxOutput is how much of what a call returned is kept for searching.
	maxOutput = 8 << 10
	// maxFirst is how much of the first question a listing shows.
	maxFirst = 160
	// maxResult is how much of what a call returned a listing of calls shows.
	maxResult = 160
	// maxExcerpt is how much around a hit a search shows.
	maxExcerpt = 160
	// maxHeadLines is how many lines of a file are read for what a session
	// says of itself at its start.
	maxHeadLines = 40
)

// The kinds of a record.
const (
	KindText     = "text"
	KindThinking = "thinking"
	KindTool     = "tool"
	KindResult   = "result"
)

// Record is one thing in a session: what someone said, what the assistant
// thought or answered, a call of a tool, or what the tool returned.
type Record struct {
	Seq    int       // 1-based, in the order the session holds them
	Time   time.Time // zero when the line has no time
	Role   string    // user or assistant
	Kind   string    // one of the kinds above
	Tool   string    // the tool of a call or of what it returned
	Path   string    // the file the call touched, if it names one
	ID     string    // the id of a call, to match what it returned
	Failed bool      // what a tool returned was an error
	Sub    bool      // from a subagent's sidechain
	Text   string
}

// Session is what a transcript says of itself: who asked what, and when.
type Session struct {
	ID      string    // the ID Claude Code gave the session
	Project string    // the working directory the session ran in, if it says
	Dir     string    // the directory Claude Code filed the project under
	Started time.Time // the first record that has a time
	Ended   time.Time // when the file was last written
	Size    int64     // the bytes of the transcript
	First   string    // the first thing that was asked
}

// Archive is the transcripts below one directory, as Claude Code keeps them.
type Archive struct{ dir string }

// Open reads the transcripts below dir, ~/.claude/projects on this machine.
func Open(dir string) *Archive { return &Archive{dir: dir} }

// source is one transcript and what its place in the directory says of it.
type source struct {
	path    string
	project string // the directory Claude Code filed the project under
	id      string // the ID of the session, the name of the file
	ended   time.Time
	size    int64
}

// sources are the transcripts, the most recently written first. A missing
// directory is no session at all, not an error.
func (a *Archive) sources() ([]source, error) {
	var out []source
	err := filepath.WalkDir(a.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == a.dir {
				return err
			}
			// A directory that cannot be read hides only its own sessions, as a
			// transcript that cannot be read is no session to list. WalkDir
			// reports no error of a file, only of reading a directory.
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // the file went away while the directory was read
		}
		out = append(out, source{
			path:    path,
			project: top(a.dir, path),
			id:      strings.TrimSuffix(filepath.Base(path), ".jsonl"),
			ended:   last(path, info.Size(), info.ModTime()),
			size:    info.Size(),
		})
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	slices.SortFunc(out, func(a, b source) int { return b.ended.Compare(a.ended) })
	return out, nil
}

// top is the directory below root that holds the transcript.
func top(root, path string) string {
	// WalkDir found path below root, so the two are related.
	rel, _ := filepath.Rel(root, path)
	if i := strings.IndexRune(rel, filepath.Separator); i > 0 {
		return rel[:i]
	}
	return filepath.Base(filepath.Dir(path))
}

// resolve finds the transcript of a session by its ID, or by as much of it as
// names only one session.
func (a *Archive) resolve(name string) (source, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return source{}, errors.New("name the session to read: claude_sessions lists them")
	}
	in, err := a.sources()
	if err != nil {
		return source{}, err
	}
	var found []source
	for _, s := range in {
		if s.id == name {
			return s, nil
		}
		if strings.HasPrefix(s.id, name) {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return source{}, fmt.Errorf("there is no session %q: claude_sessions lists them", name)
	}
	return source{}, fmt.Errorf("%q names %d sessions: %s", name, len(found), strings.Join(distinct(found), ", "))
}

// Filter narrows a listing of sessions.
type Filter struct {
	Project string    // part of the working directory or of the directory of the project
	Since   time.Time // sessions written at or after it
	Until   time.Time // sessions begun at or before it
	Limit   int
}

// Sessions lists the transcripts, the most recently written first, and
// returns how many it listed.
func (a *Archive) Sessions(f Filter) (string, int, error) {
	in, err := a.sources()
	if err != nil {
		return "", 0, err
	}
	limit := f.Limit
	if limit < 1 {
		limit = DefaultSessions
	}
	limit = min(limit, MaxSessions)
	var found []source
	var metas []meta
	more := false
	for _, s := range in {
		if !hasProject(s.project, f.Project) {
			continue
		}
		if !f.Since.IsZero() && s.ended.Before(f.Since) {
			continue
		}
		m, err := head(s.path)
		if err != nil {
			continue // a transcript that cannot be read is no session to list
		}
		// A session ran over a range when it began before the range ended and
		// ended after it began: one that ran into the next day is still a
		// session of the day before.
		start := m.started
		if start.IsZero() {
			start = s.ended
		}
		if !f.Until.IsZero() && start.After(f.Until) {
			continue
		}
		if len(found) == limit {
			more = true
			break
		}
		found = append(found, s)
		metas = append(metas, m)
	}
	listed := len(found)
	if listed == 0 {
		return "no Claude Code session matches", 0, nil
	}
	names := distinct(found)
	var out strings.Builder
	for i, s := range found {
		where := metas[i].project
		if where == "" {
			where = s.project
		}
		fmt.Fprintf(&out, "%s%s · %s · %s", names[i], agentOf(s.id), span(metas[i].started, s.ended), where)
		// What was asked first may lie past the lines head reads.
		if first := oneLine(metas[i].first, maxFirst); first != "" {
			fmt.Fprintf(&out, " · %s", first)
		}
		out.WriteString("\n")
	}
	if more {
		fmt.Fprintf(&out, "[more sessions are not shown: narrow with project or since]")
	} else {
		fmt.Fprintf(&out, "[%s]", count(listed, "session"))
	}
	return out.String(), listed, nil
}

// meta is what a transcript says of itself at its start.
type meta struct {
	id      string
	project string
	started time.Time
	first   string
}

// head reads the start of a transcript: the ID, the working directory, when
// it began and what was asked first. It stops as soon as it knows them.
func head(path string) (meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return meta{}, err
	}
	defer f.Close()
	var m meta
	r := bufio.NewReaderSize(f, 1<<16)
	for i := 0; i < maxHeadLines; i++ {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var e entry
			if json.Unmarshal(bytes.TrimSpace(line), &e) == nil {
				if m.id == "" {
					m.id = e.SessionID
				}
				if m.project == "" {
					m.project = e.CWD
				}
				if m.started.IsZero() && e.Timestamp != "" {
					m.started, _ = time.Parse(time.RFC3339Nano, e.Timestamp)
				}
				if m.first == "" && e.Type == "user" {
					m.first = asked(e.Message.Content)
				}
			}
		}
		if err != nil {
			break
		}
		if m.id != "" && m.project != "" && !m.started.IsZero() && m.first != "" {
			break
		}
	}
	return m, nil
}

// asked is what a user line asked: its text, without the results of tools.
func asked(content json.RawMessage) string {
	content = bytes.TrimSpace(content)
	if len(content) == 0 {
		return ""
	}
	if content[0] == '"' {
		// A JSON string, as the line it is part of was decoded.
		var s string
		json.Unmarshal(content, &s)
		return strings.TrimSpace(s)
	}
	var blocks []block
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return strings.TrimSpace(b.Text)
		}
	}
	return ""
}

// Query is what to look for in the transcripts.
type Query struct {
	Text    string
	Project string
	Role    string // user, assistant or any
	Tool    string // only what a call of this tool said or returned
	Since   time.Time
	Until   time.Time
	Limit   int
}

// Search looks in the transcripts for a text, ignoring case, and returns
// what it found and how many hits it gave.
func (a *Archive) Search(q Query) (string, int, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return "", 0, errors.New("a search needs a text to look for")
	}
	role, err := roleOf(q.Role)
	if err != nil {
		return "", 0, err
	}
	limit := q.Limit
	if limit < 1 {
		limit = DefaultHits
	}
	limit = min(limit, MaxHits)
	in, err := a.sources()
	if err != nil {
		return "", 0, err
	}
	needles := needlesOf(text)
	tool := strings.TrimSpace(q.Tool)
	var out strings.Builder
	hits, more, searched := 0, false, 0
	for _, s := range in {
		// A session that ended before since holds nothing after it; one that
		// ended after until may still hold records before it.
		if !hasProject(s.project, q.Project) || !within(q.Since, time.Time{}, s.ended) {
			continue
		}
		searched++
		var names map[string]string // the tools of the calls, by id
		err := eachMatching(s.path, func(line []byte) bool {
			for _, needle := range needles {
				if containsFold(line, needle) {
					return true
				}
			}
			// The tool of what a call returned is known only from the call, which
			// need not hold the text: with a tool to filter by, every call is read.
			return tool != "" && bytes.Contains(line, []byte(`"tool_use"`))
		}, func(r Record) bool {
			if role != "" && r.Role != role {
				return true
			}
			if !within(q.Since, q.Until, r.Time) {
				return true
			}
			found := excerpt(r.Text, text)
			if found == "" {
				return true
			}
			if r.Kind == KindResult && r.Tool == "" && r.ID != "" {
				// Without the filter the calls are read only for a hit that needs
				// one, and then once for the session.
				if names == nil {
					names = callNames(s.path)
				}
				r.Tool = names[r.ID]
			}
			if tool != "" && !strings.EqualFold(r.Tool, tool) {
				return true
			}
			line := fmt.Sprintf("%s · %s · #%d %s%s · %s\n", short(s.id), when(r.Time), r.Seq, r.Role, suffix(r), found)
			if hits == limit || out.Len()+len(line) > maxSearchBytes {
				more = true
				return false
			}
			out.WriteString(line)
			hits++
			return true
		})
		if err != nil {
			// A transcript that cannot be read is no session searched, as it is
			// no session to list.
			searched--
			continue
		}
		if more {
			break
		}
	}
	if hits == 0 {
		return fmt.Sprintf("no record of %s contains %q", count(searched, "session"), text), 0, nil
	}
	if more {
		fmt.Fprintf(&out, "[more hits are not shown; searched %s: search for something more precise]", count(searched, "session"))
	} else {
		fmt.Fprintf(&out, "[%s in %s]", count(hits, "hit"), count(searched, "session"))
	}
	return out.String(), hits, nil
}

// Read says which records of a session to give.
type Read struct {
	Session string
	From    int       // the record to begin with; negative counts from the end
	Count   int       // how many records at most
	Offset  int       // bytes to skip in the first record
	Only    string    // user, assistant, tool or result; empty for all
	Since   time.Time // records at or after it
	Until   time.Time // records at or before it
}

// Read gives the records of a session in order, with a note at the end that
// says where to continue when they do not all fit.
func (a *Archive) Read(req Read) (string, error) {
	if req.Offset < 0 {
		return "", errors.New("offset must not be negative")
	}
	kind, err := only(req.Only)
	if err != nil {
		return "", err
	}
	windowed := !req.Since.IsZero() || !req.Until.IsZero()
	keep := func(r Record) bool { return kind(r) && within(req.Since, req.Until, r.Time) }
	// A transcript is written in order: after a record past until, nothing of
	// the range follows, and the rest of the file is not read.
	past := func(r Record) bool { return !req.Until.IsZero() && r.Time.After(req.Until) }
	src, err := a.resolve(req.Session)
	if err != nil {
		return "", err
	}
	count := req.Count
	if count < 1 {
		count = DefaultRecords
	}
	count = min(count, MaxRecords)
	from := req.From
	if from < 0 {
		// Counted from the end, over the records that Only keeps.
		seqs := make([]int, 0, 64)
		if err := each(src.path, func(r Record) bool {
			if past(r) {
				return false
			}
			if keep(r) {
				seqs = append(seqs, r.Seq)
			}
			return true
		}); err != nil {
			return "", err
		}
		i := len(seqs) + from
		from = 1
		if i >= 0 && i < len(seqs) {
			from = seqs[i]
		}
	}
	from = max(from, 1)
	var out strings.Builder
	first, last, total, shown, line, full := 0, 0, 0, 0, 0, false
	next := 0 // in a range: the record of it that the next read begins with
	nextOffset := 0
	var offsetErr error
	n := 0 // the line want is asked of: it sees every line but an empty end
	err = eachMatching(src.path, func(line []byte) bool {
		n++
		if !full {
			return true
		}
		if windowed {
			// The record of this line ends the read, and need not be counted.
			return true
		}
		// The records after those returned are only counted: a line of what
		// someone said is a record, and it is not decoded for that.
		if isMessage(line) {
			total = n
		}
		return false
	}, func(r Record) bool {
		if windowed && (full || past(r)) {
			return false
		}
		if full {
			total = r.Seq
			return true
		}
		total = r.Seq
		if !keep(r) || r.Seq < from {
			return true
		}
		if r.Seq != line {
			// Only whole records are given: those of one line stay together.
			if shown == count {
				full = true
				next = r.Seq
				return true
			}
			shown++
			line = r.Seq
		}
		text := show(r)
		if first == 0 {
			if req.Offset >= len(text) || !utf8.RuneStart(text[req.Offset]) {
				offsetErr = errors.New("offset must be within the first record, on a UTF-8 boundary")
				return false
			}
			text = text[req.Offset:]
			first = r.Seq
		}
		if len(text) > MaxReadBytes-readNote-out.Len() {
			if out.Len() > 0 {
				full = true // for the next read
				next = r.Seq
				return true
			}
			room := MaxReadBytes - readNote
			for room > 0 && !utf8.RuneStart(text[room]) {
				room--
			}
			nextOffset = req.Offset + room
			text = text[:room]
			full = true
		}
		last = r.Seq
		out.WriteString(text)
		return true
	})
	if err != nil {
		return "", err
	}
	if offsetErr != nil {
		return "", offsetErr
	}
	if first == 0 {
		if windowed {
			return fmt.Sprintf("no record of the session is in the range: there is none from %d on", from), nil
		}
		return fmt.Sprintf("the session has %d records: there is none from %d on", total, from), nil
	}
	if windowed {
		// The records of a range are not counted: its end is not read.
		if nextOffset > 0 {
			fmt.Fprintf(&out, "\n[records %d to %d of the range: continue with from %d, offset %d]", first, last, last, nextOffset)
		} else if next > 0 {
			fmt.Fprintf(&out, "\n[records %d to %d of the range: continue with from %d]", first, last, next)
		} else {
			fmt.Fprintf(&out, "\n[records %d to %d: the range ends here]", first, last)
		}
		return out.String(), nil
	}
	if nextOffset > 0 {
		fmt.Fprintf(&out, "\n[records %d to %d of %d: continue with from %d, offset %d]", first, last, total, last, nextOffset)
	} else if last < total {
		fmt.Fprintf(&out, "\n[records %d to %d of %d: continue with from %d]", first, last, total, last+1)
	} else {
		fmt.Fprintf(&out, "\n[records %d to %d of %d]", first, last, total)
	}
	return out.String(), nil
}

// readNote is how much room a note at the end of a read may need.
const readNote = 96

// isMessage says whether a line is what a user or the assistant said, without
// decoding it. What a tool returned holds such a text only escaped, so it does
// not count.
func isMessage(line []byte) bool {
	return bytes.Contains(line, []byte(`"type":"user"`)) || bytes.Contains(line, []byte(`"type":"assistant"`))
}

// Calls says which tool calls to list.
type Calls struct {
	Session string
	Tool    string
	Path    string // part of the file a call touched
	Project string
	Since   time.Time
	Until   time.Time
	Limit   int
}

// Call is one call of a tool in a session.
type Call struct {
	Session string
	Time    time.Time
	Tool    string
	Path    string
	Args    string
	Result  string
	Failed  bool
	Sub     bool
}

// ToolCalls lists the calls of tools in the transcripts, the most recent
// first, and returns how many it listed.
func (a *Archive) ToolCalls(q Calls) (string, int, error) {
	in, err := a.sources()
	if err != nil {
		return "", 0, err
	}
	if name := strings.TrimSpace(q.Session); name != "" {
		src, err := a.resolve(name)
		if err != nil {
			return "", 0, err
		}
		in = []source{src}
	}
	limit := q.Limit
	if limit < 1 {
		limit = DefaultCalls
	}
	limit = min(limit, MaxCalls)
	tool := strings.TrimSpace(q.Tool)
	var calls []Call
	oldest := time.Time{} // of the calls found so far
	for _, s := range in {
		// A session that ended before since holds nothing after it; one that
		// ended after until may still hold records before it.
		if !hasProject(s.project, q.Project) || !within(q.Since, time.Time{}, s.ended) {
			continue
		}
		// The transcripts are the most recently written first: when a call
		// is there for every place a listing has, what is older cannot
		// change it.
		if len(calls) >= limit && !oldest.IsZero() && s.ended.Before(oldest) {
			break
		}
		open := make(map[string]*Call)
		var found []*Call
		err := eachMatching(s.path, callsOf, func(r Record) bool {
			switch r.Kind {
			case KindTool:
				c := &Call{Session: short(s.id), Time: r.Time, Tool: r.Tool, Path: r.Path, Args: r.Text, Sub: r.Sub}
				if c.Time.IsZero() {
					// A call without a time is as old as its session, so that it
					// sorts among the others and the break above still holds.
					c.Time = s.ended
				}
				if r.ID != "" {
					open[r.ID] = c
				}
				found = append(found, c)
			case KindResult:
				if c, ok := open[r.ID]; ok {
					c.Failed = c.Failed || r.Failed
					c.Result = oneLine(r.Text, maxResult)
				}
			}
			return true
		})
		if err != nil {
			continue // a transcript that cannot be read is no session to list
		}
		for _, c := range found {
			if tool != "" && !strings.EqualFold(c.Tool, tool) {
				continue
			}
			if q.Path != "" && !strings.Contains(strings.ToLower(c.Path), strings.ToLower(q.Path)) {
				continue
			}
			if !within(q.Since, q.Until, c.Time) {
				continue
			}
			calls = append(calls, *c)
			if oldest.IsZero() || c.Time.Before(oldest) {
				oldest = c.Time
			}
		}
	}
	if len(calls) == 0 {
		return "no tool call matches", 0, nil
	}
	slices.SortStableFunc(calls, func(a, b Call) int { return b.Time.Compare(a.Time) })
	more := len(calls) > limit
	if more {
		calls = calls[:limit]
	}
	var out strings.Builder
	for _, c := range calls {
		what := c.Args
		if c.Path != "" && (what == "" || what == c.Path) {
			what = c.Path
		}
		fmt.Fprintf(&out, "%s · %s · %s%s%s · %s%s\n", c.Session, when(c.Time), c.Tool,
			marked(c.Failed, " failed"), marked(c.Sub, " (subagent)"), oneLine(what, maxExcerpt), returned(c.Result))
	}
	if more {
		fmt.Fprintf(&out, "[more calls are not shown: narrow with session, tool, path or since]")
	} else {
		fmt.Fprintf(&out, "[%s]", count(len(calls), "call"))
	}
	return out.String(), len(calls), nil
}

// marked is text when cond holds, and else nothing.
func marked(cond bool, text string) string {
	if cond {
		return text
	}
	return ""
}

// returned is what a call gave back, after the call.
func returned(s string) string {
	if s == "" {
		return ""
	}
	return " → " + s
}

// agentOf marks the transcript of a subagent Claude Code ran for a session.
func agentOf(id string) string {
	if strings.HasPrefix(id, "agent-") {
		return " (subagent)"
	}
	return ""
}

// last is when a transcript of length bytes last said something: the time of
// its last record, or when it was written when it holds none. Claude Code
// touches the file without writing to it, so the time of the file says
// nothing.
func last(path string, length int64, written time.Time) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return written
	}
	defer f.Close()
	const window = 64 << 10
	size := min(length, window)
	buf := make([]byte, size)
	if _, err := f.ReadAt(buf, length-size); err != nil && !errors.Is(err, io.EOF) {
		return written
	}
	const mark = `"timestamp":"`
	i := bytes.LastIndex(buf, []byte(mark))
	if i < 0 {
		return written
	}
	rest := buf[i+len(mark):]
	j := bytes.IndexByte(rest, '"')
	if j <= 0 {
		return written
	}
	t, err := time.Parse(time.RFC3339Nano, string(rest[:j]))
	if err != nil {
		return written
	}
	return t
}

// distinct names the sessions so that each name tells one from the others: a
// session by the beginning of its ID, longer where that is not enough.
func distinct(in []source) []string {
	for width := 8; width < 36; width += 4 {
		names := make([]string, 0, len(in))
		seen := make(map[string]bool, len(in))
		clash := false
		for _, s := range in {
			name := s.id
			if len(name) > width {
				name = name[:width]
			}
			if seen[name] {
				clash = true
			}
			seen[name] = true
			names = append(names, name)
		}
		if !clash {
			return names
		}
	}
	names := make([]string, 0, len(in))
	for _, s := range in {
		names = append(names, s.id)
	}
	return names
}

// callsOf says whether a line can hold a call of a tool or what one
// returned: only these are read when the calls are listed.
func callsOf(line []byte) bool {
	return bytes.Contains(line, []byte(`"tool_use"`)) || bytes.Contains(line, []byte(`"tool_result"`))
}

// callNames is the tool of every call in a transcript, by the id of the call.
// A transcript that cannot be read names none.
func callNames(path string) map[string]string {
	names := make(map[string]string)
	_ = eachMatching(path, func(line []byte) bool {
		return bytes.Contains(line, []byte(`"tool_use"`))
	}, func(r Record) bool {
		if r.Kind == KindTool && r.ID != "" {
			names[r.ID] = r.Tool
		}
		return true
	})
	return names
}

// entry is a line of a transcript as Claude Code writes it.
type entry struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Sidechain bool   `json:"isSidechain"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// block is one part of a message.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// scan numbers the records of one transcript and remembers which tool a
// result belongs to. A record is numbered by the line of the transcript it
// stands on, so that the number of a hit is the same in a read, whatever the
// search had to look at.
type scan struct {
	tools map[string]Record // by the id of the call
}

// feed takes line n of a transcript and hands its records to fn, which says
// whether the next one is wanted too.
func (s *scan) feed(line []byte, n int, fn func(Record) bool) bool {
	line = bytes.TrimSpace(line)
	if len(line) < 2 || line[0] != '{' {
		return true
	}
	var e entry
	if err := json.Unmarshal(line, &e); err != nil {
		return true
	}
	if e.Type != "user" && e.Type != "assistant" {
		return true
	}
	content := bytes.TrimSpace(e.Message.Content)
	if len(content) == 0 {
		return true
	}
	when := time.Time{}
	if e.Timestamp != "" {
		when, _ = time.Parse(time.RFC3339Nano, e.Timestamp)
	}
	take := func(kind, text string) bool {
		return fn(Record{Seq: n, Time: when, Role: e.Type, Kind: kind, Sub: e.Sidechain, Text: text})
	}
	if content[0] == '"' {
		var text string
		if json.Unmarshal(content, &text) != nil || strings.TrimSpace(text) == "" {
			return true
		}
		return take(KindText, text)
	}
	var blocks []block
	if json.Unmarshal(content, &blocks) != nil {
		return true
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" && !take(KindText, b.Text) {
				return false
			}
		case "thinking":
			if strings.TrimSpace(b.Thinking) != "" && !take(KindThinking, b.Thinking) {
				return false
			}
		case "tool_use":
			call := Record{Seq: n, Time: when, Role: e.Type, Kind: KindTool, Tool: b.Name, Path: touched(b.Input), ID: b.ID, Sub: e.Sidechain, Text: args(b.Input)}
			if b.ID != "" {
				s.tools[b.ID] = call
			}
			if !fn(call) {
				return false
			}
		case "tool_result":
			made := s.tools[b.ToolUseID]
			out := Record{Seq: n, Time: when, Role: e.Type, Kind: KindResult, Tool: made.Tool, Path: made.Path,
				ID: b.ToolUseID, Failed: b.IsError, Sub: e.Sidechain, Text: flatten(b.Content)}
			if !fn(out) {
				return false
			}
		}
	}
	return true
}

// each hands every record of a transcript to fn, in order, until fn says
// that it wants no more.
func each(path string, fn func(Record) bool) error {
	return eachMatching(path, nil, fn)
}

// eachMatching is each, which first asks want of the line: a record of a
// line it refuses is not made at all.
func eachMatching(path string, want func([]byte) bool, fn func(Record) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	s := &scan{tools: make(map[string]Record)}
	for n := 1; ; n++ {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && (want == nil || want(line)) && !s.feed(line, n, fn) {
			return nil
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// touched is the file the arguments of a call name, if they name one.
func touched(input json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, key := range []string{"file_path", "notebook_path", "path"} {
		if s, ok := m[key].(string); ok {
			return s
		}
	}
	return ""
}

// args is what a call was given, as short as a person reads it: the command
// of Bash, the file of Edit, and else the JSON of the arguments.
func args(input json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(input, &m) != nil {
		return clip(string(input), maxArgs)
	}
	for _, key := range []string{"command", "file_path", "notebook_path", "path", "pattern", "url", "query", "description", "prompt", "name"} {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return clip(s, maxArgs)
		}
	}
	// The input decoded as an object, so it is JSON that compacts.
	var b bytes.Buffer
	json.Compact(&b, input)
	return clip(b.String(), maxArgs)
}

// flatten is what a tool returned, as text.
func flatten(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		// A JSON string, as the line it is part of was decoded.
		var s string
		json.Unmarshal(raw, &s)
		return clip(s, maxOutput)
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return clip(string(raw), maxOutput)
	}
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == "text" && bl.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(bl.Text)
		}
	}
	return clip(b.String(), maxOutput)
}

// show is a record as Read gives it back.
func show(r Record) string {
	head := "#" + strconv.Itoa(r.Seq) + " · " + when(r.Time) + " · " + r.Role
	switch r.Kind {
	case KindThinking:
		head += " · thinking"
	case KindTool:
		head += " · " + r.Tool
	case KindResult:
		head += " · " + r.Tool
		if r.Tool != "" {
			head += " result"
		}
		if r.Failed {
			head += " (failed)"
		}
	}
	if r.Sub {
		head += " · subagent"
	}
	return head + "\n" + r.Text + "\n"
}

// suffix names the tool of a hit that was a call or what one returned.
func suffix(r Record) string {
	switch r.Kind {
	case KindTool:
		return " · " + r.Tool
	case KindResult:
		return " · " + r.Tool + " result"
	case KindThinking:
		return " · thinking"
	}
	return ""
}

// only is what records Read keeps.
func only(what string) (func(Record) bool, error) {
	switch strings.ToLower(strings.TrimSpace(what)) {
	case "", "all":
		return func(Record) bool { return true }, nil
	case "user":
		return func(r Record) bool { return r.Role == "user" && r.Kind == KindText }, nil
	case "assistant":
		return func(r Record) bool { return r.Role == "assistant" && (r.Kind == KindText || r.Kind == KindThinking) }, nil
	case "tool":
		return func(r Record) bool { return r.Kind == KindTool }, nil
	case "result":
		return func(r Record) bool { return r.Kind == KindResult }, nil
	}
	return nil, fmt.Errorf("only is user, assistant, tool or result, not %q", what)
}

// roleOf checks the role a search is about.
func roleOf(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "any":
		return "", nil
	case "user", "assistant":
		return strings.ToLower(strings.TrimSpace(role)), nil
	}
	return "", fmt.Errorf("role is user, assistant or any, not %q", role)
}

// hasProject says whether the directory of a project holds what the filter
// names, ignoring case.
func hasProject(dir, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return true
	}
	return strings.Contains(strings.ToLower(dir), strings.ToLower(strings.ReplaceAll(want, "/", "-")))
}

// within says whether t is inside since and until, where a zero time is no
// bound and a record without a time is always inside.
func within(since, until, t time.Time) bool {
	if t.IsZero() {
		return true
	}
	if !since.IsZero() && t.Before(since) {
		return false
	}
	return !(!until.IsZero() && t.After(until))
}

// needlesOf is a text as it appears in a transcript: as it is, and with the
// characters that JSON escapes, each folded to lower case for the search.
func needlesOf(text string) [][]byte {
	out := [][]byte{[]byte(strings.ToLower(text))}
	if escaped, err := json.Marshal(text); err == nil && len(escaped) > 2 {
		inner := string(escaped[1 : len(escaped)-1])
		if inner != text {
			out = append(out, []byte(strings.ToLower(inner)))
		}
	}
	return out
}

// fold is the byte in lower case, for ASCII.
func fold(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 'a' - 'A'
	}
	return b
}

// containsFold says whether hay holds needle, ignoring case, where needle is
// already folded and not empty: a search has a text.
func containsFold(hay, needle []byte) bool {
	if len(needle) > len(hay) {
		return false
	}
	first, last := needle[0], len(hay)-len(needle)
	for i := 0; i <= last; i++ {
		if fold(hay[i]) != first {
			continue
		}
		j := 1
		for ; j < len(needle); j++ {
			if fold(hay[i+j]) != needle[j] {
				break
			}
		}
		if j == len(needle) {
			return true
		}
	}
	return false
}

// indexFold is where sub, which is not empty, appears in s, ignoring case, or
// -1.
func indexFold(s, sub string) int {
	needle := []byte(strings.ToLower(sub))
	if len(needle) > len(s) {
		return -1
	}
	first, last := needle[0], len(s)-len(needle)
	for i := 0; i <= last; i++ {
		if fold(s[i]) != first {
			continue
		}
		j := 1
		for ; j < len(needle); j++ {
			if fold(s[i+j]) != needle[j] {
				break
			}
		}
		if j == len(needle) {
			return i
		}
	}
	return -1
}

// excerpt is the part of text around the first place text holds query, one
// line and short, or "" when it holds none.
func excerpt(text, query string) string {
	i := indexFold(text, query)
	if i < 0 {
		return ""
	}
	start := max(i-80, 0)
	end := min(i+len(query)+80, len(text))
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	out := oneLine(text[start:end], maxExcerpt)
	switch {
	case start > 0 && end < len(text):
		return "…" + out + "…"
	case start > 0:
		return "…" + out
	case end < len(text):
		return out + "…"
	}
	return out
}

// oneLine is s with its whitespace as single spaces, at most n bytes long.
func oneLine(s string, n int) string {
	return clip(strings.Join(strings.Fields(s), " "), n)
}

// clip is s at most n bytes long, on a boundary of a rune.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// short is the beginning of a session ID, enough to name it again.
func short(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// when is a time as a person reads it, in the time of this machine.
func when(t time.Time) string {
	if t.IsZero() {
		return "no time"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// span is when a session ran.
func span(start, end time.Time) string {
	if start.IsZero() {
		return "until " + when(end)
	}
	if end.IsZero() || start.Format("2006-01-02") == end.Format("2006-01-02") {
		return when(start) + "–" + end.Local().Format("15:04")
	}
	return when(start) + " – " + when(end)
}

// count names how many of a thing there are.
func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}
