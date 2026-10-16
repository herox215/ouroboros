package history

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"ouroboros/internal/storage"
)

// database opens lazily because Open historically cannot return an error. A
// failed import is rolled back and may be retried; a committed import is never
// replayed, even after deletion or a restart. Legacy files are read-only inputs.
func (a *Archive) database() (*sql.DB, error) { return a.openDatabase(true) }

// readDatabase does not create a missing archive (or its profile parents).
// Existing legacy directories still need the ordinary transactional import.
// A nil database with no error means the archive does not exist yet.
func (a *Archive) readDatabase() (*sql.DB, error) { return a.openDatabase(false) }

func (a *Archive) openDatabase(create bool) (*sql.DB, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.db != nil {
		return a.db, nil
	}
	if !create {
		info, err := os.Stat(a.dir)
		if errors.Is(err, fs.ErrNotExist) {
			// ENOENT can also mean a dangling symlink in the path. Only
			// genuine absence is an empty archive, not a broken directory.
			for path := filepath.Clean(a.dir); ; path = filepath.Dir(path) {
				_, linkErr := os.Lstat(path)
				if linkErr == nil {
					if _, statErr := os.Stat(path); statErr != nil {
						return nil, fmt.Errorf("%w: archive directory %s: %v", fs.ErrInvalid, path, statErr)
					}
					break
				}
				if !errors.Is(linkErr, fs.ErrNotExist) {
					return nil, linkErr
				}
				if filepath.Dir(path) == path {
					return nil, err
				}
			}
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("archive path is not a directory: %s", a.dir)
		}
	}
	db, err := storage.Open(a.dir)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(schema)
	if err != nil {
		return nil, err
	}
	var done int
	err = tx.QueryRow("SELECT count(*) FROM history_migrations WHERE name='legacy-v1'").Scan(&done)
	if err != nil {
		return nil, err
	}
	if done == 0 {
		if err = a.importLegacy(tx); err != nil {
			return nil, err
		}
		if _, err = tx.Exec("INSERT INTO history_migrations(name) VALUES('legacy-v1')"); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	a.db = db
	ok = true
	return db, nil
}

// Close releases this handle. The archive may be used again and reopens lazily.
// Callers must not Close concurrently with another archive operation.
func (a *Archive) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.db == nil {
		return nil
	}
	err := a.db.Close()
	a.db = nil
	return err
}

const schema = `
CREATE TABLE IF NOT EXISTS history_migrations(name TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS history_sessions(
 id TEXT PRIMARY KEY, archived INTEGER NOT NULL DEFAULT 0,
 deleted INTEGER NOT NULL DEFAULT 0, modified INTEGER NOT NULL DEFAULT 0,
 size INTEGER NOT NULL DEFAULT 0, title TEXT, effort TEXT, directory TEXT,
 goon INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS history_records(
 rowid INTEGER PRIMARY KEY, session_id TEXT NOT NULL REFERENCES history_sessions(id),
 ordinal INTEGER NOT NULL, seq INTEGER NOT NULL, answer INTEGER NOT NULL,
 data TEXT NOT NULL, search_text TEXT NOT NULL, UNIQUE(session_id,ordinal)
);
CREATE INDEX IF NOT EXISTS history_recent ON history_sessions(archived,deleted,modified DESC);
CREATE VIRTUAL TABLE IF NOT EXISTS history_fts USING fts5(search_text,content='history_records',content_rowid='rowid',tokenize='trigram');
CREATE TRIGGER IF NOT EXISTS history_records_insert AFTER INSERT ON history_records BEGIN
 INSERT INTO history_fts(rowid,search_text) VALUES(new.rowid,new.search_text);
END;
CREATE TRIGGER IF NOT EXISTS history_records_delete AFTER DELETE ON history_records BEGIN
 INSERT INTO history_fts(history_fts,rowid,search_text) VALUES('delete',old.rowid,old.search_text);
END;
`

func insertRecord(tx *sql.Tx, id string, ordinal int, r Record, b []byte) error {
	text := r.Content
	for _, c := range r.ToolCalls {
		text += "\n" + c.Name + " " + c.Arguments
	}
	_, err := tx.Exec("INSERT INTO history_records(session_id,ordinal,seq,answer,data,search_text) VALUES(?,?,?,?,?,?)", id, ordinal, r.Seq, r.Answer, string(b), strings.ToLower(text))
	return err
}

