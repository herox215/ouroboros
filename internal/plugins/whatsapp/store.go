package whatsapp

import (
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

const retainedMessages = 5000

// maxText is how many bytes of a text are kept.
const maxText = 16000

type message struct {
	Chat   string `json:"chat"`
	ID     string `json:"id"`
	Sender string `json:"sender"`
	Text   string `json:"text"`
	Time   int64  `json:"time"`
	FromMe bool   `json:"from_me"`
}
type textStore struct {
	mu sync.Mutex
	db *sql.DB
}

func privateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("private directory required")
	}
	if err = os.Chmod(path, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, e := range entries {
		fi, err = e.Info()
		if err != nil {
			return err
		}
		if !fi.Mode().IsRegular() {
			return errors.New("unexpected private state entry")
		}
		if err = os.Chmod(filepath.Join(path, e.Name()), 0600); err != nil {
			return err
		}
	}
	return nil
}

// SQLite derives journal/WAL/SHM permissions from the precreated 0600 database.
// No process-wide umask changes are needed.
func privateDB(path string) (*sql.DB, error) {
	fi, err := os.Lstat(path)
	if err == nil && !fi.Mode().IsRegular() {
		return nil, errors.New("regular database required")
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = f.Chmod(0600)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
func openTextStore(path string) (*textStore, error) {
	if err := privateDir(path); err != nil {
		return nil, err
	}
	db, err := privateDB(filepath.Join(path, "messages.db"))
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS messages (
 chat TEXT NOT NULL, id TEXT NOT NULL, sender TEXT NOT NULL, text TEXT NOT NULL,
 time INTEGER NOT NULL, from_me INTEGER NOT NULL, PRIMARY KEY(chat,id));
 CREATE INDEX IF NOT EXISTS messages_time ON messages(time);`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &textStore{db: db}, nil
}
func (s *textStore) close() { s.mu.Lock(); defer s.mu.Unlock(); _ = s.db.Close() }

// put keeps messages, in one transaction, and lets go of the oldest beyond
// retainedMessages.
func (s *textStore) put(messages ...message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, m := range messages {
		if m.Text == "" || m.ID == "" {
			continue
		}
		if _, err := exactJID(m.Chat); err != nil {
			continue
		}
		m.Text = capped(m.Text, maxText)
		_, err = tx.Exec(`INSERT INTO messages(chat,id,sender,text,time,from_me) VALUES(?,?,?,?,?,?) ON CONFLICT(chat,id) DO UPDATE SET text=excluded.text`, m.Chat, m.ID, m.Sender, m.Text, m.Time, m.FromMe)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(`DELETE FROM messages WHERE rowid IN (SELECT rowid FROM messages ORDER BY time DESC,rowid DESC LIMIT -1 OFFSET ?)`, retainedMessages)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// capped is text cut to at most n bytes, at the start of a character.
func capped(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

func (s *textStore) read(jid string, n int) ([]message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT chat,id,sender,text,time,from_me FROM (SELECT rowid AS seq,* FROM messages WHERE chat=? ORDER BY time DESC,rowid DESC LIMIT ?) ORDER BY time,seq`, jid, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []message{}
	for rows.Next() {
		var m message
		if err = rows.Scan(&m.Chat, &m.ID, &m.Sender, &m.Text, &m.Time, &m.FromMe); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type chat struct {
	JID      string `json:"jid"`
	LastTime int64  `json:"last_time"`
	Messages int    `json:"retained_text_messages"`
}

func (s *textStore) chats(n int) ([]chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT chat,MAX(time),COUNT(*) FROM messages GROUP BY chat ORDER BY MAX(time) DESC,chat LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chat{}
	for rows.Next() {
		var c chat
		if err = rows.Scan(&c.JID, &c.LastTime, &c.Messages); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
