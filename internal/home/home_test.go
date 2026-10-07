package home

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/diagnosis"
)

func TestIdentityIsGeneratedOnce(t *testing.T) {
	d := Dir(t.TempDir())
	if err := d.SaveConfig(config.Config{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	first, err := d.Identity()
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.Identity()
	if err != nil || second.Public() != first.Public() {
		t.Fatalf("identity changed: %v", err)
	}
	if cfg, _ := d.Config(); cfg.Name != "alpha" || cfg.PrivateKey != first.Private() {
		t.Fatalf("config %+v", cfg)
	}
}

func TestInvalidIdentity(t *testing.T) {
	d := Dir(t.TempDir())
	d.SaveConfig(config.Config{PrivateKey: "nonsense"})
	if _, err := d.Identity(); err == nil {
		t.Fatal("want error")
	}
}

// The node and the UI may both be the first to ask for the identity. They
// get the same one, and it is the one that is persisted.
func TestIdentityIsTheSameForEveryone(t *testing.T) {
	d := Dir(t.TempDir())
	keys := make([]string, 16)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := d.Identity()
			if err != nil {
				t.Error(err)
				return
			}
			keys[i] = id.Private()
		}()
	}
	wg.Wait()
	cfg, err := d.Config()
	if err != nil || cfg.PrivateKey == "" {
		t.Fatalf("config %+v, %v", cfg, err)
	}
	for i, key := range keys {
		if key != cfg.PrivateKey {
			t.Fatalf("caller %d got an identity that is not the persisted one", i)
		}
	}
}

// A configuration that was read before the identity existed does not erase
// the identity when it is saved, as the setup does on first run.
func TestSaveConfigKeepsIdentity(t *testing.T) {
	d := Dir(t.TempDir())
	cfg, _ := d.Config()
	id, err := d.Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Name = "alpha"
	if err := d.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Config(); got.Name != "alpha" || got.PrivateKey != id.Private() {
		t.Fatalf("config %+v", got)
	}
	if again, err := d.Identity(); err != nil || again.Public() != id.Public() {
		t.Fatalf("identity changed: %v", err)
	}
}

func TestIdentityReportsErrors(t *testing.T) {
	// A home below a file cannot be read.
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	if _, err := Dir(filepath.Join(file, "home")).Identity(); err == nil {
		t.Error("an unreadable home should fail")
	}

	// Without the lock no identity is made up: it could differ from the one
	// another process persists.
	d := Dir(t.TempDir())
	os.Mkdir(d.path("config.json.lock"), 0o700)
	if _, err := d.Identity(); err == nil {
		t.Error("a home that cannot be locked should fail")
	}
	if cfg, _ := d.Config(); cfg.PrivateKey != "" {
		t.Error("an identity was persisted without the lock")
	}
}

func TestCreate(t *testing.T) {
	d := Dir(filepath.Join(t.TempDir(), "a", "home"))
	if err := d.Create(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(string(d))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("home %v, %v", info, err)
	}
	if err := d.Create(); err != nil {
		t.Fatalf("a home that exists: %v", err)
	}
}

// Everything a node keeps is below its home.
func TestFiles(t *testing.T) {
	d := Dir(t.TempDir())
	if got := d.UpdateDir(); got != filepath.Join(string(d), "update") {
		t.Errorf("update dir %q", got)
	}
	if got := d.History(); got != filepath.Join(string(d), "history") {
		t.Errorf("history dir %q", got)
	}
	s := d.Service()
	if s.PIDFile != filepath.Join(string(d), "ouro.pid") || s.LogFile != filepath.Join(string(d), "ouro.log") ||
		len(s.Env) != 1 || s.Env[0] != "OURO_HOME="+string(d) {
		t.Errorf("service %+v", s)
	}

	d.Contacts().AddTrusted("key", "beta", "gpt")
	if trusted, _, _ := d.Contacts().State("key"); !trusted {
		t.Error("a contact should be kept")
	}
	if _, err := os.Stat(filepath.Join(string(d), "contacts.json")); err != nil {
		t.Errorf("contacts: %v", err)
	}

	if got := d.Secrets(); len(got) != 2 || got[0] != filepath.Join(string(d), "config.json") || got[1] != filepath.Join(string(d), "config.json.lock") {
		t.Errorf("secrets %v", got)
	}
	if _, err := d.Proposals().Add(config.Config{Name: "alpha"}, "name", []byte(`"beta"`), "why"); err != nil {
		t.Errorf("a proposal should be kept: %v", err)
	}
	if list, _ := d.Proposals().List(); len(list) != 1 {
		t.Errorf("proposals %v", list)
	}
	if _, err := os.Stat(filepath.Join(string(d), "proposals.json")); err != nil {
		t.Errorf("proposals: %v", err)
	}

	d.Failures().Record(diagnosis.Entry{ID: "AAAA", Peer: "key"})
	if _, ok, _ := d.Failures().Lookup("key", "AAAA"); !ok {
		t.Error("a failed answer should be kept")
	}
	if _, err := os.Stat(filepath.Join(string(d), "failures.json")); err != nil {
		t.Errorf("failures: %v", err)
	}
}

// Another process than the node adds to the log of the node.
func TestLog(t *testing.T) {
	d := Dir(t.TempDir())
	d.Log("accept %s: %v", "key", "disk full")
	d.Log("second")
	b, _ := os.ReadFile(d.Service().LogFile)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], " accept key: disk full") || !strings.HasSuffix(lines[1], " second") {
		t.Fatalf("log %q", b)
	}
	if info, _ := os.Stat(d.Service().LogFile); info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 600", info.Mode().Perm())
	}
	// A log that cannot be written is given up on.
	Dir(filepath.Join(string(d), "missing")).Log("lost")
}