func (a *Archive) importLegacy(tx *sql.Tx) error {
	parties, err := os.ReadDir(a.dir)
	if err != nil {
		return err
	}
	for _, p := range parties {
		if !party.MatchString(p.Name()) {
			continue
		}
		if !p.IsDir() {
			return fmt.Errorf("legacy party is not a directory: %s", p.Name())
		}
		entries, err := os.ReadDir(filepath.Join(a.dir, p.Name()))
		if err != nil {
			return err
		}
		for _, e := range entries {
			ext := filepath.Ext(e.Name())
			name := strings.TrimSuffix(e.Name(), ext)
			if !named.MatchString(name) || (ext != suffix && ext != titleSuffix && ext != effortSuffix && ext != directorySuffix && ext != goOnSuffix) {
				continue
			}
			path := filepath.Join(a.dir, p.Name(), e.Name())
			id := p.Name() + "/" + name
			// Never follow symlinks out of an archive. Non-regular legacy inputs are
			// errors rather than silently marking an incomplete import as finished.
			if !e.Type().IsRegular() {
				return fmt.Errorf("legacy archive input is not a regular file: %s", path)
			}
			if _, err = tx.Exec("INSERT INTO history_sessions(id) VALUES(?) ON CONFLICT(id) DO NOTHING", id); err != nil {
				return err
			}
			if ext == suffix {
				info, err := e.Info()
				if err != nil {
					return err
				}
				ordinal := 0
				var insertErr error
				err = records(path, func(r Record) bool {
					ordinal++
					b, e := json.Marshal(r)
					if e == nil {
						e = insertRecord(tx, id, ordinal, r, b)
					}
					insertErr = e
					return e == nil
				})
				if err != nil {
					return err
				}
				if insertErr != nil {
					return insertErr
				}
				if _, err = tx.Exec("UPDATE history_sessions SET archived=1,modified=?,size=? WHERE id=?", info.ModTime().UnixNano(), info.Size(), id); err != nil {
					return err
				}
			} else {
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				column := metadataColumn(ext)
				var value any = string(b)
				if ext == goOnSuffix {
					value = 1
				}
				if _, err = tx.Exec("UPDATE history_sessions SET "+column+"=? WHERE id=?", value, id); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (a *Archive) id(path string) string {
	return filepath.Base(filepath.Dir(path)) + "/" + strings.TrimSuffix(filepath.Base(path), suffix)
}

func metadataColumn(kind string) string {
	switch kind {
	case titleSuffix:
		return "title"
	case effortSuffix:
		return "effort"
	case directorySuffix:
		return "directory"
	case goOnSuffix:
		return "goon"
	}
	panic("unknown history metadata kind")
}

func (a *Archive) setBeside(id, kind, text string) error {
	if _, err := a.path(id); err != nil {
		return err
	}
	db, err := a.database()
	if err != nil {
		return err
	}
	var value any = text
	if kind == goOnSuffix {
		value = 1
	}
	column := metadataColumn(kind)
	_, err = db.Exec("INSERT INTO history_sessions(id,"+column+") VALUES(?,?) ON CONFLICT(id) DO UPDATE SET deleted=0,"+column+"=excluded."+column, id, value)
	return err
}

func (a *Archive) records(path string, each func(Record) bool) error {
	return a.searchRecords(path, "", each)
}

// FTS is only a candidate filter: match still applies the original per-field
// Go substring semantics. Short queries (and Unicode queries, where tokenizer
// case rules may differ) fall back to an ordered SQL scan. Normalizing indexed
// text with Go's ToLower preserves existing case behavior, not SQLite's LIKE.
func (a *Archive) searchRecords(path, needle string, each func(Record) bool) error {
	db, err := a.readDatabase()
	if err != nil {
		return err
	}
	if db == nil {
		return fs.ErrNotExist
	}
	id := a.id(path)
	var exists int
	err = db.QueryRow("SELECT 1 FROM history_sessions WHERE id=? AND archived=1 AND deleted=0", id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fs.ErrNotExist
	}
	if err != nil {
		return err
	}
	query := "SELECT data FROM history_records WHERE session_id=?"
	args := []any{id}
	ascii := true
	for _, r := range needle {
		if r < 32 || r > 126 {
			ascii = false
			break
		}
	}
	if len(needle) >= 3 && ascii {
		query += " AND rowid IN (SELECT rowid FROM history_fts WHERE history_fts MATCH ?)"
		args = append(args, "\""+strings.ReplaceAll(needle, "\"", "\"\"")+"\"")
	}
	query += " ORDER BY ordinal"
	rows, err := db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b string
		if err = rows.Scan(&b); err != nil {
			return err
		}
		var r Record
		if err = json.Unmarshal([]byte(b), &r); err != nil {
			return err
		}
		if !each(r) {
			return nil
		}
	}
	return rows.Err()
}
