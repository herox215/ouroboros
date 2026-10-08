package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// childEnv makes the test binary the command in the sandbox: it tries what
// the sandbox has to refuse or allow and prints how it went, see child.
const childEnv = "OURO_SANDBOX_CHILD"

func TestMain(m *testing.M) {
	if dirs := os.Getenv(childEnv); dirs != "" {
		child(strings.Split(dirs, string(os.PathListSeparator)))
		return
	}
	os.Exit(m.Run())
}

// child tries, in the sandbox: to write in the writable directory and
// outside it, also from a process it starts, to write what is read only in
// it and to create what is read only and missing, to read and write what is
// hidden, to connect to another host and to itself on the loopback
// interface, and to connect to a socket of the file system. dirs are the
// writable directory, the one outside it and the socket.
func child(dirs []string) {
	try := func(what string, err error) {
		if err != nil {
			fmt.Printf("%s: %v\n", what, err)
		} else {
			fmt.Printf("%s: ok\n", what)
		}
	}
	inside, outside, socket := dirs[0], dirs[1], dirs[2]
	fmt.Printf("nested: %v\n", Nested())
	try("write inside", os.WriteFile(filepath.Join(inside, "file"), []byte("x"), 0o600))
	try("write outside", os.WriteFile(filepath.Join(outside, "file"), []byte("x"), 0o600))
	try("write from a child", exec.Command("sh", "-c", "echo x > "+filepath.Join(outside, "child")).Run())
	_, err := os.ReadFile(filepath.Join(outside, "readme"))
	try("read outside", err)
	try("write null", os.WriteFile(os.DevNull, []byte("x"), 0))

	try("write next to the missing", os.WriteFile(filepath.Join(inside, "sub", "existing"), []byte("y"), 0o600))
	try("write read only", os.WriteFile(filepath.Join(inside, "ro", "file"), []byte("x"), 0o600))
	try("write read only from a child", exec.Command("sh", "-c", "echo x > "+filepath.Join(inside, "ro", "child")).Run())
	_, err = os.ReadFile(filepath.Join(inside, "ro", "readme"))
	try("read read only", err)
	try("create missing", os.Mkdir(filepath.Join(inside, "sub", "missing"), 0o700))
	try("create below missing", os.MkdirAll(filepath.Join(inside, "sub", "missing", "deeper"), 0o700))
	try("write below read only", os.WriteFile(filepath.Join(inside, "ro", "writable", "file"), []byte("x"), 0o600))
	try("replace read only link", os.Remove(filepath.Join(inside, "sub", "go")))
	try("write through read only link", os.WriteFile(filepath.Join(inside, "sub", "go"), []byte("x"), 0o600))
	secret, err := os.ReadFile(filepath.Join(inside, "secret"))
	if err == nil && len(secret) > 0 {
		err = fmt.Errorf("read %q", secret)
	}
	try("read hidden", err)
	try("write hidden", os.WriteFile(filepath.Join(inside, "secret"), []byte("x"), 0o600))
	secret, err = os.ReadFile(filepath.Join(inside, "secrets", "key"))
	if err == nil && len(secret) > 0 {
		err = fmt.Errorf("read %q", secret)
	}
	try("read hidden directory", err)
	try("write hidden directory", os.WriteFile(filepath.Join(inside, "secrets", "new"), []byte("x"), 0o600))

	start := time.Now()
	_, err = net.DialTimeout("tcp", "192.0.2.1:80", 2*time.Second)
	try("dial another host", err)
	fmt.Printf("dial another host took long: %v\n", time.Since(start) > time.Second)
	fmt.Printf("dial another host refused: %v\n", errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENETUNREACH))
	conn, err := net.Dial("udp", "192.0.2.1:53")
	if err == nil {
		_, err = conn.Write([]byte("x"))
		conn.Close()
	}
	try("send to another host", err)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err == nil {
		var c net.Conn
		c, err = net.Dial("tcp", l.Addr().String())
		if err == nil {
			c.Close()
		}
		l.Close()
	}
	try("loopback", err)
	_, err = net.Dial("unix", socket)
	try("unix socket", err)
}

// run runs the child in the sandbox of p and returns what it printed. The
// writable directory holds what is read only and hidden in p, see child.
func runChild(t *testing.T, network bool) (string, string, string) {
	t.Helper()
	if Nested() {
		t.Skip("in a sandbox already, where no other one starts")
	}
	if err := Available(); err != nil {
		t.Fatalf("no sandbox: %v", err)
	}
	inside, outside := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(outside, "readme"), []byte("x"), 0o600)
	for _, dir := range []string{"sub", "ro/writable", "secrets"} {
		os.MkdirAll(filepath.Join(inside, dir), 0o700)
	}
	os.WriteFile(filepath.Join(inside, "sub", "existing"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(inside, "tool"), []byte("the tool"), 0o700)
	os.Symlink(filepath.Join(inside, "tool"), filepath.Join(inside, "sub", "go"))
	os.WriteFile(filepath.Join(inside, "ro", "readme"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(inside, "secret"), []byte("the secret"), 0o600)
	os.WriteFile(filepath.Join(inside, "secrets", "key"), []byte("the key"), 0o600)
	// A socket of the file system, such as that of Docker, is no way out.
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("ouro-sandbox-%d.sock", os.Getpid()))
	os.Remove(socket)
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	p := Policy{
		Writable: []string{inside},
		// Within it, as a policy names them: one of them through a link.
		ReadOnly: []string{filepath.Join(inside, "ro"), filepath.Join(inside, "sub", "missing"), filepath.Join(inside, "sub", "go")},
		Hidden:   []string{filepath.Join(inside, "secret"), filepath.Join(inside, "secrets"), filepath.Join(inside, "none")},
		Network:  network,
	}
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(inside, link)
	p.ReadOnly[0] = filepath.Join(link, "ro")
	cmd, err := Command(context.Background(), p, os.Args[0], "-test.run=^$")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(cmd.Env, childEnv+"="+strings.Join([]string{inside, outside, socket}, string(os.PathListSeparator)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	t.Log(string(out))
	return string(out), inside, outside
}

// check fails t unless got has every line of want and none of refused is ok.
func check(t *testing.T, got string, want, refused []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("lacks %q:\n%s", w, got)
		}
	}
	for _, r := range refused {
		if strings.Contains(got, r+": ok\n") || !strings.Contains(got, r+": ") {
			t.Errorf("%s was not refused:\n%s", r, got)
		}
	}
}