// The log does not grow beyond its limit: what is in it then moves to
// ouro.log.1, which keeps one generation. The node writes through LogTo, the
// other processes through Log, and both keep the limit.
func TestLogIsRotated(t *testing.T) {
	d := Dir(t.TempDir())
	old := maxLog
	maxLog = 64
	t.Cleanup(func() { maxLog = old })
	logFile, rotated := d.Service().LogFile, filepath.Join(string(d), "ouro.log.1")
	read := func(path string) string {
		b, _ := os.ReadFile(path)
		return string(b)
	}

	// The node logs to the file it was started with, opened for appending.
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	node := d.LogTo(f)
	if n, err := node.Write([]byte(strings.Repeat("a", 63) + "\n")); n != 64 || err != nil {
		t.Fatalf("write: %d, %v", n, err)
	}
	if read(logFile) != strings.Repeat("a", 63)+"\n" {
		t.Fatalf("below the limit nothing moves: %q", read(logFile))
	}
	d.Log("first")
	if !strings.HasSuffix(read(logFile), " first\n") || strings.Count(read(logFile), "\n") != 1 || read(rotated) != strings.Repeat("a", 63)+"\n" {
		t.Fatalf("at the limit the log moves: %q, %q", read(logFile), read(rotated))
	}
	node.Write([]byte(strings.Repeat("b", 64)))
	node.Write([]byte("c\n"))
	if read(logFile) != "c\n" || !strings.HasSuffix(read(rotated), " first\n"+strings.Repeat("b", 64)) {
		t.Fatalf("the node's file goes on at its new end: %q, %q", read(logFile), read(rotated))
	}
	for _, path := range []string{logFile, rotated} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v", path, info, err)
		}
	}

	// What cannot be moved stays where it is, and the line is added.
	os.Remove(rotated)
	os.Mkdir(rotated, 0o700)
	os.WriteFile(logFile, []byte(strings.Repeat("d", 64)), 0o600)
	d.Log("kept")
	if !strings.HasPrefix(read(logFile), strings.Repeat("d", 64)) || !strings.HasSuffix(read(logFile), " kept\n") {
		t.Fatalf("a log that cannot be rotated: %q", read(logFile))
	}
	// A log that cannot be read is not rotated, and not written.
	maxLog = 0
	os.Remove(logFile)
	os.Mkdir(logFile, 0o700)
	d.Log("lost")
	// Without the lock the line is written all the same.
	other := Dir(t.TempDir())
	os.Mkdir(filepath.Join(string(other), "ouro.log.lock"), 0o700)
	os.Chmod(filepath.Join(string(other), "ouro.log.lock"), 0)
	other.Log("unlocked")
	if !strings.HasSuffix(read(other.Service().LogFile), " unlocked\n") {
		t.Fatalf("without the lock: %q", read(other.Service().LogFile))
	}
}

// The node and another process log at the same time, and the log rotates in
// between: no line is lost or torn.
func TestLogFromTwoWriters(t *testing.T) {
	d := Dir(t.TempDir())
	old := maxLog
	maxLog = 4 << 10
	t.Cleanup(func() { maxLog = old })
	f, err := os.OpenFile(d.Service().LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	node := log.New(d.LogTo(f), "", 0)
	var wg sync.WaitGroup
	const lines = 200
	wg.Go(func() {
		for i := range lines {
			node.Printf("node %03d %s", i, strings.Repeat("n", 40))
		}
	})
	wg.Go(func() {
		for i := range lines {
			d.Log("ui %03d %s", i, strings.Repeat("u", 40))
		}
	})
	wg.Wait()
	var all []string
	for _, name := range []string{"ouro.log.1", "ouro.log"} {
		b, _ := os.ReadFile(filepath.Join(string(d), name))
		all = append(all, strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")...)
	}
	last := all[len(all)-1]
	if !strings.Contains(last, " 199 ") {
		t.Fatalf("the last line is the last written: %q", last)
	}
	for _, line := range all {
		if !strings.HasSuffix(line, strings.Repeat("n", 40)) && !strings.HasSuffix(line, strings.Repeat("u", 40)) {
			t.Fatalf("torn line %q", line)
		}
	}
}

// The socket is in the directory of the node, unless that path is too long
// for a socket: then it is in a directory of the user in the temporary
// directory, named after the directory of the node.
func TestSocket(t *testing.T) {
	short := Dir("/home/luna/.config/ouroboros")
	if got := short.Socket(); got != "/home/luna/.config/ouroboros/ouro.sock" {
		t.Errorf("socket %q", got)
	}
	long := Dir("/" + strings.Repeat("a", 100))
	got := long.Socket()
	if filepath.Dir(got) != SocketDir() || !strings.HasSuffix(got, ".sock") || len(filepath.Base(got)) != 21 {
		t.Errorf("socket %q", got)
	}
	if other := Dir("/" + strings.Repeat("b", 100)).Socket(); other == got {
		t.Error("two nodes share a socket")
	}
	if again := Dir("/" + strings.Repeat("a", 100) + "/").Socket(); again != got {
		t.Errorf("the same directory has another socket: %q", again)
	}
	if want := filepath.Join(os.TempDir(), fmt.Sprintf("ouro-%d", os.Getuid())); SocketDir() != want {
		t.Errorf("socket dir %q", SocketDir())
	}
}
