package memory

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
)

// StaleLoads is what a memory that is loaded and never used comes to: from
// that many loads on it is chosen for no topic, see active, and Tidy then
// archives it, which only a search finds anymore. Core memories are loaded
// with every answer, and not counted.
const StaleLoads = 10

// excerpt are the columns of a memory with at most 256 characters of its
// content, as a search or a topic gets it.
const excerpt = "m.id,m.key,substr(m.content,1,256),m.project,m.version,m.created,m.updated,m.source,m.core,m.expires,m.loaded,m.used,m.archived"

// current is what a memory m that is neither archived nor expired at the
// time it is given is.
const current = "archived=0 AND (expires='' OR expires>?)"

// now is the time as Entry.Expires has it.
func now() string { return time.Now().UTC().Format(time.RFC3339) }

// active is what a memory m that a topic may be given is: no core one, as
// those are given anyway, current and not stale, global or of project.
const active = "m.core=0 AND m.archived=0 AND (m.expires='' OR m.expires>?) AND NOT (m.used=0 AND m.loaded>=?) AND (m.project='' OR m.project=?)"

// rank orders memories by how often they were used for how often they were
// loaded, the latest first among the same.
const rank = "(m.used+1.0)/(m.loaded+1.0) DESC,m.updated DESC,m.id"

// OfProject returns at most limit memories of project that a topic may be
// given, see active, the best ranked first: none without a project.
func (s *Store) OfProject(ctx context.Context, project string, limit int) ([]Entry, error) {
	if project == "" {
		return []Entry{}, nil
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+excerpt+" FROM memories m WHERE "+active+" AND m.project<>'' ORDER BY "+rank+" LIMIT ?", now(), StaleLoads, project, limit)
	if err != nil {
		return nil, err
	}
	return entries(rows)
}

// Related returns at most limit memories, global or of project, that a topic
// may be given and that have any of words, the best match first: none
// without words.
func (s *Store) Related(ctx context.Context, project string, words []string, limit int) ([]Entry, error) {
	var tokens []string
	for _, w := range words {
		for _, t := range strings.FieldsFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
			tokens = append(tokens, "\""+t+"\"")
		}
	}
	if len(tokens) == 0 {
		return []Entry{}, nil
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+excerpt+" FROM memories m JOIN memory_fts ON memory_fts.rowid=m.rowid WHERE memory_fts MATCH ? AND "+active+" ORDER BY bm25(memory_fts),m.id LIMIT ?",
		strings.Join(tokens, " OR "), now(), StaleLoads, project, limit)
	if err != nil {
		return nil, err
	}
	return entries(rows)
}

// Keys returns the keys of at most limit memories, global or of project,
// that are neither core ones nor archived nor expired, the most used first:
// what a topic is told there is to be found.
func (s *Store) Keys(ctx context.Context, project string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+excerpt+" FROM memories m WHERE m.core=0 AND m."+current+" AND (m.project='' OR m.project=?) ORDER BY m.used DESC,m.updated DESC,m.id LIMIT ?", now(), project, limit)
	if err != nil {
		return nil, err
	}
	found, err := entries(rows)
	keys := make([]string, len(found))
	for i, e := range found {
		keys[i] = e.Key
	}
	return keys, err
}

// Loaded counts that the memories ids were put into the prompt of a topic,
// and Used that the agent read or found them. Neither changes their version.
func (s *Store) Loaded(ctx context.Context, ids []string) error { return s.count(ctx, "loaded", ids) }
func (s *Store) Used(ctx context.Context, ids []string) error   { return s.count(ctx, "used", ids) }

func (s *Store) count(ctx context.Context, column string, ids []string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, "UPDATE memories SET "+column+"="+column+"+1 WHERE id=?", id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Tidy archives the memories that expired, and those that are no core ones
// and were loaded StaleLoads times and never used: a stale one is chosen for
// no topic anymore, so nothing counts its loads up to a later bound. It
// returns how many.
func (s *Store) Tidy(ctx context.Context) (int64, error) {
	var n int64
	err := s.write(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, "UPDATE memories SET archived=1 WHERE archived=0 AND ((expires<>'' AND expires<=?) OR (core=0 AND used=0 AND loaded>=?))", now(), StaleLoads)
		if err == nil {
			n, err = r.RowsAffected()
		}
		return err
	})
	return n, err
}

// Active returns at most limit memories of every scope that are neither
// core ones nor archived nor expired, by scope and key: what a consolidation
// looks at.
func (s *Store) Active(ctx context.Context, limit int) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+excerpt+" FROM memories m WHERE m.core=0 AND m."+current+" ORDER BY m.project,m.key LIMIT ?", now(), limit)
	if err != nil {
		return nil, err
	}
	return entries(rows)
}

// Archive archives the memories ids of project, as a consolidation does with
// those it merged into one.
func (s *Store) Archive(ctx context.Context, project string, ids []string) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, "UPDATE memories SET archived=1 WHERE id=? AND project=?", id, project); err != nil {
				return err
			}
		}
		return nil
	})
}

// Mark keeps when what name names last happened, as a consolidation; Marked
// returns it, the zero time if never.
func (s *Store) Mark(ctx context.Context, name string, at time.Time) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO memory_meta(name,value) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET value=excluded.value", name, at.UTC().Format(time.RFC3339Nano))
		return err
	})
}

func (s *Store) Marked(ctx context.Context, name string) (time.Time, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM memory_meta WHERE name=?", name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339Nano, value)
}