// The sandbox lets a command and what it starts write where it may, and
// nowhere else, keeps what is read only as it is, does not show what is
// hidden, and lets it reach itself on the loopback interface, and nothing
// beyond.
func TestSandbox(t *testing.T) {
	got, inside, outside := runChild(t, false)
	check(t, got, []string{
		"nested: true\n", "write inside: ok\n", "read outside: ok\n", "write null: ok\n", "loopback: ok\n",
		"dial another host took long: false\n", "dial another host refused: true\n",
		"write next to the missing: ok\n", "read read only: ok\n",
	}, []string{
		"write outside", "write from a child", "dial another host", "send to another host", "unix socket",
		"write read only", "write read only from a child", "create missing", "create below missing", "write below read only",
		"replace read only link", "write through read only link",
		"read hidden", "write hidden", "read hidden directory", "write hidden directory",
	})
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Errorf("written outside: %v", entries)
	}
	if b, _ := os.ReadFile(filepath.Join(inside, "secret")); string(b) != "the secret" {
		t.Errorf("the hidden file is %q", b)
	}
	if entries, _ := os.ReadDir(filepath.Join(inside, "secrets")); len(entries) != 1 {
		t.Errorf("written to the hidden directory: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(inside, "sub", "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("what is read only and was missing: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(inside, "sub", "go")); string(b) != "the tool" {
		t.Errorf("behind the read only link: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(inside, "sub", "existing")); string(b) != "y" {
		t.Errorf("next to what is missing: %q", b)
	}
}

// With network the command reaches other hosts, as far as the network of the
// machine goes, and still no socket of the file system; the rest of the
// policy holds.
func TestSandboxNetwork(t *testing.T) {
	got, _, _ := runChild(t, true)
	check(t, got, []string{"write inside: ok\n", "loopback: ok\n", "dial another host refused: false\n"},
		[]string{"write outside", "write read only", "create missing", "read hidden", "unix socket"})
}

// What cannot be found is no command, and no sandbox starts in one.
func TestCommandFails(t *testing.T) {
	nested := Nested()
	for _, p := range []Policy{{Writable: []string{"relative"}}, {ReadOnly: []string{"relative"}}, {Hidden: []string{"relative"}}} {
		if _, err := Command(context.Background(), p, "true"); err == nil || !strings.Contains(err.Error(), "not absolute") {
			t.Errorf("a path that is not absolute in %+v: %v", p, err)
		}
	}
	t.Setenv(Env, "1")
	if !Nested() {
		t.Fatal("not nested")
	}
	if _, err := Command(context.Background(), Policy{}, "true"); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "do not nest") {
		t.Errorf("a sandbox in a sandbox: %v", err)
	}
	if nested {
		return // the rest needs a sandbox
	}
	t.Setenv(Env, "")
	if _, err := Command(context.Background(), Policy{Writable: []string{filepath.Join(t.TempDir(), "missing")}}, "true"); err == nil {
		t.Error("a writable path that does not exist should fail")
	}
	if _, err := Command(context.Background(), Policy{}, filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a command that does not exist: %v", err)
	}
}

// A path leads where the directory above it leads; one that does not exist
// is taken as it is written below the nearest one that does, and a link
// counts as itself and as where it leads.
func TestReals(t *testing.T) {
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)
	links := t.TempDir()
	realLinks, _ := filepath.EvalSymlinks(links)
	link := filepath.Join(links, "link")
	os.Symlink(dir, link)
	os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "dangling"))
	locked := filepath.Join(dir, "locked")
	os.Mkdir(locked, 0)
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	got := reals([]string{link, filepath.Join(link, "a", "b"), "/", filepath.Join(dir, "dangling", "x"), filepath.Join(locked, "x", "y")})
	want := []string{
		filepath.Join(realLinks, "link"), real,
		filepath.Join(real, "a", "b"),
		"/",
		filepath.Join(real, "dangling", "x"),
		filepath.Join(real, "locked", "x", "y"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("reals:\n%q\nwant\n%q", got, want)
	}
	if _, err := Command(context.Background(), Policy{Writable: []string{filepath.Join(dir, "missing")}}, "true"); err == nil && !Nested() {
		t.Error("a writable path that is missing should fail")
	}
}
