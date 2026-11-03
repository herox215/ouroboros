// Package memory stores the memories of the owner: what they asked to be
// remembered, and what the review of an answer learned from what they said.
// It keeps no transcripts. Every memory belongs to a named topic, not to a
// directory: the agent finds the topics in its prompt, see Topics, loads the
// memories of one with Load, writes with Add and deletes with Forget. The
// memories of the topic Core go into every prompt of the owner's agent;
// together they have at most MaxCore characters. What Add overwrites and
// Forget deletes is kept for KeepDays in the history, see Previous, so that a
// mistake can be restored by hand.
package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ouroboros/internal/storage"

	"modernc.org/sqlite"
)

const (
	MaxContent = 8192
	MaxKey     = 128
	MaxTopic   = 80
	// MaxHits is how many memories Load returns at most.
	MaxHits = 20
	// MaxCore is how many characters the contents of all memories of the
	// topic Core have together: a write that would make them more fails
	// with ErrCoreFull.
	MaxCore = 2000
	// KeepDays is how long the history keeps what was overwritten or
	// forgotten.
	KeepDays = 30
)

// The fixed topics: Core goes into every prompt, General holds what concerns
// no topic of its own.
const (
	Core    = "core"
	General = "general"
)

var (
	ErrNotFound = errors.New("memory not found in this topic")
	ErrConflict = errors.New("memory version conflict; load it again before changing it")
	ErrCoreFull = fmt.Errorf("core memories are full (%d characters together): shorten or forget memories of the topic core first", MaxCore)
)

// Entry is the current version of a memory. Source is a trusted owner
// conversation ID, supplied by the caller's authenticated context, never a
// model argument.
type Entry struct {
	ID      string `json:"id"`
	Topic   string `json:"topic"`
	Key     string `json:"key"`
	Content string `json:"content"`
	Version int64  `json:"version"`
	Created string `json:"created"`
	Updated string `json:"updated"`
	Source  string `json:"source_session"`
	// Expires is when a memory of a state ends, in RFC 3339, "" for never:
	// from then on it is neither loaded nor listed.
	Expires string `json:"expires,omitempty"`
	// Used counts how often a search of Load found it.
	Used int64 `json:"used,omitempty"`
}

// Topic is a topic with how many current memories it has.
type Topic struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Store struct {
	db    *sql.DB
	owned bool
	wal   *storage.WAL // that db writes to, if Open opened it; see write
}

// Close releases a pool opened by Open; New leaves its caller-owned pool alone.
func (s *Store) Close() error {
	if s.owned {
		return s.db.Close()
	}
	return nil
}

