// Package memory stores the memories of the owner: what they asked to be
// remembered, and what the review of an answer learned from what they said,
// see Remember. It keeps no transcripts. Core memories are the few that go
// into every prompt of the owner's agent; together they have at most MaxCore
// characters. Others are chosen for a topic, see Related, and counted when
// they are loaded and used: one that is loaded often and never used, or that
// expired, is archived, see Tidy. Nothing is deleted but by Forget.
package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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
	MaxHits    = 20
	// MaxCore is how many characters the contents of all core memories have
	// together: a write that would make them more fails with ErrCoreFull.
	MaxCore = 2000
)

var (
	ErrNotFound = errors.New("memory not found in this scope")
	ErrConflict = errors.New("memory version conflict; read it again before changing it")
	ErrCoreFull = fmt.Errorf("core memories are full (%d characters together): merge or shorten core memories with memory_update first", MaxCore)
)

// Entry is the current version only. Source is a trusted owner conversation ID,
// supplied by the caller's authenticated context, never a model argument.
type Entry struct {
	ID      string `json:"id"`
	Key     string `json:"key"`
	Content string `json:"content"`
	Project string `json:"project,omitempty"`
	Version int64  `json:"version"`
	Created string `json:"created"`
	Updated string `json:"updated"`
	Source  string `json:"source_session"`
	// Core says that it goes into every prompt of the owner's agent. Core
	// memories are global.
	Core bool `json:"core,omitempty"`
	// Expires is when a memory of a state ends, in RFC 3339, "" for never:
	// then Tidy archives it.
	Expires string `json:"expires,omitempty"`
	// Loaded and Used count how often it was put into the prompt of a topic
	// and how often the agent read it or found it.
	Loaded int64 `json:"loaded,omitempty"`
	Used   int64 `json:"used,omitempty"`
	// Archived says that it is chosen for no topic anymore: only a search
	// finds it.
	Archived bool `json:"archived,omitempty"`
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

// added are the columns of memories that came after its first version, with
// their type.
var added = [][2]string{
	{"core", "INTEGER NOT NULL DEFAULT 0"},
	{"expires", "TEXT NOT NULL DEFAULT ''"},
	{"loaded", "INTEGER NOT NULL DEFAULT 0"},
	{"used", "INTEGER NOT NULL DEFAULT 0"},
	{"archived", "INTEGER NOT NULL DEFAULT 0"},
}

// New installs the memory tables without changing other shared tables.
func New(db *sql.DB) (*Store, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS memories (
 id TEXT PRIMARY KEY, project TEXT NOT NULL, key TEXT NOT NULL,
 content TEXT NOT NULL, version INTEGER NOT NULL, created TEXT NOT NULL,
 updated TEXT NOT NULL, source TEXT NOT NULL, UNIQUE(project,key));
 CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(key,content);
 CREATE TRIGGER IF NOT EXISTS memory_insert AFTER INSERT ON memories BEGIN
 INSERT INTO memory_fts(rowid,key,content) VALUES(new.rowid,new.key,new.content); END;
 CREATE TRIGGER IF NOT EXISTS memory_update AFTER UPDATE ON memories BEGIN
 DELETE FROM memory_fts WHERE rowid=old.rowid;
 INSERT INTO memory_fts(rowid,key,content) VALUES(new.rowid,new.key,new.content); END;
 CREATE TRIGGER IF NOT EXISTS memory_delete AFTER DELETE ON memories BEGIN
 DELETE FROM memory_fts WHERE rowid=old.rowid; END;`)
	// A table of a version before a column gets it.
	for _, column := range added {
		if err != nil {
			break
		}
		var has int
		err = tx.QueryRow("SELECT count(*) FROM pragma_table_info('memories') WHERE name=?", column[0]).Scan(&has)
		if err == nil && has == 0 {
			_, err = tx.Exec("ALTER TABLE memories ADD COLUMN " + column[0] + " " + column[1])
		}
	}
	if err == nil {
		_, err = tx.Exec("CREATE TABLE IF NOT EXISTS memory_meta (name TEXT PRIMARY KEY, value TEXT NOT NULL)")
	}
	if err == nil {
		_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS memory_changes (
 id INTEGER PRIMARY KEY AUTOINCREMENT, at TEXT NOT NULL, action TEXT NOT NULL,
 reason TEXT NOT NULL, undo TEXT NOT NULL, undone INTEGER NOT NULL DEFAULT 0)`)
	}
	if err != nil {
		return nil, fmt.Errorf("memory schema: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

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

const columns = "id,key,content,project,version,created,updated,source,core,expires,loaded,used,archived"

type scanner interface{ Scan(...any) error }

func scan(row scanner) (e Entry, err error) {
	err = row.Scan(&e.ID, &e.Key, &e.Content, &e.Project, &e.Version, &e.Created, &e.Updated, &e.Source, &e.Core, &e.Expires, &e.Loaded, &e.Used, &e.Archived)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

func (s *Store) Create(ctx context.Context, project, key, content, source string) (Entry, error) {
	return s.create(ctx, project, key, content, source, false)
}

// CreateCore creates a global core memory, see Entry.Core.
func (s *Store) CreateCore(ctx context.Context, key, content, source string) (Entry, error) {
	return s.create(ctx, "", key, content, source, true)
}

func (s *Store) create(ctx context.Context, project, key, content, source string, core bool) (Entry, error) {
	if err := validate(key, content, source); err != nil {
		return Entry{}, err
	}
	e := fresh(project, key, content, source, core, time.Time{})
	err := s.write(ctx, func(tx *sql.Tx) error {
		return insert(ctx, tx, e)
	})
	if err != nil {
		return Entry{}, err
	}
	return e, nil
}

// fresh is a new memory, at version 1, which expires, if it is not zero.
func fresh(project, key, content, source string, core bool, expires time.Time) Entry {
	var id [16]byte
	rand.Read(id[:]) // never fails
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return Entry{ID: hex.EncodeToString(id[:]), Key: key, Content: content, Project: project, Version: 1, Created: now, Updated: now, Source: source, Core: core, Expires: until(expires)}
}

// until is t as Entry.Expires has it.
func until(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// insert writes e, which must still fit with the other core memories if it
// is one.
func insert(ctx context.Context, tx *sql.Tx, e Entry) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO memories ("+columns+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", e.ID, e.Key, e.Content, e.Project, e.Version, e.Created, e.Updated, e.Source, e.Core, e.Expires, e.Loaded, e.Used, e.Archived)
	if err == nil && e.Core {
		err = full(ctx, tx)
	}
	return err
}

// full is ErrCoreFull when the core memories have more than MaxCore
// characters together: it is asked after a write, which is then rolled back.
func full(ctx context.Context, tx *sql.Tx) error {
	var used int
	if err := tx.QueryRowContext(ctx, "SELECT coalesce(sum(length(content)),0) FROM memories WHERE core=1").Scan(&used); err != nil {
		return err
	}
	if used > MaxCore {
		return ErrCoreFull
	}
	return nil
}

// Core returns the core memories, the oldest first.
func (s *Store) Core(ctx context.Context) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM memories WHERE core=1 AND "+current+" ORDER BY created,id", now())
	if err != nil {
		return nil, err
	}
	return entries(rows)
}

// Remember keeps content under key in the scope of project, as the review of
// an answer learned it, not the owner asked for it: a memory of the same key
// is replaced, as what was said last holds. It returns the memory and what it
// replaced, an Entry without ID if nothing, which Undo needs. A core memory
// is global and must fit, see MaxCore. A memory of a state expires, if
// expires is not zero. What it replaces is no longer archived.
func (s *Store) Remember(ctx context.Context, project, key, content, source string, core bool, expires time.Time) (e, was Entry, err error) {
	if err = validate(key, content, source); err != nil {
		return Entry{}, Entry{}, err
	}
	if core && project != "" {
		return Entry{}, Entry{}, errors.New("core memories are global")
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		was, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM memories WHERE project=? AND key=?", project, key))
		if errors.Is(err, ErrNotFound) {
			was, e = Entry{}, fresh(project, key, content, source, core, expires)
			return insert(ctx, tx, e)
		}
		if err != nil {
			return err
		}
		e = was
		e.Content, e.Source, e.Core, e.Version, e.Expires, e.Archived = content, source, core, was.Version+1, until(expires), false
		e.Updated = time.Now().UTC().Format(time.RFC3339Nano)
		return replace(ctx, tx, e)
	})
	if err != nil {
		return Entry{}, Entry{}, err
	}
	return e, was, nil
}

// replace writes e over the version before it, which must still fit with
// the other core memories if it is one.
func replace(ctx context.Context, tx *sql.Tx, e Entry) error {
	_, err := tx.ExecContext(ctx, "UPDATE memories SET content=?,source=?,core=?,updated=?,version=?,expires=?,archived=? WHERE id=?", e.Content, e.Source, e.Core, e.Updated, e.Version, e.Expires, e.Archived, e.ID)
	if err == nil && e.Core {
		err = full(ctx, tx)
	}
	return err
}

// Undo takes back what Remember made e of was: it forgets e if was has no
// ID, and writes was back otherwise. It fails with ErrConflict if e was
// changed since.
func (s *Store) Undo(ctx context.Context, e, was Entry) error {
	if was.ID == "" {
		return s.Forget(ctx, e.Project, e.ID, e.Version)
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, "UPDATE memories SET content=?,source=?,core=?,expires=?,archived=?,updated=?,version=version+1 WHERE id=? AND project=? AND version=?", was.Content, was.Source, was.Core, was.Expires, was.Archived, time.Now().UTC().Format(time.RFC3339Nano), e.ID, e.Project, e.Version)
		if err != nil {
			return err
		}
		return changed(ctx, tx, r, e.Project, e.ID)
	})
}

