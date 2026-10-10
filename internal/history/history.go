// Package history keeps every conversation of the agents in full, on disk,
// for the agent to search and read again: what the memory of a conversation
// cuts off, and the conversations before /new.
//
// An archive is a directory with one directory per party: owner for the
// conversations of the owner, and peer-<fingerprint> for those of each peer,
// named by its authenticated key. In it every conversation is one file of
// JSON lines, a Record each, appended to after every answer. Who reads finds
// only what is in scope for it: the owner its current conversation or all of
// its own, a peer its current conversation and nothing else.
//
// A conversation may have a title besides, named for its topic (see
// SetTitle), and an effort of its own (see SetEffort): each is kept in a
// file of its own next to it. The title names it in place of its first
// message.
//
// Nothing is deleted by the archive itself: the owner deletes a session with
// Delete.
package history

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"ouroboros/internal/openai"
)

// The bounds of what the tools give back.
const (
	// MaxHits is how many hits one search returns at most, DefaultHits how
	// many without a limit.
	MaxHits     = 50
	DefaultHits = 20
	// MaxRecords is how many records one read returns at most,
	// DefaultRecords how many without a count.
	MaxRecords     = 200
	DefaultRecords = 20
	// MaxSessions is how many sessions a listing names at most.
	MaxSessions = 50

	maxSearchBytes = 16 << 10 // of the hits of one search
	maxReadBytes   = 32 << 10 // of the records of one read
	excerpt        = 80       // bytes on each side of a hit
	maxTitle       = 80       // bytes of the first message that names a session
	cutNote        = 100      // bytes left for the note on a record that is cut
)

// The parties of an archive: the directory of the owner, and the start of
// the directory of each peer.
const (
	ownerDir     = "owner"
	peerPrefix   = "peer-"
	suffix       = ".jsonl"
	titleSuffix  = ".title"
	effortSuffix = ".effort"
)

// The outcomes of an answer, in the record that ends it.
const (
	Answered = "answered"
	Failed   = "failed"
	Canceled = "canceled"
)

