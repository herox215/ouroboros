package memory

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Candidate is a memory the archivist should look at, and why. It is what a
// pass starts with: SQL against the store, no model call, see Candidates.
type Candidate struct {
	Entry
	Reasons []string
}

// Pair are two current memories of one scope that say nearly the same.
type Pair struct {
	A, B Entry
}

// Current returns at most limit memories that are neither archived nor
// expired, core ones included, by project and key. Unlike Active it keeps
// the core memories and the stale ones: the archivist may change any of them.
func (s *Store) Current(ctx context.Context, limit int) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM memories WHERE "+current+" ORDER BY project,key LIMIT ?", now(), limit)
	if err != nil {
		return nil, err
	}
	return entries(rows)
}

// Candidates returns the memories a pass should look at, or none: those new
// or changed since marker, those written long ago and
// never used, those whose project directory is gone, the core ones while the
// core memories are nearly full, and those that say nearly the same as
// another. What was examined in the version it has now is left out, see
// Meta; a pair that was judged different is left out while neither changed.
func (s *Store) Candidates(ctx context.Context, marker time.Time, staleAfter time.Duration, limit int) ([]Candidate, error) {
	all, err := s.Current(ctx, limit)
	if err != nil {
		return nil, err
	}
	seen, err := s.Metas(ctx, "archivist/seen/")
	if err != nil {
		return nil, err
	}
	at := time.Now()
	out := map[string]*Candidate{}
	add := func(e Entry, why string) {
		c := out[e.ID]
		if c == nil {
			c = &Candidate{Entry: e}
			out[e.ID] = c
		}
		if !slices.Contains(c.Reasons, why) {
			c.Reasons = append(c.Reasons, why)
		}
	}
	for _, e := range all {
		var why []string
		if marker.IsZero() || updatedAfter(e.Updated, marker) {
			why = append(why, "new or changed since the last pass")
		}
		if staleAfter > 0 && e.Used == 0 {
			if t, err := time.Parse(time.RFC3339Nano, e.Created); err == nil && at.Sub(t) >= staleAfter {
				why = append(why, "written long ago and never used")
			}
		}
		if e.Project != "" {
			if _, err := os.Stat(e.Project); errors.Is(err, os.ErrNotExist) {
				why = append(why, "its project directory is gone")
			}
		}
		if len(why) == 0 || seen["archivist/seen/"+e.ID] == strconv.FormatInt(e.Version, 10) {
			continue
		}
		for _, w := range why {
			add(e, w)
		}
	}
	var used int
	if err := s.db.QueryRowContext(ctx, "SELECT coalesce(sum(length(content)),0) FROM memories WHERE core=1 AND archived=0").Scan(&used); err != nil {
		return nil, err
	}
	if used*10 >= MaxCore*9 {
		for _, e := range all {
			if e.Core && seen["archivist/seen/"+e.ID] != strconv.FormatInt(e.Version, 10) {
				add(e, "the core memories are nearly full")
			}
		}
	}
	pairs := pairsOf(all)
	notes, err := s.Metas(ctx, "archivist/pair/")
	if err != nil {
		return nil, err
	}
	for _, p := range pairs {
		if notes[PairName(p.A, p.B)] == PairVersions(p) {
			continue
		}
		add(p.A, "says nearly the same as "+strconv.Quote(p.B.Key))
		add(p.B, "says nearly the same as "+strconv.Quote(p.A.Key))
	}
	if len(out) == 0 {
		return nil, nil
	}
	list := make([]Candidate, 0, len(out))
	for _, c := range out {
		list = append(list, *c)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, nil
}

// Pairs returns the current memories of one scope that say nearly the same,
// see pairsOf.
func (s *Store) Pairs(ctx context.Context, limit int) ([]Pair, error) {
	all, err := s.Current(ctx, limit)
	if err != nil {
		return nil, err
	}
	return pairsOf(all), nil
}

// pairsOf are the pairs among all that say nearly the same: their words
// overlap by at least three fifths of the shorter one, with at least three
// words in common.
func pairsOf(all []Entry) []Pair {
	words := make([]map[string]bool, len(all))
	for i, e := range all {
		words[i] = wordsOf(e.Key + " " + e.Content)
	}
	var out []Pair
	for i := range all {
		for j := i + 1; j < len(all); j++ {
			if all[i].Project != all[j].Project || overlap(words[i], words[j]) < 0.6 {
				continue
			}
			out = append(out, Pair{all[i], all[j]})
		}
	}
	return out
}

// PairName is the name under which the note of a pair is kept.
func PairName(a, b Entry) string {
	ids := []string{a.ID, b.ID}
	sort.Strings(ids)
	return "archivist/pair/" + ids[0] + "/" + ids[1]
}

// PairVersions is what the note of p holds while neither of the two changed.
func PairVersions(p Pair) string {
	return strconv.FormatInt(p.A.Version, 10) + ":" + strconv.FormatInt(p.B.Version, 10)
}

// SetMeta keeps value under name.
func (s *Store) SetMeta(ctx context.Context, name, value string) error {
	return s.SetMetas(ctx, map[string]string{name: value})
}

// SetMetas keeps the values under their names, in one transaction.
func (s *Store) SetMetas(ctx context.Context, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		for name, value := range values {
			if _, err := tx.ExecContext(ctx, "INSERT INTO memory_meta(name,value) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value", name, value); err != nil {
				return err
			}
		}
		return nil
	})
}

