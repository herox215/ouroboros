package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
	if got, _ := w.service(context.Background(), sv, "{}", map[string]any{"action": "status", "name": "web"}); !strings.HasPrefix(got, "service web runs, for ") ||
		!strings.Contains(got, fmt.Sprintf(", on localhost:%d; its last lines:\nserving", port)) {
		t.Errorf("status while it runs: %q", got)
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

	// The owner stops one in the front end, and none that does not run.
	if sv.stopID("none") {
		t.Error("the owner stopped a service that is not there")
	}
	id := sv.list()[0].ID
	if !sv.stopID(id) || answers(port) {
		t.Error("the owner did not stop the service")
	}
	if sv.stopID(id) {
		t.Error("the owner stopped a service that ended")
	}
	if got, _ := w.service(context.Background(), sv, "{}", map[string]any{"action": "status", "name": "web"}); !strings.Contains(got, "was stopped after") ||
		!strings.Contains(got, "the owner stopped it") || !strings.Contains(got, "serving") {
		t.Errorf("status after the owner stopped it: %q", got)
	}
	if _, err := os.Stat(sv.file); !os.IsNotExist(err) {
		t.Errorf("the file of the services is left: %v", err)
	}
	// What ended is still listed for a while.
	if got, _ := w.service(context.Background(), sv, "{}", map[string]any{"action": "list"}); !strings.HasPrefix(got, "web · ended at ") ||
		!strings.Contains(got, fmt.Sprintf(" · localhost:%d · echo serving;", port)) || !strings.Contains(got, "\nleaves · ended at ") {
		t.Errorf("list once they ended: %q", got)
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
	// One that ends before its port answers is not waited for.
	start := time.Now()
	got, err = w.service(ctx, sv, "{}", map[string]any{"action": "start", "name": "crash", "port": freePort(t), "command": "echo no; exit 2"})
	if err != nil || !strings.HasPrefix(got, "service crash ended by itself after") || !strings.Contains(got, "exit 2") || !strings.Contains(got, "no") {
		t.Errorf("one that ends before its port answers: %q, %v", got, err)
	}
	if took := time.Since(start); took >= readyTime {
		t.Errorf("the start waited %s for the port of one that ended", took)
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
		`{"action": "start", "name": "web", "command": "x", "hours": -1}`:   `"hours" is how many hours it may run`,
	} {
		var a map[string]any
		json.Unmarshal([]byte(args), &a)
		if _, err := w.service(ctx, sv, "{}", a); err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", args, err, want)
		}
	}
	tool, err := ServiceTool(sv, w.guard).Configure(json.RawMessage("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(ctx, json.RawMessage(`{"action": "list", "lines": "many"}`)); err == nil || !strings.Contains(err.Error(), ToolService) {
		t.Errorf("arguments that cannot be read: %v", err)
	}
	// Settings it cannot run with keep it from being built.
	for settings, want := range map[string]string{
		`{"env": [""]}`:    `"" in env is no name of a variable`,
		`{"env": ["A=B"]}`: `"A=B" in env is no name of a variable`,
		`{"env": ["A B"]}`: `"A B" in env is no name of a variable`,
	} {
		if _, err := ServiceTool(sv, w.guard).Configure(json.RawMessage(settings)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", settings, err, want)
		}
	}
}

// A service that cannot start says why, and is neither listed nor noted.
func TestServiceStartFails(t *testing.T) {
	w := newWorld(t)
	sv := w.supervisor()
	ctx := context.Background()
	start := map[string]any{"action": "start", "name": "web", "command": "sleep 30"}

	// Something that is no service listens on the port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if _, err := w.service(ctx, sv, "{}", map[string]any{"action": "start", "name": "web", "command": "sleep 30", "port": port}); err == nil ||
		err.Error() != fmt.Sprintf("something listens on port %d already: choose another port, or ask the user about what listens there", port) {
		t.Errorf("a port that is taken: %v", err)
	}

	old := shell
	t.Cleanup(func() { shell = old })
	shell = filepath.Join(t.TempDir(), "bash")
	if _, err := w.service(ctx, sv, "{}", start); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("without bash: %v", err)
	}
	shell = old

	t.Cleanup(func() { pipe = os.Pipe })
	pipe = func() (*os.File, *os.File, error) { return nil, nil, errTest }
	if _, err := w.service(ctx, sv, "{}", start); !errors.Is(err, errTest) {
		t.Errorf("without a pipe: %v", err)
	}
	pipe = os.Pipe

	if got, _ := w.service(ctx, sv, "{}", map[string]any{"action": "list"}); got != "no service runs" {
		t.Errorf("list: %q", got)
	}
	if _, err := os.Stat(sv.file); !os.IsNotExist(err) {
		t.Errorf("the file of the services: %v", err)
	}
}

