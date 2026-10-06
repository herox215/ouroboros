package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// outside it, also from a process it starts, to connect to another host and
// to itself on the loopback interface, and to connect to a socket of the
// file system. dirs are the writable directory, the one outside it and the
// socket.
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

	start := time.Now()
	_, err = net.DialTimeout("tcp", "192.0.2.1:80", 5*time.Second)
	try("dial another host", err)
	fmt.Printf("dial another host took long: %v\n", time.Since(start) > 4*time.Second)
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

// The sandbox lets a command and what it starts write where it may, and
// nowhere else, reach itself on the loopback interface, and nothing beyond.
func TestSandbox(t *testing.T) {
	if Nested() {
		t.Skip("in a sandbox already, where no other one starts")
	}
	if err := Available(); err != nil {
		t.Fatalf("no sandbox: %v", err)
	}
	inside, outside := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(outside, "readme"), []byte("x"), 0o600)
	// A socket of the file system, such as that of Docker, is no way out.
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("ouro-sandbox-%d.sock", os.Getpid()))
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	cmd, err := Command(context.Background(), Policy{Writable: []string{inside}}, os.Args[0], "-test.run=^$")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(cmd.Env, childEnv+"="+strings.Join([]string{inside, outside, socket}, string(os.PathListSeparator)))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got := string(out)
	t.Log(got)
	for _, want := range []string{
		"nested: true\n", "write inside: ok\n", "read outside: ok\n", "write null: ok\n", "loopback: ok\n", "dial another host took long: false\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	for _, refused := range []string{"write outside", "write from a child", "dial another host", "send to another host", "unix socket"} {
		if strings.Contains(got, refused+": ok\n") || !strings.Contains(got, refused+": ") {
			t.Errorf("%s was not refused:\n%s", refused, got)
		}
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 1 {
		t.Errorf("written outside: %v", entries)
	}
}

// What cannot be found is no command, and no sandbox starts in one.
func TestCommandFails(t *testing.T) {
	nested := Nested()
	if _, err := Command(context.Background(), Policy{Writable: []string{"relative"}}, "true"); err == nil {
		t.Error("a writable path that is not absolute should fail")
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