func (s *Store) Read(ctx context.Context, project, id string) (Entry, error) {
	return scan(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM memories WHERE id=? AND project=?", id, project))
}

func (s *Store) Update(ctx context.Context, project, id string, version int64, key, content, source string) (Entry, error) {
	if err := validate(key, content, source); err != nil {
		return Entry{}, err
	}
	if version < 1 {
		return Entry{}, ErrConflict
	}
	var e Entry
	err := s.write(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE memories SET key=?,content=?,source=?,updated=?,version=version+1 WHERE id=? AND project=? AND version=?", key, content, source, time.Now().UTC().Format(time.RFC3339Nano), id, project, version)
		if err != nil {
			return err
		}
		if err = changed(ctx, tx, result, project, id); err != nil {
			return err
		}
		e, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM memories WHERE id=? AND project=?", id, project))
		if err == nil && e.Core {
			err = full(ctx, tx)
		}
		return err
	})
	if err != nil {
		return Entry{}, err
	}
	return e, nil
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

func changed(ctx context.Context, tx *sql.Tx, r sql.Result, project, id string) error {
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	var one int
	err = tx.QueryRowContext(ctx, "SELECT 1 FROM memories WHERE id=? AND project=?", id, project).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return ErrConflict
}

// Forget deletes both the current row and its FTS content in one transaction.
// No previous versions or tombstones are retained. SQLite free pages and the
// conversation archive are not a secure-erasure guarantee.
func (s *Store) Forget(ctx context.Context, project, id string, version int64) error {
	if version < 1 {
		return ErrConflict
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, "DELETE FROM memories WHERE id=? AND project=? AND version=?", id, project, version)
		if err != nil {
			return err
		}
		return changed(ctx, tx, r, project, id)
	})
}