// No more services start than the limit of whom they run for: those of
// others and those that ended do not count, and the name of one that ended
// is free.
func TestServiceAdmit(t *testing.T) {
	ended := make(chan struct{})
	close(ended)
	svc := func(peer, name string, port int, running bool) *service {
		done := ended
		if running {
			done = make(chan struct{})
		}
		return &service{peer: peer, name: name, port: port, p: &process{done: done}}
	}
	sv := &supervisor{all: []*service{
		svc("", "a", 0, true), svc("", "b", 0, true), svc("", "c", 8080, true), svc("", "old", 0, false), svc("", "older", 0, false),
		svc("peer", "a", 0, true), svc("peer", "b", 0, true),
	}}
	for _, tt := range []struct {
		peer, name string
		port       int
		want       string
	}{
		{"", "d", 0, ""},
		{"", "old", 0, ""},
		{"", "a", 0, "the service a runs already: stop it, or choose another name"},
		{"", "d", 8080, "the service c listens on port 8080 already"},
		{"other", "d", 9090, ""},
		{"other", "a", 0, ""},
		{"peer", "c", 0, "2 services run already: stop one first"},
		{"peer", "d", 8080, "the service c listens on port 8080 already"},
	} {
		limit := MaxServices
		if tt.peer != "" {
			limit = MaxPeerServices
		}
		err := sv.admit(tt.peer, tt.name, tt.port, limit)
		if tt.want == "" && err != nil || tt.want != "" && (err == nil || err.Error() != tt.want) {
			t.Errorf("%q of %q on %d: %v, want %q", tt.name, tt.peer, tt.port, err, tt.want)
		}
	}
	sv.all = append(sv.all, svc("", "d", 0, true))
	if err := sv.admit("", "e", 0, MaxServices); err == nil || err.Error() != "4 services run already: stop one first" {
		t.Errorf("one more than the limit: %v", err)
	}
	sv.closed = true
	if err := sv.admit("other", "e", 0, MaxPeerServices); err == nil || err.Error() != "the node stops: no service starts any more" {
		t.Errorf("once the node stops: %v", err)
	}
}

// A service that does not end when asked is killed once its time to end is
// over.
func TestServiceKilledAfterGrace(t *testing.T) {
	w := newWorld(t)
	sv := w.supervisor()
	ctx := context.Background()
	if _, err := w.service(ctx, sv, "{}", map[string]any{"action": "start", "name": "stubborn",
		"command": `trap '' TERM; echo up; while true; do sleep 0.1; done`}); err != nil {
		t.Fatal(err)
	}
	w.waitFor(sv, "stubborn", "up")
	start := time.Now()
	got, err := w.service(ctx, sv, "{}", map[string]any{"action": "stop", "name": "stubborn"})
	if err != nil || !strings.HasPrefix(got, "service stubborn was stopped after") || !strings.Contains(got, ": you stopped it") {
		t.Errorf("stop: %q, %v", got, err)
	}
	if took := time.Since(start); took < serviceGrace {
		t.Errorf("killed after %s, before its time to end was over", took)
	}
}

