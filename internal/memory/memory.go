// Package memory stores only explicit user memories, not inferred facts or transcripts.
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
)

var (
	ErrNotFound = errors.New("memory not found in this scope")
	ErrConflict = errors.New("memory version conflict; read it again before changing it")
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
}

type Store struct {
	db    *sql.DB
	owned bool
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
	s.owned = true
	return s, nil
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

const columns = "id,key,content,project,version,created,updated,source"

type scanner interface{ Scan(...any) error }

func scan(row scanner) (e Entry, err error) {
	err = row.Scan(&e.ID, &e.Key, &e.Content, &e.Project, &e.Version, &e.Created, &e.Updated, &e.Source)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

func (s *Store) Create(ctx context.Context, project, key, content, source string) (Entry, error) {
	if err := validate(key, content, source); err != nil {
		return Entry{}, err
	}
	var id [16]byte
	rand.Read(id[:]) // never fails
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e := Entry{ID: hex.EncodeToString(id[:]), Key: key, Content: content, Project: project, Version: 1, Created: now, Updated: now, Source: source}
	err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO memories ("+columns+") VALUES(?,?,?,?,?,?,?,?)", e.ID, e.Key, e.Content, e.Project, e.Version, e.Created, e.Updated, e.Source)
		return err
	})
	if err != nil {
		return Entry{}, err
	}
	return e, nil
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
// statement: roll back both the row and its index before trying again.
func (s *Store) write(ctx context.Context, f func(*sql.Tx) error) error {
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
	tokens := strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	from := "memories m"
	order := "m.updated DESC,m.id"
	if len(tokens) > 0 {
		for i, t := range tokens {
			tokens[i] = "\"" + strings.ReplaceAll(t, "\"", "\"\"") + "\""
		}
		from += " JOIN memory_fts ON memory_fts.rowid=m.rowid"
		where += " AND memory_fts MATCH ?"
		args = append(args, strings.Join(tokens, " AND "))
		order = "bm25(memory_fts),m.updated DESC,m.id"
	} else if strings.TrimSpace(query) != "" {
		return []Entry{}, nil
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, "SELECT m.id,m.key,substr(m.content,1,256),m.project,m.version,m.created,m.updated,m.source FROM "+from+" WHERE "+where+" ORDER BY "+order+" LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
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
