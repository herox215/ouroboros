// Package history keeps complete conversation archives and persistent session
// metadata in SQLite. Legacy JSONL archives are imported lazily, transactionally,
// without modifying the originals. Access remains scoped by authenticated party.
package history

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
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

	"time"
	"unicode/utf8"

	"ouroboros/internal/openai"
	"ouroboros/internal/storage"
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
	MaxReadBytes   = 16 << 10 // of one read, including its continuation note
	excerpt        = 80       // bytes on each side of a hit
	maxTitle       = 80       // bytes of the first message that names a session
	readNote       = 256      // bytes reserved for the continuation note
)

// The parties of an archive: the directory of the owner, and the start of
// the directory of each peer.
const (
	ownerDir        = "owner"
	peerPrefix      = "peer-"
	suffix          = ".jsonl"
	titleSuffix     = ".title"
	effortSuffix    = ".effort"
	modelSuffix     = ".model"
	directorySuffix = ".directory"
	goOnSuffix      = ".goon"
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
	Role           string               `json:"role"`
	Content        string               `json:"content,omitempty"`
	Source         openai.MessageSource `json:"source,omitzero"`
	ToolCalls      []Call               `json:"tool_calls,omitempty"`
	ToolCallID     string               `json:"tool_call_id,omitempty"`
	PresentationID string               `json:"presentation_id,omitempty"`
	// Outcome is Answered, Failed or Canceled. Why an answer failed is not
	// kept: a peer reads its archive, and must not learn the cause from it.
	Outcome string `json:"outcome,omitempty"`
	// Statistics preserves the last reported usage across process restarts.
	Statistics *Statistics `json:"statistics,omitempty"`
}

// Statistics is the context size and provider usage reported when an answer ended.
// Cache is JSON so the archive need not depend on the assistant.
type Statistics struct {
	// PresentationID belongs to the end record, not its usage statistics.
	PresentationID string          `json:"-"`
	Context        int             `json:"context,omitempty"`
	Cache          json.RawMessage `json:"cache,omitempty"`
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
	mu  sync.Mutex
	db  *sql.DB
	// open opens the database file. Tests replace it with a file that fails
	// where they need it to; Open sets it to storage.Open.
	open func(string) (*sql.DB, error)
}

