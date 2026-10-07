package node

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ouroboros/internal/daemon"
	"ouroboros/internal/home"
)

// local is a client of the socket at path. A node holds its PID file before
// it listens on its socket: a socket that does not answer yet is tried again
// for a while.
func local(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		for deadline := time.Now().Add(patience); ; time.Sleep(5 * time.Millisecond) {
			var d net.Dialer
			conn, err := d.DialContext(ctx, "unix", path)
			if err == nil || time.Now().After(deadline) {
				return conn, err
			}
		}
	}}}
}

// get asks the socket at path for what it serves.
func get(t *testing.T, path string) string {
	t.Helper()
	resp, err := local(path).Get("http://ouro/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// hello serves "hello from " and name.
func hello(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "hello from "+name) })
}

// The node serves its front ends on a socket only its user can open, which
// is gone when the node stops; one without a handler for it serves nothing.
func TestRunServesTheSocket(t *testing.T) {
	dir := configured(t)
	s := testServer(dir)
	s.Local = hello("the node")
	_, stop := run(t, s)
	path := dir.Socket()
	if got := get(t, path); got != "hello from the node" {
		t.Fatalf("got %q", got)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 || info.Mode().Type() != os.ModeSocket {
		t.Fatalf("socket %v, %v", info, err)
	}
	if dirInfo, err := os.Stat(filepath.Dir(path)); err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory %v, %v", dirInfo, err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the socket should be gone: %v", err)
	}

	s.Local = nil
	run(t, s)
	if got := get(t, path); !strings.Contains(got, "404") {
		t.Fatalf("got %q", got)
	}
}