// Record is one line of the archive of a conversation.
type Record struct {
	Seq    int       `json:"seq"`    // within the conversation, from 1
	Answer int       `json:"answer"` // the answer it belongs to, from 1
	Time   time.Time `json:"time"`   // when the answer ended
	// Role is that of the message: user, assistant or tool, and end for the
	// record that closes an answer with its Outcome.
	Role       string `json:"role"`
	Content    string `json:"content,omitempty"`
	ToolCalls  []Call `json:"tool_calls,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Outcome is Answered, Failed or Canceled. Why an answer failed is not
	// kept: a peer reads its archive, and must not learn the cause from it.
	Outcome string `json:"outcome,omitempty"`
	// Statistics preserves the last reported usage across process restarts.
	Statistics *Statistics `json:"statistics,omitempty"`
}

// Statistics is the context size and provider usage reported when an answer ended.
// Cache is JSON so the archive need not depend on the assistant.
type Statistics struct {
	Context int             `json:"context,omitempty"`
	Cache   json.RawMessage `json:"cache,omitempty"`
}

// Call is a call of a tool the model asked for.
type Call struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Who is the party a conversation belongs to and the conversation it is in:
// the owner, or the peer with the authenticated key Peer.
type Who struct {
	Session string // the ID of the conversation
	Peer    string // the public key of the peer, "" for the owner
}

// Archive is the directory the conversations are kept in.
type Archive struct {
	dir string
	now func() time.Time

	mu    sync.Mutex
	known map[string]position // by path, where its file ended the last time
}

// position is how far a file is known: its size, and the last sequence
// number and answer in it.
type position struct {
	size        int64
	seq, answer int
}

// Open returns the archive in dir, which is created when something is kept.
func Open(dir string) *Archive {
	return &Archive{dir: dir, now: time.Now, known: map[string]position{}}
}

// named is what a session name may be: a context ID as ouro makes them, of
// a UUID or a hash. Anything else is hashed before it names a file.
var named = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Name is the name of the file of the conversation id, and of the session
// in what the archive lists: id itself when it is a context ID as ouro makes
// them, and a hash of it otherwise.
func Name(id string) string {
	if named.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:16])
}

// partyDir is the directory of the party of who.
func (a *Archive) partyDir(who Who) string {
	if who.Peer == "" {
		return filepath.Join(a.dir, ownerDir)
	}
	return filepath.Join(a.dir, Party(who.Peer))
}

// Party is how the archive names the peer with the public key publicKey: as
// the start of the ID of its conversations in List.
func Party(publicKey string) string {
	sum := sha256.Sum256([]byte(publicKey))
	return peerPrefix + hex.EncodeToString(sum[:16])
}

func (a *Archive) file(who Who, session string) string {
	return filepath.Join(a.partyDir(who), session+suffix)
}

// flock locks a file and stat looks at it; tests replace them to make them
// fail.
var (
	flock = syscall.Flock
	stat  = (*os.File).Stat
)

// Append keeps the messages of one answer of the conversation of who, and
// how it ended: err is the error of the answer. A conversation that goes on,
// also in another process, goes on in its file.
func (a *Archive) Append(who Who, msgs []openai.Message, err error, statistics ...Statistics) error {
	dir := a.partyDir(who)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := a.file(who, Name(who.Session))
	f, err2 := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err2 != nil {
		return err2
	}
	defer f.Close()
	if err := flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	info, err2 := stat(f)
	if err2 != nil {
		return err2
	}
	a.mu.Lock()
	at, ok := a.known[path]
	a.mu.Unlock()
	var out bytes.Buffer
	if !ok || at.size != info.Size() {
		// Another process went on with it, or this one does not know it yet.
		s, err := scan(f)
		if err != nil {
			return err
		}
		if !s.whole {
			out.WriteByte('\n') // a line that was cut off ends here
		}
		at = s.position
	}
	answer, now := at.answer+1, a.now().UTC()
	seq := at.seq
	add := func(r Record) {
		seq++
		r.Seq, r.Answer, r.Time = seq, answer, now
		b, _ := json.Marshal(r) // texts: cannot fail
		out.Write(append(b, '\n'))
	}
	for _, m := range msgs {
		r := Record{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, c := range m.ToolCalls {
			r.ToolCalls = append(r.ToolCalls, Call{c.ID, c.Function.Name, c.Function.Arguments})
		}
		add(r)
	}
	end := Record{Role: "end", Outcome: outcome(err)}
	if len(statistics) > 0 {
		end.Statistics = &statistics[0]
	}
	add(end)
	if _, err := f.Write(out.Bytes()); err != nil {
		a.forget(path)
		return err
	}
	a.mu.Lock()
	a.known[path] = position{info.Size() + int64(out.Len()), seq, answer}
	a.mu.Unlock()
	return nil
}

// outcome is how an answer with the error err ended.
func outcome(err error) string {
	switch {
	case err == nil:
		return Answered
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return Canceled
	}
	return Failed
}

func (a *Archive) forget(path string) {
	a.mu.Lock()
	delete(a.known, path)
	a.mu.Unlock()
}

// scanned is how far a file goes, and whether it ends with a whole line.
type scanned struct {
	position
	whole bool
}

// scan reads where the file f ends: its size, and the last sequence number
// and answer in it. A line that is no record is passed over.
func scan(f *os.File) (scanned, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return scanned{}, err
	}
	at := scanned{whole: true}
	err := lines(f, func(line []byte) bool {
		at.size += int64(len(line))
		at.whole = bytes.HasSuffix(line, []byte("\n"))
		var r Record
		if json.Unmarshal(line, &r) == nil {
			at.seq, at.answer = max(at.seq, r.Seq), max(at.answer, r.Answer)
		}
		return true
	})
	return at, err
}

// lines hands each line of r to each, with its line break, until each
// returns false.
func lines(r io.Reader, each func(line []byte) bool) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && !each(line) {
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

// records hands each record of the file at path to each, until it returns
// false.
func records(path string, each func(Record) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return lines(f, func(line []byte) bool {
		var r Record
		if json.Unmarshal(line, &r) != nil || r.Seq == 0 {
			return true
		}
		return each(r)
	})
}

// ErrScope is the error of a peer that names another conversation than its
// current one, or all of them.
var ErrScope = errors.New("a peer can only reach the archive of the conversation it is in")

// session is a conversation in the archive, as it is listed.
type session struct {
	name     string
	path     string
	modified time.Time
	size     int64
}

// sessions are the sessions of the party of who, the most recently changed
// first; for a peer only its current one.
func (a *Archive) sessions(who Who) ([]session, error) {
	if who.Peer != "" {
		name := Name(who.Session)
		path := a.file(who, name)
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return []session{{name, path, info.ModTime(), info.Size()}}, nil
	}
	return list(a.partyDir(who))
}

// list returns the sessions in dir, the most recently changed first.
func list(dir string) ([]session, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []session
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), suffix)
		if !ok || !e.Type().IsRegular() || !named.MatchString(name) {
			continue
		}
		s := session{name: name, path: filepath.Join(dir, e.Name())}
		if info, err := e.Info(); err == nil { // not if it was deleted meanwhile
			s.modified, s.size = info.ModTime(), info.Size()
		}
		out = append(out, s)
	}
	slices.SortStableFunc(out, func(x, y session) int {
		return cmp.Or(y.modified.Compare(x.modified), strings.Compare(x.name, y.name))
	})
	return out, nil
}

// when is how a time is shown.
func when(t time.Time) string { return t.Local().Format("2006-01-02 15:04") }

// Search returns the records in scope that contain query, ignoring case: of
// the current conversation of who, or with all of every conversation of the
// owner, the current one first and then the most recent. A peer may not ask
// for all. It returns at most limit hits (DefaultHits for 0, at most
// MaxHits) and how many it returned.
func (a *Archive) Search(who Who, query string, all bool, limit int) (string, int, error) {
	if strings.TrimSpace(query) == "" {
		return "", 0, errors.New("search needs a query")
	}
	if all && who.Peer != "" {
		return "", 0, ErrScope
	}
	if limit < 1 {
		limit = DefaultHits
	}
	limit = min(limit, MaxHits)
	current := Name(who.Session)
	var in []session
	if all {
		listed, err := a.sessions(who)
		if err != nil {
			return "", 0, err
		}
		in = listed
		if i := slices.IndexFunc(in, func(s session) bool { return s.name == current }); i > 0 {
			in = append(append([]session{in[i]}, in[:i]...), in[i+1:]...)
		}
	} else {
		in = []session{{name: current, path: a.file(who, current)}}
	}
	needle := strings.ToLower(query)
	var out strings.Builder
	hits, more := 0, false
	for _, s := range in {
		err := records(s.path, func(r Record) bool {
			found, ok := match(r, needle)
			if !ok {
				return true
			}
			line := fmt.Sprintf("%s · %s · #%d %s: %s\n", label(s.name, current), when(r.Time), r.Seq, r.Role, found)
			if hits == limit || out.Len()+len(line) > maxSearchBytes {
				more = true
				return false
			}
			out.WriteString(line)
			hits++
			return true
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", 0, err
		}
		if more {
			break
		}
	}
	scope := "this conversation"
	if all {
		scope = count(len(in), "conversation")
	}
	if hits == 0 {
		return fmt.Sprintf("no record of %s contains %q", scope, query), 0, nil
	}
	if more {
		fmt.Fprintf(&out, "[more hits in %s are not shown: search for something more precise]", scope)
	} else {
		fmt.Fprintf(&out, "[%s in %s]", count(hits, "hit"), scope)
	}
	return out.String(), hits, nil
}

// count is n things.
func count(n int, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return fmt.Sprintf("%d %ss", n, thing)
}

// label names a session in a hit or a listing.
func label(name, current string) string {
	if name == current {
		return name + " (this conversation)"
	}
	return name
}

// match returns an excerpt of r around needle, which is lower case, and
// whether r contains it: in its content, or in the name or the arguments of
// a call.
func match(r Record, needle string) (string, bool) {
	texts := []string{r.Content}
	for _, c := range r.ToolCalls {
		texts = append(texts, c.Name+" "+c.Arguments)
	}
	for _, text := range texts {
		lower := strings.ToLower(text)
		i := strings.Index(lower, needle)
		if i < 0 {
			continue
		}
		if len(lower) != len(text) {
			text = lower // the positions are those of the lower case
		}
		from, to := max(i-excerpt, 0), min(i+len(needle)+excerpt, len(text))
		cut := oneLine(strings.ToValidUTF8(text[from:to], ""))
		if from > 0 {
			cut = "…" + cut
		}
		if to < len(text) {
			cut += "…"
		}
		return cut, true
	}
	return "", false
}

// oneLine is text on one line.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// Read returns the records of the conversation session of the party of who,
// from the sequence number from on (1 for 0): count of them (DefaultRecords
// for 0, at most MaxRecords) and at most maxReadBytes, with a note where to
// go on. Without session it is the current conversation; a peer may name no
// other.
func (a *Archive) Read(who Who, session string, from, count int) (string, error) {
	current := Name(who.Session)
	if session == "" {
		session = current
	}
	if who.Peer != "" && session != current {
		return "", ErrScope
	}
	if !named.MatchString(session) {
		return "", fmt.Errorf("there is no session %q: history_sessions lists them", session)
	}
	from = max(from, 1)
	if count < 1 {
		count = DefaultRecords
	}
	count = min(count, MaxRecords)
	var out strings.Builder
	first, last, total, full := 0, 0, 0, false
	err := records(a.file(who, session), func(r Record) bool {
		total = r.Seq // the records after those returned are counted on
		if r.Seq < from || full {
			return true
		}
		text := show(r)
		if len(text) > maxReadBytes-out.Len() {
			if first > 0 {
				full = true // for the next read
				return true
			}
			// A record that does not fit alone is cut.
			keep := maxReadBytes - cutNote
			text = strings.ToValidUTF8(text[:keep], "") + fmt.Sprintf("\n[cut: %d more bytes of this record]\n", len(text)-keep)
		}
		if first == 0 {
			first = r.Seq
		}
		last = r.Seq
		out.WriteString(text)
		full = last-first+1 == count
		return true
	})
	if errors.Is(err, fs.ErrNotExist) {
		if session == current {
			return "nothing of this conversation is archived yet", nil
		}
		return "", fmt.Errorf("there is no session %q: history_sessions lists them", session)
	}
	if err != nil {
		return "", err
	}
	if first == 0 {
		return fmt.Sprintf("the session has %d records: there is none from %d on", total, from), nil
	}
	if last < total {
		fmt.Fprintf(&out, "[records %d to %d of %d: continue with from %d]", first, last, total, last+1)
	} else {
		fmt.Fprintf(&out, "[records %d to %d of %d]", first, last, total)
	}
	return out.String(), nil
}

// show is a record as Read gives it back.
func show(r Record) string {
	head := fmt.Sprintf("#%d · answer %d · %s · ", r.Seq, r.Answer, when(r.Time))
	switch r.Role {
	case "end":
		return head + "the answer " + r.Outcome + "\n"
	case "tool":
		return head + "tool result\n" + r.Content + "\n"
	}
	text := head + r.Role + "\n"
	if r.Content != "" {
		text += r.Content + "\n"
	}
	for _, c := range r.ToolCalls {
		text += "→ " + c.Name + " " + c.Arguments + "\n"
	}
	return text
}

// Sessions lists the conversations in the scope of who, the most recent
// first: for the owner all of its own, for a peer its current one. It
// returns how many it listed.
func (a *Archive) Sessions(who Who) (string, int, error) {
	in, err := a.sessions(who)
	if err != nil {
		return "", 0, err
	}
	if len(in) == 0 {
		return "no conversation is archived yet", 0, nil
	}
	current := Name(who.Session)
	var out strings.Builder
	shown := min(len(in), MaxSessions)
	for _, s := range in[:shown] {
		// A line is bounded by the title, so the listing is by MaxSessions.
		started, title := describe(s.path)
		fmt.Fprintf(&out, "%s · started %s · last %s · %q\n", label(s.name, current), started, when(s.modified), title)
	}
	if shown < len(in) {
		fmt.Fprintf(&out, "[%s more, older, not shown]", count(len(in)-shown, "session"))
	} else {
		fmt.Fprintf(&out, "[%s]", count(shown, "session"))
	}
	return out.String(), shown, nil
}

// describe returns when the conversation at path started and its title: the
// one it was given, or else its first message of the user, as far as they are
// there.
func describe(path string) (started, title string) {
	if b, err := os.ReadFile(titled(path)); err == nil {
		title = string(b)
	}
	records(path, func(r Record) bool {
		if started == "" {
			started = when(r.Time)
		}
		if title != "" || r.Role != "user" {
			return title == ""
		}
		title = oneLine(r.Content)
		if len(title) > maxTitle {
			title = strings.ToValidUTF8(title[:maxTitle], "") + "…"
		}
		return false
	})
	return started, title
}

// Entry is a conversation in the archive as the owner sees it to delete it:
// ID names it for Delete, as owner/<session> or peer-<fingerprint>/<session>.
type Entry struct {
	ID       string
	Modified time.Time
	Size     int64
	Title    string
}

// List returns every conversation in the archive, of the owner and of every
// peer, the most recent first.
func (a *Archive) List() ([]Entry, error) {
	parties, err := os.ReadDir(a.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, p := range parties {
		if !p.IsDir() || p.Name() != ownerDir && !strings.HasPrefix(p.Name(), peerPrefix) {
			continue
		}
		in, err := list(filepath.Join(a.dir, p.Name()))
		if err != nil {
			return nil, err
		}
		for _, s := range in {
			_, title := describe(s.path)
			out = append(out, Entry{p.Name() + "/" + s.name, s.modified, s.size, title})
		}
	}
	slices.SortStableFunc(out, func(x, y Entry) int { return y.Modified.Compare(x.Modified) })
	return out, nil
}

// ErrNotArchived is the error of Delete for a session the archive does not
// have.
var ErrNotArchived = errors.New("the archive has no such session")

// party is what the directory of a party may be called.
var party = regexp.MustCompile(`^(` + ownerDir + `|` + peerPrefix + `[0-9a-f]{32})$`)

// Owner is the ID of the conversation session of the owner, as List names
// it.
func Owner(session string) string { return ownerDir + "/" + Name(session) }

// path is the file of the conversation id, as List names it.
func (a *Archive) path(id string) (string, error) {
	dir, name, ok := strings.Cut(id, "/")
	if !ok || !party.MatchString(dir) || !named.MatchString(name) {
		return "", fmt.Errorf("%q names no session: they are named owner/SESSION or peer-…/SESSION", id)
	}
	return filepath.Join(a.dir, dir, name+suffix), nil
}

// Delete removes the conversation id, as List names it, from the archive.
func (a *Archive) Delete(id string) error {
	path, err := a.path(id)
	if err != nil {
		return err
	}
	for _, beside := range []string{titleSuffix, effortSuffix} {
		if err := os.Remove(besides(path, beside)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.Remove(path); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotArchived, id)
	} else if err != nil {
		return err
	}
	a.forget(path)
	return nil
}

// besides is the file with the ending kind next to the conversation in the
// file at path, like its title.
func besides(path, kind string) string { return strings.TrimSuffix(path, suffix) + kind }

// titled is the file of the title of the conversation in the file at path.
func titled(path string) string { return besides(path, titleSuffix) }

// MaxTitle is how long a title SetTitle keeps may be, in bytes.
const MaxTitle = 80

// SetTitle gives the conversation id, as List names it, a title, which names
// it from now on in place of its first message: on one line, cut to
// MaxTitle bytes. The conversation needs not be archived yet.
func (a *Archive) SetTitle(id, title string) error {
	path, err := a.path(id)
	if err != nil {
		return err
	}
	title = oneLine(title)
	if len(title) > MaxTitle {
		title = strings.ToValidUTF8(title[:MaxTitle], "")
	}
	if title == "" {
		return errors.New("a title needs a text")
	}
	return write(titled(path), title)
}

// write writes text to the file at path, next to a conversation.
func write(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o600)
}

// Title returns the title the conversation id, as List names it, was given
// with SetTitle, "" without one.
func (a *Archive) Title(id string) (string, error) {
	title, _, err := a.beside(id, titleSuffix)
	return title, err
}

// beside returns what the file with the ending kind next to the
// conversation id says, and whether there is one.
func (a *Archive) beside(id, kind string) (string, bool, error) {
	path, err := a.path(id)
	if err != nil {
		return "", false, err
	}
	b, err := os.ReadFile(besides(path, kind))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	return string(b), err == nil, err
}

// SetEffort gives the conversation id, as List names it, an effort of its
// own, "" too: how much the model thinks in it. What an effort is, is not
// for the archive to know. The conversation needs not be archived yet.
func (a *Archive) SetEffort(id, effort string) error {
	path, err := a.path(id)
	if err != nil {
		return err
	}
	return write(besides(path, effortSuffix), effort)
}

// Effort returns the effort the conversation id, as List names it, was given
// with SetEffort, and whether it was given one.
func (a *Archive) Effort(id string) (string, bool, error) {
	return a.beside(id, effortSuffix)
}

// Records returns every record of the conversation id, as List names it, in
// order.
func (a *Archive) Records(id string) ([]Record, error) {
	path, err := a.path(id)
	if err != nil {
		return nil, err
	}
	var out []Record
	err = records(path, func(r Record) bool { out = append(out, r); return true })
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotArchived, id)
	}
	return out, err
}