// Search treats input as tokens, never as FTS syntax. An empty query lists
// recent memories. Each hit has only a 256-rune excerpt; Read returns full text.
// Scope all means global plus the trusted current project, not other projects.
func (s *Store) Search(ctx context.Context, project, scope, query string, limit int) ([]Entry, error) {
	if len(query) > 1024 {
		return nil, errors.New("memory query exceeds 1024 bytes")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > MaxHits {
		limit = MaxHits
	}
	where := "m.project=''"
	args := []any{}
	switch scope {
	case "", "all":
		if project != "" {
			where = "(m.project='' OR m.project=?)"
			args = append(args, project)
		}
	case "global":
	case "project":
		if project == "" {
			return nil, errors.New("no current project selected")
		}
		where = "m.project=?"
		args = append(args, project)
	default:
		return nil, errors.New("scope must be global, project or all")
	}
	tokens := ftsTokens(query)
	from := "memories m"
	order := "m.updated DESC,m.id"
	if len(tokens) > 0 {
		from += " JOIN memory_fts ON memory_fts.rowid=m.rowid"
		where += " AND memory_fts MATCH ?"
		args = append(args, strings.Join(tokens, " AND "))
		order = "bm25(memory_fts),m.updated DESC,m.id"
	} else if strings.TrimSpace(query) != "" {
		return []Entry{}, nil
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, "SELECT "+excerpt+" FROM "+from+" WHERE "+where+" ORDER BY "+order+" LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	return entries(rows)
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