// A service whose port does not answer runs all the same, and the agent is
// told so.
func TestServiceDoesNotAnswer(t *testing.T) {
	w := newWorld(t)
	sv := w.supervisor()
	ctx := context.Background()
	port := freePort(t)
	got, err := w.service(ctx, sv, "{}", map[string]any{"action": "start", "name": "mute", "port": port, "command": "echo up; exec sleep 60"})
	if err != nil || !strings.HasPrefix(got, "service mute runs since") ||
		!strings.Contains(got, fmt.Sprintf(`, but does not answer on localhost:%d after 15s: look at it with "status".`, port)) || !strings.HasSuffix(got, "\nup") {
		t.Errorf("start: %q, %v", got, err)
	}
	if got, _ := w.service(ctx, sv, "{}", map[string]any{"action": "status", "name": "mute"}); !strings.Contains(got, fmt.Sprintf(", on localhost:%d, which does not answer; its last lines:\nup", port)) {
		t.Errorf("status: %q", got)
	}
}

// A file of the services that cannot be written is logged, and the service
// runs all the same.
func TestServiceNotNoted(t *testing.T) {
	w := newWorld(t)
	var mu sync.Mutex
	var logged []string
	sv := &supervisor{file: filepath.Join(w.node, "missing", "services.json"), logf: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	}}
	t.Cleanup(sv.stopAll)
	dir, err := prepare(context.Background(), ToolService, w.guard, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := sv.launch("quiet", "", "sleep 30", dir, 0, time.Hour, settings{})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	first := fmt.Sprint(logged)
	mu.Unlock()
	if !s.running() || !strings.HasPrefix(first, "[the services that run cannot be noted: ") {
		t.Errorf("logged %s", first)
	}
}

// The tool tells a time under an hour in minutes.
func TestHours(t *testing.T) {
	for d, want := range map[time.Duration]string{30 * time.Minute: "30 minutes", 59 * time.Minute: "59 minutes", time.Hour: "1 hour", 2 * time.Hour: "2 hours", 24 * time.Hour: "24 hours"} {
		if got := hours(d); got != want {
			t.Errorf("%s: %q, want %q", d, got, want)
		}
	}
}

// The owner is shown what a call of the tool for services does.
func TestShowService(t *testing.T) {
	for args, want := range map[string]string{
		`{"action": "start", "name": "web"}`:  "starting the service web",
		`{"action": "stop", "name": "web"}`:   "stopping the service web",
		`{"action": "status", "name": "web"}`: "looking at the service web",
		`{"action": "logs", "name": "web"}`:   "looking at the service web",
		`{"action": "list"}`:                  "listing the services",
		`{"action": "restart", "name": "x"}`:  "",
		`{"action": 1}`:                       "",
	} {
		if got := ShowService(json.RawMessage(args)); got != want {
			t.Errorf("%s: %q, want %q", args, got, want)
		}
	}
}

// The log of a service keeps the last serviceLog bytes it printed, from the
// start of a line on.
func TestServiceLog(t *testing.T) {
	var r ring
	if got := r.last(5); got != "(nothing)" {
		t.Errorf("an empty log: %q", got)
	}
	// Lines of 100 bytes each, numbered.
	for i := range 700 {
		fmt.Fprintf(&r, "%03d%s\n", i, strings.Repeat("x", 96))
	}
	if len(r.buf) != serviceLog/100*100 {
		t.Errorf("%d bytes kept", len(r.buf))
	}
	lines := strings.Split(r.last(1000), "\n")
	if len(lines) != serviceLog/100 || !strings.HasPrefix(lines[0], fmt.Sprintf("%03d", 700-serviceLog/100)) || !strings.HasPrefix(lines[len(lines)-1], "699") {
		t.Errorf("%d lines kept, from %.3s to %.3s", len(lines), lines[0], lines[len(lines)-1])
	}
	if got := r.last(2); got != "698"+strings.Repeat("x", 96)+"\n699"+strings.Repeat("x", 96) {
		t.Errorf("the last two lines: %q", got)
	}
	// What has no end of a line is kept as far as it goes.
	var long ring
	if n, err := long.Write([]byte(strings.Repeat("y", serviceLog+10))); n != serviceLog+10 || err != nil || len(long.buf) != serviceLog {
		t.Errorf("one long line: %d, %v, %d bytes kept", n, err, len(long.buf))
	}
}
