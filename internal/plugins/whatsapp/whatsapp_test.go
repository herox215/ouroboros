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
	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
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

// The plugin adds the ability, keeps its state below the gateways of the
// home, and closes it when the node stops.
func TestPluginKeepsStateBelowGatewaysUntilStop(t *testing.T) {
	dir := home.Dir(t.TempDir())
	k := kernel.New()
	if err := k.Start(Plugin(dir)); err != nil {
		t.Fatal(err)
	}
	abilities := plugin.All[plugin.Ability](k.Context())
	if len(abilities) != 1 || abilities[0].Name != "whatsapp" {
		t.Fatalf("abilities: %+v", abilities)
	}
	var chats plugin.Tool
	for _, tool := range abilities[0].Tools {
		if tool.Name == "whatsapp_chats" {
			chats = tool
		}
	}
	if out, err := chats.Run(context.Background(), json.RawMessage(`{}`)); err != nil || out != "[]" {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(filepath.Join(dir.Gateways(), "whatsapp", "messages.db")); err != nil {
		t.Fatal(err)
	}
	k.Stop()
	if _, err := chats.Run(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatal(err)
	}
}

// Only bare JIDs of users, LIDs and groups pass; a group ID has at most one
// hyphen, inside.
func TestExactJID(t *testing.T) {
	for _, jid := range []string{"123@s.whatsapp.net", "456@lid", "1-2@g.us", "12@g.us"} {
		if _, err := exactJID(jid); err != nil {
			t.Errorf("%s: %v", jid, err)
		}
	}
	for _, jid := range []string{"12a@s.whatsapp.net", "1-2@s.whatsapp.net", "-12@g.us", "12-@g.us", "1-2-3@g.us", "x@evil", "123"} {
		if _, err := exactJID(jid); err == nil {
			t.Errorf("%s passed", jid)
		}
	}
}

// Every tool fails with a message of its own, and none tells what went wrong
// inside: a store that cannot be opened or read, a session that cannot be
// made, a failed connect, invalid arguments, and a send that is acknowledged
// but not kept.
func TestToolFailures(t *testing.T) {
	ctx := context.Background()
	fails := func(s *service, name, args, want string) {
		t.Helper()
		_, err := tool(t, s, name).Run(ctx, json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s %s: %v, want %q", name, args, err, want)
		}
	}
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// The state directory cannot be made below a file.
	s := &service{path: filepath.Join(file, "state"), makeClient: newClient}
	fails(s, "whatsapp_chats", `{}`, "cannot open private WhatsApp text store")
	// A session that cannot be made, by connect or by a contact search.
	s = &service{path: filepath.Join(root, "state"), makeClient: func(context.Context, string, *textStore) (backend, error) {
		return nil, errors.New("secret initialization")
	}}
	defer s.close()
	fails(s, "whatsapp_connect", `{}`, "cannot initialize private WhatsApp session")
	// A connect that fails.
	fake := &fakeBackend{err: errors.New("secret dial failure")}
	s.makeClient = func(context.Context, string, *textStore) (backend, error) { return fake, nil }
	fails(s, "whatsapp_connect", `{}`, "WhatsApp connection failed")
	// Invalid arguments.
	fails(s, "whatsapp_chats", `{"limit":"0"}`, "limit")
	fails(s, "whatsapp_read", `{"jid":"x"}`, "exact bare WhatsApp JID")
	fails(s, "whatsapp_read", `{"jid":"123@s.whatsapp.net","limit":"201"}`, "limit")
	// A store that cannot be read.
	s.store.db.Close()
	fails(s, "whatsapp_chats", `{}`, "cannot read local WhatsApp chats")
	fails(s, "whatsapp_read", `{"jid":"123@s.whatsapp.net"}`, "cannot read local WhatsApp messages")
	// Sent, but not kept.
	fake.err = nil
	got := call(t, s, "whatsapp_write", `{"jid":"123@s.whatsapp.net","text":"hi"}`)
	if !strings.Contains(got, `"server_acknowledged":true`) || !strings.Contains(got, "do not resend") {
		t.Fatal(got)
	}
	// Stopped: nothing opens the store again.
	s.close()
	fails(s, "whatsapp_contacts", `{"query":"Kim"}`, "stopped")
	fails(s, "whatsapp_chats", `{}`, "stopped")
	fails(s, "whatsapp_read", `{"jid":"123@s.whatsapp.net"}`, "stopped")
}

// failChmod makes chmod fail for the paths it is asked to, and do nothing
// for the others.
func failChmod(t *testing.T, fail func(string) bool) {
	t.Helper()
	old := chmod
	t.Cleanup(func() { chmod = old })
	chmod = func(path string, mode os.FileMode) error {
		if fail(path) {
			return os.ErrPermission
		}
		return old(path, mode)
	}
}

// The private directory is refused when it is no directory of its own, when
// its permissions cannot be set, and when it cannot be listed.
func TestPrivateDirFailures(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(filepath.Join(file, "state")); err == nil {
		t.Fatal("made a directory below a file")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := privateDir(link); err == nil || err.Error() != "private directory required" {
		t.Fatalf("symlink: %v", err)
	}
	dir := filepath.Join(root, "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "messages.db"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	t.Run("directory", func(t *testing.T) {
		failChmod(t, func(path string) bool { return path == dir })
		if err := privateDir(dir); !errors.Is(err, os.ErrPermission) {
			t.Fatal(err)
		}
	})
	t.Run("entry", func(t *testing.T) {
		failChmod(t, func(path string) bool { return path != dir })
		if err := privateDir(dir); !errors.Is(err, os.ErrPermission) {
			t.Fatal(err)
		}
	})
	t.Run("unlisted", func(t *testing.T) {
		// A directory that keeps a mode without reading.
		failChmod(t, func(string) bool { return false })
		chmod = func(string, os.FileMode) error { return nil }
		if err := os.Chmod(dir, 0300); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(dir, 0700)
		if err := privateDir(dir); err == nil {
			t.Fatal("listed an unreadable directory")
		}
	})
}

// A database file is refused when it is no regular file, when it cannot be
// made or its permissions set, and when it holds no database.
func TestPrivateDBFailures(t *testing.T) {
	root := t.TempDir()
	if _, err := privateDB(root); err == nil || err.Error() != "regular database required" {
		t.Fatalf("directory: %v", err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("not a database, but long enough to be read as a header of one"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := privateDB(filepath.Join(file, "x.db")); err == nil {
		t.Fatal("database below a file")
	}
	if _, err := privateDB(filepath.Join(root, "missing", "x.db")); err == nil {
		t.Fatal("database in a missing directory")
	}
	if _, err := privateDB(file); err == nil {
		t.Fatal("opened what is no database")
	}
	old := fchmod
	t.Cleanup(func() { fchmod = old })
	fchmod = func(*os.File, os.FileMode) error { return os.ErrPermission }
	if _, err := privateDB(filepath.Join(root, "new.db")); !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
}

// storeWith is a text store whose database was made by setup first.
func storeWith(t *testing.T, setup string) *textStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state")
	if err := privateDir(path); err != nil {
		t.Fatal(err)
	}
	db, err := privateDB(filepath.Join(path, "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(setup); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	s, err := openTextStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	return s
}

// The text store fails as its database does: one that holds no database or
// a table that does not fit, values of the wrong kind, and a closed one.
// Messages without text, ID or exact chat are not kept.
func TestTextStoreFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	if err := privateDir(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "messages.db"), []byte("not a database, but long enough to be read as a header of one"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openTextStore(path); err == nil {
		t.Fatal("opened what is no database")
	}
	path = filepath.Join(t.TempDir(), "state")
	if err := privateDir(path); err != nil {
		t.Fatal(err)
	}
	db, err := privateDB(filepath.Join(path, "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE messages(x)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := openTextStore(path); err == nil {
		t.Fatal("indexed a table without time")
	}

	// Without rowid, nothing old can be let go of, and nothing read.
	s := storeWith(t, `CREATE TABLE messages(chat TEXT NOT NULL, id TEXT NOT NULL, sender TEXT NOT NULL, text TEXT NOT NULL CHECK(text <> 'refused'),
 time INTEGER NOT NULL, from_me INTEGER NOT NULL, PRIMARY KEY(chat,id)) WITHOUT ROWID`)
	ignored := []message{{Chat: "123@s.whatsapp.net", ID: "no text"}, {Chat: "123@s.whatsapp.net", Text: "no ID"}, {Chat: "x", ID: "x", Text: "no exact chat"}}
	if err := s.put(ignored...); err == nil {
		t.Fatal("let go of old messages without rowid")
	}
	if err := s.put(message{Chat: "123@s.whatsapp.net", ID: "1", Text: "refused"}); err == nil {
		t.Fatal("kept a refused text")
	}
	if _, err := s.read("123@s.whatsapp.net", 1); err == nil {
		t.Fatal("read without rowid")
	}

	// A time that is no number cannot be read.
	s = storeWith(t, `CREATE TABLE messages(chat TEXT NOT NULL, id TEXT NOT NULL, sender TEXT NOT NULL, text TEXT NOT NULL,
 time INTEGER NOT NULL, from_me INTEGER NOT NULL, PRIMARY KEY(chat,id));
 INSERT INTO messages VALUES('123@s.whatsapp.net','1','','text','never',0)`)
	if _, err := s.read("123@s.whatsapp.net", 1); err == nil {
		t.Fatal("read a time that is no number")
	}
	if _, err := s.chats(1); err == nil {
		t.Fatal("listed a time that is no number")
	}
	s.db.Close()
	if err := s.put(message{Chat: "123@s.whatsapp.net", ID: "1", Text: "late"}); err == nil {
		t.Fatal("kept a text in a closed store")
	}
	if _, err := s.chats(1); err == nil {
		t.Fatal("listed a closed store")
	}
}