// Open returns a lazy archive handle. Its first write (or read of an existing
// directory) opens SQLite in dir and imports any legacy archive. Reads of a
// missing directory do not create it. Operation errors include
// initialization failures; Open retains its historical no-error signature.
func Open(dir string) *Archive {
	return &Archive{dir: dir, now: time.Now, open: storage.Open}
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

// Append atomically keeps one complete answer, continuing the SQL sequence.
func (a *Archive) Append(who Who, msgs []openai.Message, answerErr error, statistics ...Statistics) error {
	return a.append(who, msgs, answerErr, "", statistics...)
}

// AppendWithPresentationID keeps an answer with an end presentation ID and no
// usage statistics. Use Append with Statistics when usage is available.
func (a *Archive) AppendWithPresentationID(who Who, msgs []openai.Message, answerErr error, presentationID string) error {
	return a.append(who, msgs, answerErr, presentationID)
}

func (a *Archive) append(who Who, msgs []openai.Message, answerErr error, presentationID string, statistics ...Statistics) error {
	db, err := a.database()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id := filepath.Base(a.partyDir(who)) + "/" + Name(who.Session)
	// A write before the read serializes concurrent appenders, also across handles.
	if _, err = tx.Exec("INSERT INTO history_sessions(id) VALUES(?) ON CONFLICT(id) DO UPDATE SET deleted=0", id); err != nil {
		return err
	}
	var seq, answer, ordinal int
	if err = tx.QueryRow("SELECT coalesce(max(seq),0),coalesce(max(answer),0),coalesce(max(ordinal),0) FROM history_records WHERE session_id=?", id).Scan(&seq, &answer, &ordinal); err != nil {
		return err
	}
	answer++
	now := a.now().UTC()
	var size int64
	add := func(r Record) error {
		seq++
		ordinal++
		r.Seq, r.Answer, r.Time = seq, answer, now
		b, e := json.Marshal(r)
		if e != nil {
			return e
		}
		size += int64(len(b) + 1)
		return insertRecord(tx, id, ordinal, r, b)
	}
	for _, m := range msgs {
		r := Record{Role: m.Role, Content: m.Content, Source: m.Source, ToolCallID: m.ToolCallID, PresentationID: m.PresentationID}
		for _, c := range m.ToolCalls {
			r.ToolCalls = append(r.ToolCalls, Call{c.ID, c.Function.Name, c.Function.Arguments})
		}
		if err = add(r); err != nil {
			return err
		}
	}
	end := Record{Role: "end", Outcome: outcome(answerErr), PresentationID: presentationID}
	if len(statistics) > 0 {
		end.PresentationID = statistics[0].PresentationID
		end.Statistics = &statistics[0]
	}
	if err = add(end); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE history_sessions SET archived=1,modified=?,size=size+? WHERE id=?", now.UnixNano(), size, id); err != nil {
		return err
	}
	return tx.Commit()
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

// lines hands each line of r to each, with its line break, until each
// returns false.
func lines(r io.Reader, each func(line []byte) bool) error {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		done := len(line) > 0 && !each(line)
		if err == io.EOF {
			done, err = true, nil
		}
		// A regular file fails to read only when its disk does, which no
		// test can bring about: its error leaves where the end does.
		if done || err != nil {
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
	in, err := a.every()
	if err != nil {
		return nil, err
	}
	dir := a.partyDir(who)
	return slices.DeleteFunc(in, func(s session) bool {
		return filepath.Dir(s.path) != dir || who.Peer != "" && s.name != Name(who.Session)
	}), nil
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
		err := a.searchRecords(s.path, needle, func(r Record) bool {
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
		fmt.Fprintf(&out, "[more hits are not shown; searched %s: search for something more precise]", scope)
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
// for 0, at most MaxRecords) and at most MaxReadBytes including the note where
// to go on. Offset skips bytes of the first formatted record, on a UTF-8
// boundary. Without session it is the current conversation; a peer may name
// no other.
func (a *Archive) Read(who Who, session string, from, count, offset int) (string, error) {
	if offset < 0 {
		return "", errors.New("offset must not be negative")
	}
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
	nextOffset := 0
	var offsetErr error
	err := a.records(a.file(who, session), func(r Record) bool {
		total = r.Seq // the records after those returned are counted on
		if r.Seq < from || full {
			return true
		}
		text := show(r)
		if first == 0 {
			if offset >= len(text) || !utf8.RuneStart(text[offset]) {
				offsetErr = errors.New("offset must be within the first record, on a UTF-8 boundary")
				return false
			}
			text = text[offset:]
		}
		if len(text) > MaxReadBytes-readNote-out.Len() {
			if first > 0 {
				full = true // for the next read
				return true
			}
			keep := MaxReadBytes - readNote
			for !utf8.RuneStart(text[keep]) {
				keep--
			}
			nextOffset = offset + keep
			text = text[:keep]
		}
		if first == 0 {
			first = r.Seq
		}
		last = r.Seq
		out.WriteString(text)
		full = nextOffset > 0 || last-first+1 == count
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
	if offsetErr != nil {
		return "", offsetErr
	}
	if first == 0 {
		return fmt.Sprintf("the session has %d records: there is none from %d on", total, from), nil
	}
	if nextOffset > 0 {
		fmt.Fprintf(&out, "\n[records %d to %d of %d: continue with from %d, offset %d]", first, last, total, last, nextOffset)
	} else if last < total {
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
		started, title := a.describe(s.path)
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
func (a *Archive) describe(path string) (started, title string) {
	title, _, _ = a.beside(a.id(path), titleSuffix)
	a.records(path, func(r Record) bool {
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

// every returns the conversations of every party in the archive, the most
// recently changed first.
func (a *Archive) every() ([]session, error) { return a.allSessions(true) }

// Metadata-only sessions belong in the application listing, but not in
// transcript search or the archived-conversation listing.
func (a *Archive) allSessions(archivedOnly bool) ([]session, error) {
	db, err := a.readDatabase()
	if err != nil || db == nil {
		return nil, err
	}
	query := "SELECT id,modified,size FROM history_sessions WHERE deleted=0"
	if archivedOnly {
		query += " AND archived=1"
	}
	rows, err := db.Query(query + " ORDER BY modified DESC,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []session
	for rows.Next() {
		var id string
		var stamp, size int64
		if err = rows.Scan(&id, &stamp, &size); err != nil {
			return nil, err
		}
		out = append(out, session{filepath.Base(id), filepath.Join(a.dir, id) + suffix, time.Unix(0, stamp), size})
	}
	return out, rows.Err()
}

// listed is how the archive names the conversation s in a listing: the party
// it belongs to and its file.
func listed(s session) string { return filepath.Base(filepath.Dir(s.path)) + "/" + s.name }

// Match returns the conversations whose serialized records contain every word
// of query, ignoring case. Words may occur in separate records. It takes no
// cached shortcut, so appends and deletions from other handles remain visible.
func (a *Archive) Match(query string) ([]string, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, nil
	}
	in, err := a.every()
	if err != nil {
		return nil, err
	}
	var hits []string
	for _, s := range in {
		found := make([]bool, len(words))
		err = a.records(s.path, func(r Record) bool {
			b, _ := json.Marshal(r)
			text := strings.ToLower(string(b))
			for i, w := range words {
				found[i] = found[i] || strings.Contains(text, w)
			}
			return true
		})
		if err != nil {
			return nil, err
		}
		if !slices.Contains(found, false) {
			hits = append(hits, listed(s))
		}
	}
	slices.Sort(hits)
	return hits, nil
}

// List returns every conversation in the archive, including metadata-only
// sessions, of the owner and of every peer, the most recent first.
func (a *Archive) List() ([]Entry, error) {
	in, err := a.allSessions(false)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, s := range in {
		_, title := a.describe(s.path)
		out = append(out, Entry{listed(s), s.modified, s.size, title})
	}
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
	if _, err := a.path(id); err != nil {
		return err
	}
	db, err := a.readDatabase()
	if err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("%w: %s", ErrNotArchived, id)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE history_sessions SET deleted=1,archived=0,title=NULL,effort=NULL,directory=NULL,goon=0,size=0 WHERE id=? AND archived=1 AND deleted=0", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected() // SQLite counts the rows of every statement
	if n == 0 {
		// Legacy Delete removed sidecars even when no answer existed.
		if _, err = tx.Exec("UPDATE history_sessions SET deleted=1,title=NULL,effort=NULL,directory=NULL,goon=0 WHERE id=?", id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("DELETE FROM history_restarts WHERE session_id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM history_records WHERE session_id=?", id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotArchived, id)
	}
	return nil
}

// GoOn marks the conversation id, as List names it, to go on by itself once
// ouro started anew, see GoingOn.
func (a *Archive) GoOn(id string) error { return a.setBeside(id, goOnSuffix, "") }

// GoingOn atomically consumes the owner's restart marks, including marks set
// before the first archived answer. Peer marks are not consumed here.
func (a *Archive) GoingOn() ([]string, error) {
	db, err := a.readDatabase()
	if err != nil || db == nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Acquire the writer lock before selecting marks.
	if _, err = tx.Exec("UPDATE history_sessions SET goon=goon WHERE 0"); err != nil {
		return nil, err
	}
	// The IDs are separated by spaces, which none of them holds.
	var marked string
	if err = tx.QueryRow("SELECT coalesce(group_concat(id,' ' ORDER BY id),'') FROM history_sessions WHERE goon=1 AND deleted=0 AND id LIKE 'owner/%'").Scan(&marked); err != nil {
		return nil, err
	}
	ids := strings.Fields(marked)
	if _, err = tx.Exec("UPDATE history_sessions SET goon=0 WHERE id LIKE 'owner/%'"); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

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
	return a.setBeside(a.id(path), titleSuffix, title)
}

// Title returns the title the conversation id, as List names it, was given
// with SetTitle, "" without one.
func (a *Archive) Title(id string) (string, error) {
	title, _, err := a.beside(id, titleSuffix)
	return title, err
}

// beside returns persistent metadata, distinguishing unset from empty.
func (a *Archive) beside(id, kind string) (string, bool, error) {
	if _, err := a.path(id); err != nil {
		return "", false, err
	}
	column := metadataColumn(kind)
	db, err := a.readDatabase()
	if err != nil || db == nil {
		return "", false, err
	}
	var value sql.NullString
	err = db.QueryRow("SELECT "+column+" FROM history_sessions WHERE id=? AND deleted=0", id).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value.String, value.Valid, err
}

// SetEffort gives the conversation id, as List names it, an effort of its
// own, "" too: how much the model thinks in it. What an effort is, is not
// for the archive to know. The conversation needs not be archived yet.
func (a *Archive) SetEffort(id, effort string) error {
	path, err := a.path(id)
	if err != nil {
		return err
	}
	return a.setBeside(a.id(path), effortSuffix, effort)
}

// Effort returns the effort the conversation id, as List names it, was given
// with SetEffort, and whether it was given one.
func (a *Archive) Effort(id string) (string, bool, error) {
	return a.beside(id, effortSuffix)
}

// SetModel gives the conversation id, as List names it, the model that
// answered in it last. The conversation needs not be archived yet.
func (a *Archive) SetModel(id, model string) error {
	path, err := a.path(id)
	if err != nil {
		return err
	}
	return a.setBeside(a.id(path), modelSuffix, model)
}

// Model returns the model the conversation id, as List names it, was given
// with SetModel, and whether it was given one.
func (a *Archive) Model(id string) (string, bool, error) {
	return a.beside(id, modelSuffix)
}

// ModelChanged is the role of the record that tells that the conversation
// goes on under another model than before: replay drops what came before it.
const ModelChanged = "model"

// NoteModel keeps that the conversation id goes on under model from now on:
// what was said before is not resumed, so that no model is sent a
// conversation it has none of cached.
func (a *Archive) NoteModel(id, model string) error {
	if _, err := a.path(id); err != nil {
		return err
	}
	// Open before the model is kept: a failed open then leaves neither the
	// model nor the record that says where the conversation goes on.
	db, err := a.database()
	if err != nil {
		return err
	}
	if err := a.SetModel(id, model); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO history_sessions(id) VALUES(?) ON CONFLICT(id) DO UPDATE SET deleted=0", id); err != nil {
		return err
	}
	var seq, answer, ordinal int
	if err = tx.QueryRow("SELECT coalesce(max(seq),0),coalesce(max(answer),0),coalesce(max(ordinal),0) FROM history_records WHERE session_id=?", id).Scan(&seq, &answer, &ordinal); err != nil {
		return err
	}
	r := Record{Seq: seq + 1, Answer: answer, Time: a.now().UTC(), Role: ModelChanged, Content: model}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err = insertRecord(tx, id, ordinal+1, r, b); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE history_sessions SET archived=1,modified=?,size=size+? WHERE id=?", r.Time.UnixNano(), len(b)+1, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Records returns every record of the conversation id, as List names it, in
// order.
func (a *Archive) Records(id string) ([]Record, error) {
	path, err := a.path(id)
	if err != nil {
		return nil, err
	}
	var out []Record
	err = a.records(path, func(r Record) bool { out = append(out, r); return true })
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotArchived, id)
	}
	return out, err
}