// Metas returns the values of the names that start with prefix, by name.
func (s *Store) Metas(ctx context.Context, prefix string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name,value FROM memory_meta WHERE substr(name,1,?)=?", len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, rows.Err()
}

// Get returns the memory id of any scope, archived or not.
func (s *Store) Get(ctx context.Context, id string) (Entry, error) {
	return scan(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM memories WHERE id=?", id))
}

// Find returns at most limit current memories of every scope that have any
// of the words of query, the best first. Each has only an excerpt, as with
// Search; Get returns the whole memory.
func (s *Store) Find(ctx context.Context, query string, limit int) ([]Entry, error) {
	tokens := ftsTokens(query)
	if len(tokens) == 0 {
		return []Entry{}, nil
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+excerpt+" FROM memories m JOIN memory_fts ON memory_fts.rowid=m.rowid"+
		" WHERE m.archived=0 AND (m.expires='' OR m.expires>?) AND memory_fts MATCH ?"+
		" ORDER BY bm25(memory_fts),m.updated DESC,m.id LIMIT ?", now(), strings.Join(tokens, " OR "), cmp.Or(min(max(limit, 0), MaxHits), 10))
	if err != nil {
		return nil, err
	}
	return entries(rows)
}

// Version returns the version a memory has now.
func (s *Store) Version(ctx context.Context, id string) (int64, error) {
	var version int64
	err := s.db.QueryRowContext(ctx, "SELECT version FROM memories WHERE id=?", id).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return version, err
}

// Part is one memory a split makes.
type Part struct {
	Key     string `json:"key"`
	Content string `json:"content"`
}

// Change is one change the archivist makes to a store, see Apply.
type Change struct {
	Action  string   `json:"op"`
	IDs     []string `json:"ids,omitempty"`
	ID      string   `json:"id,omitempty"`
	Key     string   `json:"key,omitempty"`
	Content string   `json:"content,omitempty"`
	Project string   `json:"project,omitempty"`
	Core    *bool    `json:"core,omitempty"`
	Parts   []Part   `json:"parts,omitempty"`
	Reason  string   `json:"reason"`
	// Versions are the versions of the memories the change touches, by ID, as
	// the archivist saw them: Apply refuses a change whose memory changed
	// since. The desk of a pass sets them; the model does not.
	Versions map[string]int64 `json:"-"`
}

// ChangeRecord is a change the archivist made, as it is in the log.
type ChangeRecord struct {
	ID     int64  `json:"id"`
	At     string `json:"at"`
	Action string `json:"action"`
	Reason string `json:"reason"`
	Undone bool   `json:"undone,omitempty"`
	// Created are the memories the change made, as a merge or a split does.
	// The log does not list them.
	Created []string `json:"-"`
}

// ErrNoChange is a change that is not one the archivist makes.
var ErrNoChange = errors.New("not a change the archivist makes")

const archivistSource = "archivist"

type undoState struct {
	Before  []Entry  `json:"before"`
	Created []string `json:"created,omitempty"`
}

// Apply makes one change of the archivist and keeps what it was, so that it
// can be undone, see UndoChange. Nothing is deleted: a memory that a change
// replaces is archived, and a rewrite keeps its previous text in the undo.
// A change that fails leaves nothing behind.
func (s *Store) Apply(ctx context.Context, c Change) (ChangeRecord, error) {
	c.Action = strings.ToLower(strings.TrimSpace(c.Action))
	if strings.TrimSpace(c.Reason) == "" {
		return ChangeRecord{}, errors.New("a change needs its reason")
	}
	var rec ChangeRecord
	err := s.write(ctx, func(tx *sql.Tx) error {
		before, err := load(ctx, tx, idsOf(c))
		if err != nil {
			return err
		}
		if err := seenVersions(before, c.Versions); err != nil {
			return err
		}
		created := []string{}
		switch c.Action {
		case "merge":
			created, before, err = merge(ctx, tx, &c, before)
		case "rewrite":
			err = rewrite(ctx, tx, c, before)
		case "split":
			created, err = split(ctx, tx, c, before)
		case "scope":
			err = scope(ctx, tx, c, before)
		case "core":
			err = recore(ctx, tx, c, before)
		case "archive":
			err = archiveChange(ctx, tx, c, before)
		default:
			return fmt.Errorf("%w: %q", ErrNoChange, c.Action)
		}
		if err != nil {
			return err
		}
		undo, _ := json.Marshal(undoState{Before: before, Created: created}) // texts, numbers and flags: cannot fail
		at := time.Now().UTC().Format(time.RFC3339Nano)
		r, err := tx.ExecContext(ctx, "INSERT INTO memory_changes(at,action,reason,undo) VALUES(?,?,?,?)", at, c.Action, c.Reason, string(undo))
		if err != nil {
			return err
		}
		rec.ID, err = r.LastInsertId()
		rec.At, rec.Action, rec.Reason, rec.Created = at, c.Action, c.Reason, created
		return err
	})
	return rec, err
}

func idsOf(c Change) []string {
	var ids []string
	if c.ID != "" {
		ids = append(ids, c.ID)
	}
	for _, id := range c.IDs {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func load(ctx context.Context, tx *sql.Tx, ids []string) ([]Entry, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i], marks[i] = id, "?"
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM memories WHERE id IN ("+strings.Join(marks, ",")+")", args...)
	if err != nil {
		return nil, err
	}
	return entries(rows)
}

// seenVersions refuses a change whose memory is no longer in the version the
// archivist saw: its text would otherwise be replaced by one derived from an
// older text.
func seenVersions(entries []Entry, versions map[string]int64) error {
	for _, e := range entries {
		if v, ok := versions[e.ID]; ok && v != e.Version {
			return fmt.Errorf("memory %s changed since you read it", e.ID)
		}
	}
	return nil
}

func find(entries []Entry, id string) (Entry, bool) {
	if i := slices.IndexFunc(entries, func(e Entry) bool { return e.ID == id }); i >= 0 {
		return entries[i], true
	}
	return Entry{}, false
}

// fits checks that a rewrite may not say more than its sources: it may merge,
// shorten and generalise, but its text is not longer than theirs, with room
// for a sentence.
func fits(key, content string, sources []Entry) error {
	if err := validate(key, content, archivistSource); err != nil {
		return err
	}
	total := 0
	for _, e := range sources {
		total += len(e.Content)
	}
	if len(content) > total+64 {
		return errors.New("a rewrite may not say more than its sources")
	}
	return nil
}

// upsert writes the memory of project and key, replacing one of that key, and
// says whether it is new. It returns the memory and, if it replaced one, that
// one too, which the caller puts into the undo.
func upsert(ctx context.Context, tx *sql.Tx, project, key, content string, core bool) (e, was Entry, fresh_ bool, err error) {
	e, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM memories WHERE project=? AND key=?", project, key))
	if errors.Is(err, ErrNotFound) {
		e = fresh(project, key, content, archivistSource, core, time.Time{})
		return e, Entry{}, true, insert(ctx, tx, e)
	}
	if err != nil {
		return Entry{}, Entry{}, false, err
	}
	was = e
	e.Content, e.Source, e.Core, e.Version, e.Archived = content, archivistSource, core, e.Version+1, false
	e.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	return e, was, false, replace(ctx, tx, e)
}

func merge(ctx context.Context, tx *sql.Tx, c *Change, before []Entry) ([]string, []Entry, error) {
	if len(c.IDs) < 2 {
		return nil, before, errors.New("a merge needs two memories")
	}
	sources := make([]Entry, len(c.IDs))
	project := ""
	for i, id := range c.IDs {
		e, ok := find(before, id)
		if !ok {
			return nil, before, fmt.Errorf("memory %s is not there", id)
		}
		if i == 0 {
			project = e.Project
		} else if e.Project != project {
			return nil, before, errors.New("a merge needs memories of one scope")
		}
		sources[i] = e
	}
	if strings.TrimSpace(c.Key) == "" {
		c.Key = sources[0].Key
	}
	core := true
	for _, e := range sources {
		core = core && e.Core
	}
	if c.Core != nil {
		core = *c.Core
	}
	if core {
		project = ""
	}
	if err := fits(c.Key, c.Content, sources); err != nil {
		return nil, before, err
	}
	// A memory of the target key that is not one of the sources would be
	// replaced although the archivist never saw it: it has to name it among
	// the ids, whose texts it was shown.
	if e, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM memories WHERE project=? AND key=?", project, c.Key)); err == nil && !containsID(before, e.ID) {
		return nil, before, fmt.Errorf("a memory with the key %q is already there: name it among the ids", c.Key)
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, before, err
	}
	e, was, fresh_, err := upsert(ctx, tx, project, c.Key, c.Content, core)
	if err != nil {
		return nil, before, err
	}
	if !fresh_ && !containsID(before, was.ID) {
		before = append(before, was)
	}
	created := []string{}
	if fresh_ {
		created = append(created, e.ID)
	}
	for _, id := range c.IDs {
		if id != e.ID {
			if err := archiveID(ctx, tx, id); err != nil {
				return nil, before, err
			}
		}
	}
	return created, before, nil
}

func rewrite(ctx context.Context, tx *sql.Tx, c Change, before []Entry) error {
	e, ok := find(before, c.ID)
	if !ok {
		return ErrNotFound
	}
	key := c.Key
	if strings.TrimSpace(key) == "" {
		key = e.Key
	}
	if err := fits(key, c.Content, []Entry{e}); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE memories SET key=?,content=?,source=?,updated=?,version=version+1 WHERE id=?", key, c.Content, archivistSource, time.Now().UTC().Format(time.RFC3339Nano), e.ID)
	if err != nil || !e.Core {
		return err
	}
	return full(ctx, tx)
}

func split(ctx context.Context, tx *sql.Tx, c Change, before []Entry) ([]string, error) {
	e, ok := find(before, c.ID)
	if !ok {
		return nil, ErrNotFound
	}
	if len(c.Parts) < 2 {
		return nil, errors.New("a split needs two parts")
	}
	total := 0
	for _, p := range c.Parts {
		total += len(p.Content)
	}
	if total > len(e.Content)+64 {
		return nil, errors.New("a split may not say more than its source")
	}
	if err := archiveID(ctx, tx, e.ID); err != nil {
		return nil, err
	}
	var created []string
	for _, p := range c.Parts {
		if err := validate(p.Key, p.Content, archivistSource); err != nil {
			return nil, err
		}
		part := fresh(e.Project, p.Key, p.Content, archivistSource, e.Core, time.Time{})
		if err := insert(ctx, tx, part); err != nil {
			return nil, err
		}
		created = append(created, part.ID)
	}
	return created, nil
}

func scope(ctx context.Context, tx *sql.Tx, c Change, before []Entry) error {
	e, ok := find(before, c.ID)
	if !ok {
		return ErrNotFound
	}
	// A move keeps a core memory core, unless it goes to a project.
	core := e.Core
	if c.Core != nil {
		core = *c.Core
	}
	if c.Project != "" {
		core = false
	}
	_, err := tx.ExecContext(ctx, "UPDATE memories SET project=?,core=?,updated=?,version=version+1 WHERE id=?", c.Project, core, time.Now().UTC().Format(time.RFC3339Nano), e.ID)
	if err == nil && core {
		err = full(ctx, tx)
	}
	return err
}

func recore(ctx context.Context, tx *sql.Tx, c Change, before []Entry) error {
	e, ok := find(before, c.ID)
	if !ok {
		return ErrNotFound
	}
	core := c.Core != nil && *c.Core
	project := e.Project
	if core {
		project = ""
	}
	_, err := tx.ExecContext(ctx, "UPDATE memories SET project=?,core=?,updated=?,version=version+1 WHERE id=?", project, core, time.Now().UTC().Format(time.RFC3339Nano), e.ID)
	if err == nil && core {
		err = full(ctx, tx)
	}
	return err
}

func archiveChange(ctx context.Context, tx *sql.Tx, c Change, before []Entry) error {
	ids := idsOf(c)
	if len(ids) == 0 {
		return errors.New("an archive needs a memory")
	}
	for _, id := range ids {
		if _, ok := find(before, id); !ok {
			return fmt.Errorf("memory %s is not there", id)
		}
		if err := archiveID(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

func archiveID(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, "UPDATE memories SET archived=1 WHERE id=?", id)
	return err
}

func containsID(entries []Entry, id string) bool {
	return slices.ContainsFunc(entries, func(e Entry) bool { return e.ID == id })
}

// UndoChange takes back the change id, see Apply, restoring every memory it
// changed and archiving those it made. A change is undone once.
func (s *Store) UndoChange(ctx context.Context, id int64) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		var undoJSON string
		var undone int
		err := tx.QueryRowContext(ctx, "SELECT undo,undone FROM memory_changes WHERE id=?", id).Scan(&undoJSON, &undone)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if undone != 0 {
			return errors.New("the change was undone already")
		}
		var u undoState
		if err := json.Unmarshal([]byte(undoJSON), &u); err != nil {
			return err
		}
		for _, e := range u.Before {
			_, err := tx.ExecContext(ctx, "UPDATE memories SET key=?,content=?,project=?,core=?,expires=?,archived=?,source=?,updated=?,version=version+1 WHERE id=?",
				e.Key, e.Content, e.Project, e.Core, e.Expires, e.Archived, e.Source, time.Now().UTC().Format(time.RFC3339Nano), e.ID)
			if err != nil {
				return err
			}
		}
		for _, id := range u.Created {
			if err := archiveID(ctx, tx, id); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE memory_changes SET undone=1 WHERE id=?", id)
		return err
	})
}

// Changes returns the latest changes the archivist made, the newest first.
func (s *Store) Changes(ctx context.Context, limit int) ([]ChangeRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,at,action,reason,undone FROM memory_changes ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChangeRecord{}
	for rows.Next() {
		var r ChangeRecord
		if err := rows.Scan(&r.ID, &r.At, &r.Action, &r.Reason, &r.Undone); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// updatedAfter reports whether the stored time s is after marker. The stored
// times have nanoseconds or not, so they are parsed, not compared as text.
func updatedAfter(s string, marker time.Time) bool {
	t, err := time.Parse(time.RFC3339Nano, s)
	return err == nil && t.After(marker)
}

// wordsOf are the words of text that say something: at least three letters
// or digits, without the words that every memory has.
func wordsOf(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if len(w) >= 3 && !common[w] {
			out[w] = true
		}
	}
	return out
}

var common = map[string]bool{
	"der": true, "die": true, "das": true, "und": true, "oder": true, "ein": true, "eine": true,
	"einer": true, "ist": true, "sich": true, "mit": true, "von": true, "auf": true, "für": true,
	"als": true, "per": true, "the": true, "and": true, "for": true, "with": true, "that": true,
	"this": true, "are": true, "was": true, "not": true, "you": true, "its": true,
}

func overlap(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shared := 0
	for w := range a {
		if b[w] {
			shared++
		}
	}
	least := len(a)
	if len(b) < least {
		least = len(b)
	}
	if shared < 3 {
		return 0
	}
	return float64(shared) / float64(least)
}
