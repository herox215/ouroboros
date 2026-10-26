package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/plugin"
)

type fakeBackend struct {
	status                  status
	connects, sends, closes int
	jid, text               string
	err                     error
}

func (f *fakeBackend) Connect(context.Context) (status, error) {
	f.connects++
	f.status = status{Connected: true, Paired: true}
	return f.status, f.err
}
func (f *fakeBackend) Contacts(context.Context, string, int) ([]contact, error) {
	return []contact{}, f.err
}
func (f *fakeBackend) Status() status { return f.status }
func (f *fakeBackend) Send(_ context.Context, jid, text string) (message, error) {
	f.sends++
	f.jid = jid
	f.text = text
	return message{Chat: jid, ID: "ack1", Text: text, Time: 100, FromMe: true}, f.err
}
func (f *fakeBackend) Close() { f.closes++ }
func tool(t *testing.T, s *service, name string) plugin.Tool {
	t.Helper()
	for _, tool := range s.ability().Tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatal(name)
	return plugin.Tool{}
}
func call(t *testing.T, s *service, name, args string) string {
	t.Helper()
	out, err := tool(t, s, name).Run(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestLazyOwnerToolsAndAcknowledgedSend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private")
	fake := &fakeBackend{}
	made := 0
	s := &service{path: path, makeClient: func(_ context.Context, _ string, _ *textStore) (backend, error) { made++; return fake, nil }}
	defer s.close()
	if s.ability().Name != "whatsapp" || len(s.ability().Tools) != 6 {
		t.Fatal("ability contract")
	}
	for _, tool := range s.ability().Tools {
		if !tool.OwnerOnly {
			t.Fatal(tool.Name)
		}
		_, err := tool.Run(a2a.WithOrigin(context.Background(), a2a.Origin{}), json.RawMessage(`{}`))
		if err == nil || !strings.Contains(err.Error(), "owner-only") {
			t.Fatalf("peer got %s: %v", tool.Name, err)
		}
	}
	call(t, s, "whatsapp_status", `{}`)
	if _, err := os.Stat(path); !os.IsNotExist(err) || made != 0 {
		t.Fatal("status caused initialization")
	}
	if _, err := tool(t, s, "whatsapp_write").Run(context.Background(), json.RawMessage(`{"jid":"123@s.whatsapp.net","text":"hi"}`)); err == nil {
		t.Fatal("write connected implicitly")
	}
	if got := call(t, s, "whatsapp_chats", `{}`); got != "[]" || made != 0 {
		t.Fatal(got, made)
	}
	call(t, s, "whatsapp_connect", `{}`)
	got := call(t, s, "whatsapp_write", `{"jid":"123@s.whatsapp.net","text":"exact text\n"}`)
	if !strings.Contains(got, `"server_acknowledged":true`) || !strings.Contains(got, "Not proof") || fake.jid != "123@s.whatsapp.net" || fake.text != "exact text\n" {
		t.Fatal(got, fake)
	}
	if got = call(t, s, "whatsapp_read", `{"jid":"123@s.whatsapp.net","limit":"1"}`); !strings.Contains(got, "exact text") {
		t.Fatal(got)
	}
	fake.err = errors.New("secret raw pairing token")
	_, err := tool(t, s, "whatsapp_write").Run(context.Background(), json.RawMessage(`{"jid":"123@s.whatsapp.net","text":"again"}`))
	if err == nil || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "unknown") {
		t.Fatal(err)
	}
	before := fake.sends
	for _, args := range []string{`{"jid":"123","text":"x"}`, `{"jid":"123:4@s.whatsapp.net","text":"x"}`, `{"jid":"x@evil","text":"x"}`, `{"jid":"123@s.whatsapp.net","text":" "}`} {
		if _, err := tool(t, s, "whatsapp_write").Run(context.Background(), json.RawMessage(args)); err == nil {
			t.Fatal(args)
		}
	}
	if fake.sends != before || made != 1 {
		t.Fatal("invalid operation reached backend")
	}
	s.close()
	s.close()
	if fake.closes != 1 {
		t.Fatal(fake.closes)
	}
	if _, err := tool(t, s, "whatsapp_connect").Run(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("reopened after stop")
	}
}
func TestSQLitePersistenceRetentionAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private")
	s, err := openTextStore(path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO messages VALUES(?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < retainedMessages+2; i++ {
		if _, err = stmt.Exec("123@s.whatsapp.net", fmt.Sprint(i), "123@s.whatsapp.net", "text", i, false); err != nil {
			t.Fatal(err)
		}
	}
	_ = stmt.Close()
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	m := message{Chat: "123@s.whatsapp.net", ID: "new", Text: "latest", Time: 10000}
	if err = s.put(m); err != nil {
		t.Fatal(err)
	}
	m.Text = "updated"
	if err = s.put(m); err != nil {
		t.Fatal(err)
	}
	chats, err := s.chats(1)
	if err != nil || len(chats) != 1 || chats[0].Messages != retainedMessages {
		t.Fatal(chats, err)
	}
	got, err := s.read(m.Chat, 2)
	if err != nil || len(got) != 2 || got[1].Text != "updated" || got[0].Time >= got[1].Time {
		t.Fatal(got, err)
	}
	// Exercise WAL sidecars while open, not merely the main file after close.
	if _, err = s.db.Exec(`PRAGMA journal_mode=WAL; INSERT INTO messages VALUES('456@s.whatsapp.net','wal','','wal',11000,0)`); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		fi, _ := e.Info()
		if fi.Mode().Perm() != 0600 {
			t.Fatalf("%s: %v", e.Name(), fi.Mode())
		}
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0700 {
		t.Fatal(fi.Mode())
	}
	s.close()
	s, err = openTextStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	got, err = s.read(m.Chat, 1)
	if err != nil || len(got) != 1 || got[0].Text != "updated" {
		t.Fatal(got, err)
	}
	if _, err = bounded("201", 50, 200); err == nil {
		t.Fatal("unbounded limit")
	}
}
func TestRejectStateSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "state")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(path, "messages.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := openTextStore(path); err == nil {
		t.Fatal("accepted symlink")
	}
	fi, _ := os.Stat(target)
	if fi.Mode().Perm() != 0644 {
		t.Fatal("modified link target")
	}
}