// Open uses the shared history storage directory and owns its connection pool.
func Open(dir string) (*Store, error) {
	db, err := storage.Open(dir)
	if err != nil {
		return nil, err
	}
	s, err := New(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	wal := storage.Watch(db, dir)
	s.owned, s.wal = true, &wal
	return s, nil
}

// schema are the tables of the memories by topic. topic_key is the name of
// the topic in lower case: names compare case-insensitively.
const schema = `CREATE TABLE IF NOT EXISTS topic_memories (
 id TEXT PRIMARY KEY, topic TEXT NOT NULL, topic_key TEXT NOT NULL, key TEXT NOT NULL,
 content TEXT NOT NULL, version INTEGER NOT NULL, created TEXT NOT NULL,
 updated TEXT NOT NULL, source TEXT NOT NULL, expires TEXT NOT NULL DEFAULT '',
 used INTEGER NOT NULL DEFAULT 0, UNIQUE(topic_key,key));
 CREATE VIRTUAL TABLE IF NOT EXISTS topic_memory_fts USING fts5(key,content);
 CREATE TRIGGER IF NOT EXISTS topic_memory_insert AFTER INSERT ON topic_memories BEGIN
 INSERT INTO topic_memory_fts(rowid,key,content) VALUES(new.rowid,new.key,new.content); END;
 CREATE TRIGGER IF NOT EXISTS topic_memory_update AFTER UPDATE ON topic_memories BEGIN
 DELETE FROM topic_memory_fts WHERE rowid=old.rowid;
 INSERT INTO topic_memory_fts(rowid,key,content) VALUES(new.rowid,new.key,new.content); END;
 CREATE TRIGGER IF NOT EXISTS topic_memory_delete AFTER DELETE ON topic_memories BEGIN
 DELETE FROM topic_memory_fts WHERE rowid=old.rowid; END;
 CREATE TABLE IF NOT EXISTS memory_history (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, action TEXT NOT NULL,
 id TEXT NOT NULL, topic TEXT NOT NULL, key TEXT NOT NULL, content TEXT NOT NULL,
 version INTEGER NOT NULL, source TEXT NOT NULL, expires TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS memory_meta (name TEXT PRIMARY KEY, value TEXT NOT NULL);`

// migrated is the name in memory_meta that says the memories of the
// directories were moved to topics, see migrate.
const migrated = "topics-migrated"

// New installs the memory tables without changing other shared tables, and
// moves the memories of an earlier version to topics once, see migrate.
func New(db *sql.DB) (*Store, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(schema); err == nil {
		err = migrate(tx)
	}
	if err != nil {
		return nil, fmt.Errorf("memory schema: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrate moves the memories of the earlier table memories, which bound them
// to a directory, to topics, once: a core one to Core, a global one to
// General, and one of a directory to the topic of its base name. Archived and
// expired ones are not moved. The earlier table stays as it is, as a backup.
// Of two of the same key in one topic, the latest updated is kept.
func migrate(tx *sql.Tx) error {
	var done int
	if err := tx.QueryRow("SELECT count(*) FROM memory_meta WHERE name=?", migrated).Scan(&done); err != nil || done > 0 {
		return err
	}
	var old int
	if err := tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='memories'").Scan(&old); err != nil {
		return err
	}
	if old > 0 {
		var columns int
		if err := tx.QueryRow("SELECT count(*) FROM pragma_table_info('memories') WHERE name IN ('core','expires','archived')").Scan(&columns); err != nil {
			return err
		}
		filter, core := "", "0"
		if columns == 3 {
			filter, core = " WHERE archived=0 AND (expires='' OR expires>'"+now()+"')", "core"
		}
		rows, err := tx.Query("SELECT id,project,key,content,version,created,updated,source," + core + " FROM memories" + filter + " ORDER BY updated")
		if err != nil {
			return err
		}
		var moved []Entry
		for rows.Next() {
			var e Entry
			var project string
			var isCore bool
			if err = rows.Scan(&e.ID, &project, &e.Key, &e.Content, &e.Version, &e.Created, &e.Updated, &e.Source, &isCore); err != nil {
				rows.Close()
				return err
			}
			switch {
			case isCore:
				e.Topic = Core
			case project == "":
				e.Topic = General
			default:
				e.Topic = topicOf(project)
			}
			moved = append(moved, e)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		for _, e := range moved { // the latest last, so it holds
			if _, err = tx.Exec("INSERT INTO topic_memories (id,topic,topic_key,key,content,version,created,updated,source) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(topic_key,key) DO UPDATE SET content=excluded.content,updated=excluded.updated,source=excluded.source,version=version+1",
				e.ID, e.Topic, foldTopic(e.Topic), e.Key, e.Content, e.Version, e.Created, e.Updated, e.Source); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec("INSERT INTO memory_meta(name,value) VALUES(?,?)", migrated, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// topicOf is the topic of the memories of a directory: its base name.
func topicOf(project string) string {
	name := filepath.Base(filepath.Clean(project))
	if name == "." || name == "/" || name == "" {
		return General
	}
	return name
}

// CleanTopic is name as a topic has it: without the space around it, and
// General for an empty one. It fails for one that is too long or no text.
func CleanTopic(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return General, nil
	}
	if len(name) > MaxTopic || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("a topic is valid text of at most %d bytes", MaxTopic)
	}
	return name, nil
}

// foldTopic is the name a topic is compared by.
func foldTopic(name string) string { return strings.ToLower(name) }

func validate(key, content, source string) error {
	if strings.TrimSpace(key) == "" || len(key) > MaxKey || !utf8.ValidString(key) || strings.ContainsRune(key, 0) {
		return errors.New("memory key must be valid text of 1 to 128 bytes without NUL")
	}
	if strings.TrimSpace(content) == "" || len(content) > MaxContent || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return errors.New("memory content must be valid text of 1 to 8192 bytes without NUL")
	}
	if source == "" || len(source) > 256 {
		return errors.New("trusted owner source session is required")
	}
	return nil
}

const columns = "id,topic,key,content,version,created,updated,source,expires,used"

// current is what a memory that has not expired at the time it is given is.
const current = "(expires='' OR expires>?)"

// now is the time as Entry.Expires has it.
func now() string { return time.Now().UTC().Format(time.RFC3339) }

type scanner interface{ Scan(...any) error }

func scan(row scanner) (e Entry, err error) {
	err = row.Scan(&e.ID, &e.Topic, &e.Key, &e.Content, &e.Version, &e.Created, &e.Updated, &e.Source, &e.Expires, &e.Used)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// until is t as Entry.Expires has it.
func until(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Add keeps content under key in topic, which it creates if there is none of
// that name: a memory of the same key there is overwritten, and kept in the
// history. It returns the memory and what it replaced, an Entry without ID if
// nothing, which Undo needs. A memory of a state expires, if expires is not
// zero. The memories of Core must fit, see MaxCore.
func (s *Store) Add(ctx context.Context, topic, key, content, source string, expires time.Time) (e, was Entry, err error) {
	if topic, err = CleanTopic(topic); err != nil {
		return Entry{}, Entry{}, err
	}
	key = strings.TrimSpace(key)
	if err = validate(key, content, source); err != nil {
		return Entry{}, Entry{}, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		was, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM topic_memories WHERE topic_key=? AND key=?", foldTopic(topic), key))
		stamp := time.Now().UTC().Format(time.RFC3339Nano)
		if errors.Is(err, ErrNotFound) {
			var id [16]byte
			rand.Read(id[:]) // never fails
			// A topic there is keeps the name it has.
			name := topic
			tx.QueryRowContext(ctx, "SELECT topic FROM topic_memories WHERE topic_key=? LIMIT 1", foldTopic(topic)).Scan(&name)
			was, e = Entry{}, Entry{ID: hex.EncodeToString(id[:]), Topic: name, Key: key, Content: content, Version: 1, Created: stamp, Updated: stamp, Source: source, Expires: until(expires)}
			_, err = tx.ExecContext(ctx, "INSERT INTO topic_memories (id,topic,topic_key,key,content,version,created,updated,source,expires) VALUES(?,?,?,?,?,?,?,?,?,?)",
				e.ID, e.Topic, foldTopic(e.Topic), e.Key, e.Content, e.Version, e.Created, e.Updated, e.Source, e.Expires)
		} else if err == nil {
			e = was
			e.Content, e.Source, e.Version, e.Expires, e.Updated = content, source, was.Version+1, until(expires), stamp
			if err = keep(ctx, tx, "overwritten", was); err == nil {
				_, err = tx.ExecContext(ctx, "UPDATE topic_memories SET content=?,source=?,version=?,expires=?,updated=? WHERE id=?", e.Content, e.Source, e.Version, e.Expires, e.Updated, e.ID)
			}
		}
		if err == nil && foldTopic(e.Topic) == Core {
			err = full(ctx, tx)
		}
		return err
	})
	if err != nil {
		return Entry{}, Entry{}, err
	}
	return e, was, nil
}

// keep writes e to the history, as what action did to it, and drops what is
// older there than KeepDays.
func keep(ctx context.Context, tx *sql.Tx, action string, e Entry) error {
	at := time.Now().UTC()
	_, err := tx.ExecContext(ctx, "INSERT INTO memory_history (at,action,id,topic,key,content,version,source,expires) VALUES(?,?,?,?,?,?,?,?,?)",
		at.Format(time.RFC3339Nano), action, e.ID, e.Topic, e.Key, e.Content, e.Version, e.Source, e.Expires)
	if err == nil {
		_, err = tx.ExecContext(ctx, "DELETE FROM memory_history WHERE at<?", at.AddDate(0, 0, -KeepDays).Format(time.RFC3339Nano))
	}
	return err
}

// full is ErrCoreFull when the memories of Core have more than MaxCore
// characters together: it is asked after a write, which is then rolled back.
func full(ctx context.Context, tx *sql.Tx) error {
	var used int
	if err := tx.QueryRowContext(ctx, "SELECT coalesce(sum(length(content)),0) FROM topic_memories WHERE topic_key=?", Core).Scan(&used); err != nil {
		return err
	}
	if used > MaxCore {
		return ErrCoreFull
	}
	return nil
}

// Forget deletes the memory key of topic and keeps it in the history. It
// returns what it deleted.
func (s *Store) Forget(ctx context.Context, topic, key string) (Entry, error) {
	topic, err := CleanTopic(topic)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	err = s.write(ctx, func(tx *sql.Tx) error {
		e, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM topic_memories WHERE topic_key=? AND key=?", foldTopic(topic), strings.TrimSpace(key)))
		if err == nil {
			err = forget(ctx, tx, e)
		}
		return err
	})
	return e, err
}

// forget deletes e and keeps it in the history.
func forget(ctx context.Context, tx *sql.Tx, e Entry) error {
	if err := keep(ctx, tx, "forgotten", e); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM topic_memories WHERE id=?", e.ID)
	return err
}

// Undo takes back what Add made e of was: it forgets e if was has no ID, and
// writes was back otherwise. It fails with ErrConflict if e was changed since.
func (s *Store) Undo(ctx context.Context, e, was Entry) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		got, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM topic_memories WHERE id=?", e.ID))
		if err != nil {
			return err
		}
		if got.Version != e.Version {
			return ErrConflict
		}
		if was.ID == "" {
			return forget(ctx, tx, got)
		}
		if err = keep(ctx, tx, "overwritten", got); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE topic_memories SET content=?,source=?,expires=?,updated=?,version=version+1 WHERE id=?",
			was.Content, was.Source, was.Expires, time.Now().UTC().Format(time.RFC3339Nano), e.ID)
		return err
	})
}

// Update replaces the memory id at version by key and content, as the owner
// typed it in a dialog: the version before is kept in the history. It fails
// with ErrConflict if the memory changed since.
func (s *Store) Update(ctx context.Context, id string, version int64, key, content, source string) (Entry, error) {
	key = strings.TrimSpace(key)
	if err := validate(key, content, source); err != nil {
		return Entry{}, err
	}
	var e Entry
	err := s.write(ctx, func(tx *sql.Tx) error {
		was, err := scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM topic_memories WHERE id=?", id))
		if err != nil {
			return err
		}
		if was.Version != version {
			return ErrConflict
		}
		if err = keep(ctx, tx, "overwritten", was); err != nil {
			return err
		}
		e = was
		e.Key, e.Content, e.Source, e.Version, e.Updated = key, content, source, was.Version+1, time.Now().UTC().Format(time.RFC3339Nano)
		if _, err = tx.ExecContext(ctx, "UPDATE topic_memories SET key=?,content=?,source=?,version=?,updated=? WHERE id=?", e.Key, e.Content, e.Source, e.Version, e.Updated, e.ID); err != nil {
			return err
		}
		if foldTopic(e.Topic) == Core {
			return full(ctx, tx)
		}
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Read returns the memory id, also one that expired.
func (s *Store) Read(ctx context.Context, id string) (Entry, error) {
	return scan(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM topic_memories WHERE id=?", id))
}

// Load returns at most count memories of topic that have not expired, MaxHits
// if count is not positive or more: without search the most used and latest
// first, with it those with any of its words, the best match first. What a
// search finds is counted as used.
func (s *Store) Load(ctx context.Context, topic, search string, count int) ([]Entry, error) {
	topic, err := CleanTopic(topic)
	if err != nil {
		return nil, err
	}
	if len(search) > 1024 {
		return nil, errors.New("memory search exceeds 1024 bytes")
	}
	if count <= 0 || count > MaxHits {
		count = MaxHits
	}
	tokens := ftsTokens(search)
	var rows *sql.Rows
	if len(tokens) == 0 {
		rows, err = s.db.QueryContext(ctx, "SELECT "+columns+" FROM topic_memories WHERE topic_key=? AND "+current+" ORDER BY used DESC,updated DESC,id LIMIT ?", foldTopic(topic), now(), count)
	} else {
		rows, err = s.db.QueryContext(ctx, "SELECT "+prefixed("m.")+" FROM topic_memories m JOIN topic_memory_fts f ON f.rowid=m.rowid WHERE topic_memory_fts MATCH ? AND m.topic_key=? AND (m.expires='' OR m.expires>?) ORDER BY bm25(topic_memory_fts),m.updated DESC,m.id LIMIT ?",
			strings.Join(tokens, " OR "), foldTopic(topic), now(), count)
	}
	if err != nil {
		return nil, err
	}
	found, err := entries(rows)
	if err == nil && len(tokens) > 0 && len(found) > 0 {
		s.used(ctx, found) // that it cannot be counted fails no load
	}
	return found, err
}

// prefixed are the columns with prefix before each.
func prefixed(prefix string) string {
	return prefix + strings.ReplaceAll(columns, ",", ","+prefix)
}

// used counts that found were found by a search.
func (s *Store) used(ctx context.Context, found []Entry) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		for _, e := range found {
			if _, err := tx.ExecContext(ctx, "UPDATE topic_memories SET used=used+1 WHERE id=?", e.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// CoreMemories returns the memories of Core that have not expired, the
// oldest first: those that go into every prompt.
func (s *Store) CoreMemories(ctx context.Context) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM topic_memories WHERE topic_key=? AND "+current+" ORDER BY created,id", Core, now())
	if err != nil {
		return nil, err
	}
	return entries(rows)
}

// Topics returns the topics with memories that have not expired, and how
// many of them each has, the topic with the most first. Core is among them
// when it has memories.
func (s *Store) Topics(ctx context.Context) ([]Topic, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT min(topic),count(*) FROM topic_memories WHERE "+current+" GROUP BY topic_key ORDER BY count(*) DESC,topic_key", now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Topic{}
	for rows.Next() {
		var t Topic
		if err := rows.Scan(&t.Name, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Previous returns at most limit versions of memories the history keeps, of
// those overwritten or forgotten in the last KeepDays, the latest first:
// what restores a mistake by hand.
func (s *Store) Previous(ctx context.Context, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = MaxHits
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id,topic,key,content,version,at,at,source,expires,0 FROM memory_history WHERE at>=? ORDER BY seq DESC LIMIT ?",
		time.Now().UTC().AddDate(0, 0, -KeepDays).Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	return entries(rows)
}

// MaxList is how many memories a listing returns at most.
const MaxList = 200

// Listing is what a dialog lists of the memories the agent has collected:
// the memories themselves, and how many there are beyond them.
type Listing struct {
	// Memories are those of every topic, the latest updated first.
	Memories []Entry
	// Total is how many memories there are, those expired included.
	Total int
	// Hidden is how many expired memories are left out, 0 when they were
	// asked for.
	Hidden int
}

// List returns the memories of every topic a dialog of the owner lists, the
// latest updated first. Expired memories are left out unless all is true,
// and Listing.Hidden says how many of them there are. At most limit memories
// are returned, MaxList if limit is not positive.
func (s *Store) List(ctx context.Context, all bool, limit int) (Listing, error) {
	if limit <= 0 || limit > MaxList {
		limit = MaxList
	}
	var out Listing
	if err := s.db.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(expires<>'' AND expires<=?),0) FROM topic_memories", now()).Scan(&out.Total, &out.Hidden); err != nil {
		return Listing{}, err
	}
	where, args := "1", []any{}
	if all {
		out.Hidden = 0
	} else {
		where, args = current, []any{now()}
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM topic_memories WHERE "+where+" ORDER BY updated DESC,id LIMIT ?", append(args, limit)...)
	if err != nil {
		return Listing{}, err
	}
	out.Memories, err = entries(rows)
	return out, err
}

// write retries the whole transaction on SQLite busy errors. FTS triggers may
// read before acquiring the write lock, so concurrent pools can hit a snapshot
// upgrade failure even with the connection's busy timeout. Never retry just the
// statement: roll back both the row and its index before trying again. It
// writes nothing where it would not be kept, see storage.WAL.Kept.
func (s *Store) write(ctx context.Context, f func(*sql.Tx) error) error {
	if s.wal != nil {
		if err := s.wal.Kept(); err != nil {
			return fmt.Errorf("the memories keep nothing until ouro starts anew: %w", err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		tx, err := s.db.BeginTx(ctx, nil)
		if err == nil {
			err = f(tx)
			if err == nil {
				err = tx.Commit()
			}
			tx.Rollback()
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 5 || !time.Now().Before(deadline) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// ftsTokens are the words of query, each quoted for an FTS match: the input
// is never FTS syntax.
func ftsTokens(query string) []string {
	tokens := strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	for i, t := range tokens {
		tokens[i] = "\"" + strings.ReplaceAll(t, "\"", "\"\"") + "\""
	}
	return tokens
}

// entries are the memories rows has, which it closes.
func entries(rows *sql.Rows) ([]Entry, error) {
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