// A node that is replaced by a new build hands over its PID file and its
// socket: no other node starts in between, and a front end that connects
// meanwhile is answered by the new node.
func TestRunHandsOver(t *testing.T) {
	dir := configured(t)
	var logged bytes.Buffer
	s := testServer(dir)
	s.Log = &logged
	s.Local = hello("the old node")
	successor := ""
	s.Successor = func() string { return successor }
	_, stop := run(t, s)
	successor = "/new/ouro"
	err := stop()
	succession, ok := errors.AsType[*Succession](err)
	if !ok || succession.Exe != "/new/ouro" || err.Error() != "the node is handed over to /new/ouro" || len(succession.Env) != 2 {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(logged.String(), "stopped, handing over to /new/ouro") {
		t.Fatalf("log:\n%s", logged.String())
	}
	if _, ok := dir.Service().Running(); !ok {
		t.Fatal("the PID file is held until the new node takes it")
	}
	if _, err := daemon.Hold(dir.Service().PIDFile, 1); !errors.Is(err, daemon.ErrRunning) {
		t.Fatalf("no other node gets the PID file: %v", err)
	}
	// A front end that connects meanwhile waits in the socket.
	answered := make(chan string, 1)
	go func() {
		resp, err := local(dir.Socket()).Get("http://ouro/")
		if err != nil {
			answered <- err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		answered <- string(b)
	}()
	time.Sleep(50 * time.Millisecond)

	// The new node, here in the same process.
	for _, kv := range succession.Env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	s.Local, s.Successor = hello("the new node"), nil
	p, _ := run(t, s)
	if p.PID != os.Getpid() {
		t.Fatalf("process %+v", p)
	}
	select {
	case got := <-answered:
		if got != "hello from the new node" {
			t.Fatalf("got %q", got)
		}
	case <-time.After(patience):
		t.Fatal("the front end that waited should be answered")
	}
	for _, k := range []string{socketEnv, daemon.InheritEnv} {
		if _, ok := os.LookupEnv(k); ok {
			t.Errorf("%s should be taken", k)
		}
	}
}

// A node whose PID file cannot be handed over says so; one whose socket
// cannot be, hands over the PID file alone, and the new node opens the
// socket anew.
func TestRunHandsOverWhatItCan(t *testing.T) {
	dir := configured(t)
	s := testServer(dir)
	s.Successor = func() string { return "/new/ouro" }
	replace(t, &bequeath, func(*daemon.Lease) (string, error) { return "", errTest })
	_, stop := run(t, s)
	if err := stop(); !errors.Is(err, errTest) || !strings.HasPrefix(err.Error(), "the node cannot be handed over: ") {
		t.Fatalf("got %v", err)
	}
	if _, ok := dir.Service().Running(); ok {
		t.Fatal("a node that is not handed over gives up its PID file")
	}

	bequeath = (*daemon.Lease).Bequeath
	replace(t, &dup, func(int) (int, error) { return 0, errTest })
	var logged bytes.Buffer
	s.Log = &logged
	_, stop = run(t, s)
	err := stop()
	succession, ok := errors.AsType[*Succession](err)
	if !ok || len(succession.Env) != 1 || !strings.HasPrefix(succession.Env[0], daemon.InheritEnv+"=") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(logged.String(), "the socket is not handed over, the new node opens it anew: test error") {
		t.Fatalf("log:\n%s", logged.String())
	}
	k, v, _ := strings.Cut(succession.Env[0], "=")
	t.Setenv(k, v)
	s.Successor, s.Local = nil, hello("the new node")
	run(t, s)
	if got := get(t, dir.Socket()); got != "hello from the new node" {
		t.Fatalf("got %q", got)
	}
}

// A socket that cannot be opened keeps the node from starting, and leaves
// nothing behind.
func TestRunWithoutSocket(t *testing.T) {
	dir := configured(t)
	// Something that is no socket is in the way, and is not removed.
	if err := os.MkdirAll(filepath.Join(dir.Socket(), "full"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := testServer(dir)
	if err := s.Run(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "the socket of the node: ") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(dir.Service().PIDFile); !os.IsNotExist(err) {
		t.Fatal("a node that did not start should leave no PID file")
	}
}

// The directory of the socket is the user's alone.
func TestListenLocal(t *testing.T) {
	base := t.TempDir()
	// One that is too open is closed.
	open := filepath.Join(base, "open")
	os.Mkdir(open, 0o755)
	ln, err := listenLocal(filepath.Join(open, "s"))
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	if info, _ := os.Stat(open); info.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", info.Mode())
	}
	// A link to a directory is none of the user's own.
	link := filepath.Join(base, "link")
	os.Symlink(open, link)
	if _, err := listenLocal(filepath.Join(link, "s")); err == nil || !strings.Contains(err.Error(), "is not a directory of this user") {
		t.Fatalf("a link: %v", err)
	}
	// A file is in the way of the directory.
	file := filepath.Join(base, "file")
	os.WriteFile(file, nil, 0o600)
	if _, err := listenLocal(filepath.Join(file, "dir", "s")); err == nil {
		t.Fatal("a file in the way")
	}
	// A path too long for a socket.
	if _, err := listenLocal(filepath.Join(open, strings.Repeat("s", 120))); err == nil {
		t.Fatal("a path too long")
	}
	// A socket whose mode cannot be set is given up.
	replace(t, &chmod, func(string, os.FileMode) error { return errTest })
	if _, err := listenLocal(filepath.Join(open, "s")); !errors.Is(err, errTest) {
		t.Fatalf("got %v", err)
	}
}

// What is handed on as the socket of a node, but is none, is left alone, and
// the socket is opened anew.
func TestInheritedSocket(t *testing.T) {
	path := home.Dir(t.TempDir()).Socket()
	os.MkdirAll(filepath.Dir(path), 0o700)
	file, _ := os.Create(filepath.Join(t.TempDir(), "file"))
	defer file.Close()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	udpFile, _ := udp.(*net.UDPConn).File()
	defer udpFile.Close()
	other, err := net.Listen("unix", filepath.Join(t.TempDir(), "o"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherFile, _ := other.(*net.UnixListener).File()
	defer otherFile.Close()
	// What is handed on is the new node's: it may close it.
	handed := func(f *os.File) string {
		fd, err := syscall.Dup(int(f.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(fd)
	}
	for name, v := range map[string]string{
		"no number":      "x",
		"no descriptor":  "-1",
		"a file":         fmt.Sprint(file.Fd()),
		"no stream":      handed(udpFile),
		"another socket": handed(otherFile),
	} {
		t.Setenv(socketEnv, v)
		ln, err := listenLocal(path)
		if err != nil || ln.Addr().String() != path {
			t.Fatalf("%s: %v, %v", name, ln, err)
		}
		ln.Close()
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("a file should be left alone: %v", err)
	}
}

// A socket that is closed already cannot be handed on.
func TestBequeathSocket(t *testing.T) {
	ln, err := listenLocal(home.Dir(t.TempDir()).Socket())
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	if _, err := bequeathSocket(ln); err == nil {
		t.Fatal("want error")
	}
	var logged bytes.Buffer
	if s := handOver("/new/ouro", ln, log.New(&logged, "", 0)); len(s.Env) != 0 || !strings.Contains(logged.String(), "not handed over") {
		t.Fatalf("got %+v, %s", s, logged.String())
	}
}
