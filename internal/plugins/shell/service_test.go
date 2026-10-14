package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ouroboros/internal/a2a"
)

// supervisor is a supervisor of the services of w, which stops them all
// once the test ends.
func (w *world) supervisor() *supervisor {
	w.t.Helper()
	sv := &supervisor{file: filepath.Join(w.node, "services.json"), logf: w.t.Logf}
	w.t.Cleanup(sv.stopAll)
	return sv
}

// service runs a call of the tool for the services of sv with settings and
// args in ctx.
func (w *world) service(ctx context.Context, sv *supervisor, settings string, args map[string]any) (string, error) {
	w.t.Helper()
	tool, err := ServiceTool(sv, w.guard).Configure(json.RawMessage(settings))
	if err != nil {
		w.t.Fatal(err)
	}
	raw, _ := json.Marshal(args)
	return tool.Run(ctx, raw)
}

// freePort is a port on localhost nothing listens on.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// pidsIn are the numbers a service printed on lines like "pid 123".
func pidsIn(text string) []int {
	var pids []int
	for _, m := range regexp.MustCompile(`pid (\d+)`).FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		pids = append(pids, n)
	}
	return pids
}

// gone reports whether the process pid ended within a while.
func gone(pid int) bool {
	for range 150 {
		if syscall.Kill(pid, 0) != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// waitFor waits until the last lines of the service name hold want.
func (w *world) waitFor(sv *supervisor, name, want string) string {
	w.t.Helper()
	var got string
	for range 150 {
		got, _ = w.service(context.Background(), sv, "{}", map[string]any{"action": "logs", "name": name})
		if strings.Contains(got, want) {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.t.Fatalf("the service %s never printed %q:\n%s", name, want, got)
	return ""
}

// leaves is a service that leaves processes in every way a command can:
// one in the background, one with nohup and one in a session of its own,
// out of its group. Each prints its number.
const leaves = `sleep 301 & echo "pid $!"
nohup sleep 302 >/dev/null 2>&1 & echo "pid $!"
perl -e 'use POSIX; POSIX::setsid(); print "pid $$\n"; exec "sleep", "303"' &
echo ready
exec sleep 304`

// A service runs past the call that started it, answers on its port, is
// listed and looked at, and stopped with everything it started, also what
// left its group: nothing of it is left.
func TestService(t *testing.T) {
	w := newWorld(t)
	sv := w.supervisor()
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	got, err := w.service(ctx, sv, "{}", map[string]any{
		"action": "start", "name": "web", "port": port, "dir": "~/share",
		"command": fmt.Sprintf("echo serving; exec python3 -m http.server --bind 127.0.0.1 %d", port),
	})
	cancel() // the answer is over
	if err != nil || !strings.Contains(got, "service web runs since") || !strings.Contains(got, fmt.Sprintf("answers on localhost:%d", port)) ||
		!strings.Contains(got, "serving") {
		t.Fatalf("start: %q, %v", got, err)
	}
	time.Sleep(200 * time.Millisecond)
	if !answers(port) {
		t.Fatal("the service does not answer once the answer is over")
	}
	if got, _ := w.service(context.Background(), sv, "{}", map[string]any{"action": "list"}); !strings.HasPrefix(got, "web · runs for") ||
		!strings.Contains(got, fmt.Sprintf("localhost:%d", port)) {
		t.Errorf("list: %q", got)
	}
	if list := sv.list(); len(list) != 1 || list[0].Name != "web" || list[0].Port != port || list[0].PeerKey != "" {
		t.Errorf("the list for the owner: %+v", list)
	}
	// One name runs once, one port is listened on once.
	if _, err := w.service(context.Background(), sv, "{}", map[string]any{"action": "start", "name": "web", "command": "sleep 9"}); err == nil ||
		err.Error() != "the service web runs already: stop it, or choose another name" {
		t.Errorf("the same name: %v", err)
	}
	if _, err := w.service(context.Background(), sv, "{}", map[string]any{"action": "start", "name": "other", "command": "sleep 9", "port": port}); err == nil ||
		err.Error() != fmt.Sprintf("the service web listens on port %d already", port) {
		t.Errorf("the same port: %v", err)
	}

	// What it leaves in every way ends with it.
	if _, err := w.service(context.Background(), sv, "{}", map[string]any{"action": "start", "name": "leaves", "command": leaves}); err != nil {
		t.Fatal(err)
	}
	pids := pidsIn(w.waitFor(sv, "leaves", "ready"))
	if len(pids) != 3 {
		t.Fatalf("the processes it left: %v", pids)
	}
	tag := sv.find("", "leaves").tag
	got, err = w.service(context.Background(), sv, "{}", map[string]any{"action": "stop", "name": "leaves"})
	if err != nil || !strings.HasPrefix(got, "service leaves was stopped after") || !strings.Contains(got, ": you stopped it") {
		t.Errorf("stop: %q, %v", got, err)
	}
	for _, pid := range pids {
		if !gone(pid) {
			t.Errorf("the process %d of the service is left", pid)
		}
	}
	if left := tagged(tag); len(left) != 0 {
		t.Errorf("processes with its tag are left: %v", left)
	}

	// The owner stops one in the front end.
	if !sv.stopID(sv.list()[0].ID) || answers(port) {
		t.Error("the owner did not stop the service")
	}
	if got, _ := w.service(context.Background(), sv, "{}", map[string]any{"action": "status", "name": "web"}); !strings.Contains(got, "was stopped after") ||
		!strings.Contains(got, "the owner stopped it") || !strings.Contains(got, "serving") {
		t.Errorf("status after the owner stopped it: %q", got)
	}
	if _, err := os.Stat(sv.file); !os.IsNotExist(err) {
		t.Errorf("the file of the services is left: %v", err)
	}
}

// A service is asked to end and given its time for it before it is killed;
// one that ends by itself says how; one whose time is up is stopped.
func TestServiceEnds(t *testing.T) {
	w := newWorld(t)
	sv := w.supervisor()
	ctx := context.Background()
	if _, err := w.service(ctx, sv, "{}", map[string]any{"action": "start", "name": "polite",
		"command": `trap 'echo bye; exit 0' TERM; echo up; while true; do sleep 0.1; done`}); err != nil {
		t.Fatal(err)
	}
	w.waitFor(sv, "polite", "up")
	if got, _ := w.service(ctx, sv, "{}", map[string]any{"action": "stop", "name": "polite"}); !strings.Contains(got, "bye") {
		t.Errorf("asked to end, it did not: %q", got)
	}
	got, err := w.service(ctx, sv, "{}", map[string]any{"action": "start", "name": "short", "command": "echo done; exit 3"})
	if err != nil || !strings.HasPrefix(got, "service short ended by itself after") || !strings.Contains(got, "exit 3") || !strings.Contains(got, "done") {
		t.Errorf("one that ends by itself: %q, %v", got, err)
	}
	// Its name is free again.
	if _, err := w.service(ctx, sv, "{}", map[string]any{"action": "start", "name": "short", "command": "sleep 30"}); err != nil {
		t.Errorf("the name of one that ended: %v", err)
	}
	dir, err := prepare(ctx, ToolService, w.guard, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := sv.launch("brief", "", "sleep 30", dir, 0, 300*time.Millisecond, settings{})
	if err != nil {
		t.Fatal(err)
	}
	<-s.p.done
	if got := sv.status(s, 5); !strings.Contains(got, "was stopped after") || !strings.Contains(got, "its time was up") {
		t.Errorf("once its time was up: %q", got)
	}
}

// Once the node is gone, killed or crashed, its pipe closes, and the
// service ends its group itself; what left the group, and what a node
// before noted, is ended by its tag when the node starts again.
func TestServiceOutlivesNoNode(t *testing.T) {
	w := newWorld(t)
	sv := w.supervisor()
	if _, err := w.service(context.Background(), sv, "{}", map[string]any{"action": "start", "name": "orphan",
		"command": `sleep 305 & echo "pid $!"; echo ready; exec sleep 306`}); err != nil {
		t.Fatal(err)
	}
	pids := pidsIn(w.waitFor(sv, "orphan", "ready"))
	s := sv.find("", "orphan")
	s.watch.Close() // as the node's end of the pipe does, when it is gone
	for _, pid := range append(pids, s.p.cmd.Process.Pid) {
		if !gone(pid) {
			t.Errorf("the process %d outlived the node", pid)
		}
	}

	// A process the file notes, as one that left the group of a service of
	// a node that crashed; and one whose number the file notes with another
	// time it started, as a process that got the number of one that ended.
	start := func(seconds string) (*exec.Cmd, member) {
		c := exec.Command("sleep", seconds)
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		go c.Wait()
		t.Cleanup(func() { c.Process.Kill() })
		time.Sleep(100 * time.Millisecond)
		for _, p := range procs() {
			if p.pid == c.Process.Pid {
				return c, member{p.pid, p.start}
			}
		}
		t.Fatalf("the process %d is not in the table", c.Process.Pid)
		return nil, member{}
	}
	left, m := start("307")
	other, o := start("309")
	o.Start--
	data, _ := json.Marshal([]record{{Tag: newTag(), Name: "left", Command: "sleep 307", Since: time.Now(), Members: []member{m, o}}})
	os.WriteFile(sv.file, data, 0o600)
	next := w.supervisor()
	next.start()
	if !gone(left.Process.Pid) {
		t.Error("what a node before left runs on")
	}
	if syscall.Kill(other.Process.Pid, 0) != nil {
		t.Error("a process that got the number of one the file notes was killed")
	}
	if _, err := os.Stat(next.file); !os.IsNotExist(err) {
		t.Errorf("the file of the services is left: %v", err)
	}
}

// When the node stops, every service stops with it, and none starts any
// more.
func TestServicesStopWithTheNode(t *testing.T) {
	w := newWorld(t)
	sv := w.supervisor()
	ctx := context.Background()
	var pids []int
	for _, name := range []string{"a", "b"} {
		if _, err := w.service(ctx, sv, "{}", map[string]any{"action": "start", "name": name, "command": `echo "pid $$"; exec sleep 308`}); err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pidsIn(w.waitFor(sv, name, "pid"))...)
	}
	if data, err := os.ReadFile(sv.file); err != nil || strings.Count(string(data), `"tag"`) != 2 {
		t.Errorf("the file of the services: %s, %v", data, err)
	}
	sv.stopAll()
	for _, pid := range pids {
		if !gone(pid) {
			t.Errorf("the process %d outlived the node", pid)
		}
	}
	if len(sv.list()) != 0 {
		t.Errorf("services run: %+v", sv.list())
	}
	if _, err := w.service(ctx, sv, "{}", map[string]any{"action": "start", "name": "late", "command": "sleep 9"}); err == nil ||
		err.Error() != "the node stops: no service starts any more" {
		t.Errorf("a start after the node stopped: %v", err)
	}
	if got, _ := w.service(ctx, sv, "{}", map[string]any{"action": "status", "name": "a"}); !strings.Contains(got, "the node stopped") {
		t.Errorf("status: %q", got)
	}
}

// A peer starts no service, as there is no sandbox to hold it, and neither
// sees nor stops those of the owner.
func TestServicesOfPeers(t *testing.T) {
	w := newWorld(t)
	sv := w.supervisor()
	owner := context.Background()
	peer := a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer-key"})
	if _, err := w.service(peer, sv, "{}", map[string]any{"action": "start", "name": "p", "command": "sleep 9"}); err == nil ||
		err.Error() != "service runs no command of a peer: it runs in no sandbox" {
		t.Errorf("a peer starts: %v", err)
	}
	if _, err := w.service(owner, sv, "{}", map[string]any{"action": "start", "name": "web", "command": "sleep 30"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.service(peer, sv, "{}", map[string]any{"action": "list"}); got != "no service runs" {
		t.Errorf("the peer sees: %q", got)
	}
	if _, err := w.service(peer, sv, "{}", map[string]any{"action": "stop", "name": "web"}); err == nil {
		t.Error("the peer stopped the owner's service")
	}
}

// What a call asks for is checked before anything starts.
func TestServiceArguments(t *testing.T) {
	w := newWorld(t)
	sv := w.supervisor()
	ctx := context.Background()
	for args, want := range map[string]string{
		`{"action": "start", "name": "Web", "command": "x"}`:                `service needs "name": small letters, digits and -, starting with a letter, at most 32`,
		`{"action": "start", "name": "web"}`:                                `service needs "command" to start a service`,
		`{"action": "start", "name": "web", "command": "x", "hours": 25}`:   "a service runs 24 hours at most",
		`{"action": "start", "name": "web", "command": "x", "port": 70000}`: "70000 is no port",
		`{"action": "restart", "name": "web"}`:                              `service does "start", "status", "logs", "stop" or "list", not "restart"`,
		`{"action": "status", "name": "none"}`:                              "there is no service none: no service runs",
	} {
		var a map[string]any
		json.Unmarshal([]byte(args), &a)
		if _, err := w.service(ctx, sv, "{}", a); err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", args, err, want)
		}
	}
}
