package shell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/guard"
	"ouroboros/plugin"
)

var errTest = errors.New("test error")

// world is a machine with a user, a node and bash for it.
type world struct {
	t         *testing.T
	home      string // of the user
	node      string // the directory of the node
	guard     *guard.Guard
	protected []string
	told      []string // what the owner was told of the jobs of an answer, see answer
	toldMu    sync.Mutex
}

func newWorld(t *testing.T) *world {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, home: filepath.Join(root, "home"), protected: []string{"~/.ssh"}}
	w.node = filepath.Join(w.home, ".config", "ouroboros")
	t.Setenv("HOME", w.home)
	w.file("~/.config/ouroboros/config.json", "the secret")
	w.file("~/.ssh/id_ed25519", "the key")
	w.file("~/share/hello.txt", "hello")
	w.guard = &guard.Guard{
		Home:      w.node,
		Secrets:   []string{w.node + "/config.json"},
		Protected: func() ([]string, error) { return w.protected, nil },
	}
	return w
}

// path is where a path below "~" is.
func (w *world) path(path string) string { return w.home + strings.TrimPrefix(path, "~") }

// file writes a file with its directories.
func (w *world) file(path, content string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(w.path(path)), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(w.path(path), []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

// content is what the file at path holds, "<none>" if there is none.
func (w *world) content(path string) string {
	b, err := os.ReadFile(w.path(path))
	if err != nil {
		return "<none>"
	}
	return string(b)
}

// tool is bash with settings.
func (w *world) tool(settings string) plugin.Tool {
	w.t.Helper()
	tool, err := Tool(w.guard).Configure(json.RawMessage(settings))
	if err != nil {
		w.t.Fatal(err)
	}
	return tool
}

// bash runs a call of bash with settings and args in ctx.
func (w *world) bash(ctx context.Context, settings string, args map[string]any) (string, error) {
	w.t.Helper()
	raw, _ := json.Marshal(args)
	return w.tool(settings).Run(ctx, raw)
}

// A command runs in the home directory or the one it names, and its exit
// status and what it printed come back, its errors among it.
func TestRun(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	got, err := w.bash(ctx, "{}", map[string]any{"command": "echo out; echo err >&2; pwd; exit 3"})
	if err != nil || got != "exit 3\nout\nerr\n"+w.home+"\n" {
		t.Errorf("run: %q, %v", got, err)
	}
	got, err = w.bash(ctx, "{}", map[string]any{"command": "pwd", "dir": "~/share"})
	if err != nil || got != "exit 0\n"+w.home+"/share\n" {
		t.Errorf("in a directory: %q, %v", got, err)
	}
	got, err = w.bash(ctx, "{}", map[string]any{"command": "echo new > ~/share/new.txt"})
	if err != nil || got != "exit 0\n" || w.content("~/share/new.txt") != "new\n" {
		t.Errorf("write: %q, %v", got, err)
	}
}

// A command gets of the environment what programs need, the desktop among
// it, and what the owner names.
func TestEnvironment(t *testing.T) {
	w := newWorld(t)
	t.Setenv("OURO_TEST_TOKEN", "s3cret")
	t.Setenv("LC_ALL", "C")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	got, err := w.bash(context.Background(), "{}", map[string]any{"command": "env"})
	if err != nil || !strings.Contains(got, "\nLC_ALL=C\n") || !strings.Contains(got, "\nHOME="+w.home+"\n") ||
		!strings.Contains(got, "\nWAYLAND_DISPLAY=wayland-1\n") || strings.Contains(got, "s3cret") {
		t.Errorf("env: %q, %v", got, err)
	}
	got, err = w.bash(context.Background(), `{"env": ["OURO_TEST_TOKEN"]}`, map[string]any{"command": "echo $OURO_TEST_TOKEN"})
	if err != nil || got != "exit 0\ns3cret\n" {
		t.Errorf("named by the owner: %q, %v", got, err)
	}
}

// A command runs as the user, in no sandbox: no rule for files holds.
func TestNoSandbox(t *testing.T) {
	w := newWorld(t)
	got, err := w.bash(context.Background(), "{}", map[string]any{"command": "cat ~/.config/ouroboros/config.json; echo x > ~/.config/ouroboros/state"})
	if err != nil || got != "exit 0\nthe secret" || w.content("~/.config/ouroboros/state") != "x\n" {
		t.Errorf("without a sandbox: %q, %v", got, err)
	}
}

// A call that cannot run fails, and so does one for a peer, as there is no
// sandbox to hold it.
func TestRunRefuses(t *testing.T) {
	w := newWorld(t)
	w.file("~/file", "x")
	ctx := context.Background()
	peer := a2a.WithOrigin(ctx, a2a.Origin{PublicKey: "peer"})
	for _, tt := range []struct {
		ctx  context.Context
		args string
		want string
	}{
		{ctx, `{}`, `bash needs "command"`},
		{ctx, `{"command": 1}`, "bash: invalid arguments"},
		{peer, `{"command": "true"}`, "bash runs no command of a peer: it runs in no sandbox"},
		{ctx, `{"command": "true", "timeout": -1}`, "timeout is a number of seconds"},
		{ctx, `{"command": "true", "dir": "~/.ssh"}`, `reading ~/.ssh is refused: ~/.ssh is in "protected"`},
		{ctx, `{"command": "true", "dir": "~/file"}`, "~/file is no directory"},
		{ctx, `{"command": "true", "dir": "~/missing"}`, "~/missing is no directory"},
		{ctx, `{"command": "true", "dir": "~relative"}`, `the path "~relative" has to be absolute`},
	} {
		if _, err := w.tool("{}").Run(tt.ctx, json.RawMessage(tt.args)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %q", tt.args, err, tt.want)
		}
	}

	w.guard.Protected = func() ([]string, error) { return nil, errTest }
	if _, err := w.bash(ctx, "{}", map[string]any{"command": "true"}); !errors.Is(err, errTest) {
		t.Errorf("rules that cannot be read: %v", err)
	}
	w.guard.Protected = func() ([]string, error) { return nil, nil }

	old := shell
	t.Cleanup(func() { shell = old })
	shell = filepath.Join(t.TempDir(), "bash")
	if _, err := w.bash(ctx, "{}", map[string]any{"command": "true"}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("without bash: %v", err)
	}
}

// Settings bash cannot run with keep it from being built.
func TestSettings(t *testing.T) {
	w := newWorld(t)
	for settings, want := range map[string]string{
		`{"env": [""]}`:          `"" in env is no name of a variable`,
		`{"env": ["A=B"]}`:       `"A=B" in env is no name of a variable`,
		`{"env": ["A B"]}`:       `"A B" in env is no name of a variable`,
		`{"allow": ["~"]}`:       `unknown field "allow"`,
		`{"timeout": 10}`:        `unknown field "timeout"`,
		`{"env": ["GOPRIVATE"]}`: "",
	} {
		_, err := Tool(w.guard).Configure(json.RawMessage(settings))
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", settings, err, want)
		}
	}
	tool := Tool(w.guard)
	if tool.Name != "bash" || tool.OwnerOnly || !tool.Trusted || !json.Valid([]byte(tool.Schema)) || !strings.Contains(tool.Description, "16 KiB") || !strings.Contains(tool.Description, "in no sandbox") ||
		!strings.Contains(tool.Schema, `"timeout":{"type":"integer","description":"how many seconds it may run, 600 if missing`) ||
		!strings.Contains(tool.Description, "--no-verify") {
		t.Errorf("tool: %+v", tool)
	}
}

// What a command leaves running in the background runs on once it ended.
// The output of one that left the group is not waited for.
func TestLeftRunning(t *testing.T) {
	w := newWorld(t)

	// One that left the group keeps the pipe of the output open: the
	// command ends all the same, after grace.
	old := grace
	grace = 100 * time.Millisecond
	t.Cleanup(func() { grace = old })
	detached := `perl -MPOSIX -e 'exit if fork; POSIX::setsid(); sleep 2'; echo done`
	if got, err := w.bash(context.Background(), "{}", map[string]any{"command": detached}); err != nil || !strings.Contains(got, "done") {
		t.Errorf("one that left the group: %q, %v", got, err)
	}

	got, err := w.bash(context.Background(), "{}", map[string]any{"command": "sleep 30 & echo $!"})
	pid, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(got, "exit 0\n")))
	if err != nil || !running(pid) {
		t.Errorf("left running: %q, %v", got, err)
	}
	syscall.Kill(pid, syscall.SIGKILL)

	// A command that cannot start, and output that has no pipe, fail.
	old2 := shell
	shell = filepath.Join(t.TempDir(), "bash")
	if _, err := w.bash(context.Background(), "{}", map[string]any{"command": "true"}); err == nil {
		t.Error("without bash: no error")
	}
	shell = old2
	pipe = func() (*os.File, *os.File, error) { return nil, nil, errTest }
	t.Cleanup(func() { pipe = os.Pipe })
	if _, err := w.bash(context.Background(), "{}", map[string]any{"command": "true"}); !errors.Is(err, errTest) {
		t.Errorf("without a pipe: %v", err)
	}
}

// running reports whether the process pid runs, once a kill had a moment: a
// zombie its parent has not collected yet runs no more.
func running(pid int) bool {
	for range 50 {
		if syscall.Kill(pid, 0) != nil || zombie(pid) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// zombie reports whether the process pid has ended and waits to be
// collected.
func zombie(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err != nil || strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

// A command that runs too long is stopped with everything it started, and
// what it printed comes along; it ends before its calling context.
func TestTimeout(t *testing.T) {
	w := newWorld(t)
	old := grace
	t.Cleanup(func() { grace = old })
	grace = 200 * time.Millisecond
	pid := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	// bash may yet tell that what it started was killed, or not.
	_, err := w.bash(context.Background(), "{}", map[string]any{"command": "echo started; (sleep 30; echo late > " + pid + ".late) & echo $! > " + pid + "; wait", "timeout": 1})
	if err == nil || !strings.HasPrefix(err.Error(), "the command was stopped after 1s; what it printed:\nstarted\n") || time.Since(start) > 5*time.Second ||
		!strings.HasSuffix(err.Error(), `[its time was up (timed out): run it again with a larger "timeout", as far as the calling context has time left, or with "background": true]`) {
		t.Errorf("stopped: %v after %s", err, time.Since(start))
	}
	time.Sleep(100 * time.Millisecond)
	if b, err := os.ReadFile(pid); err != nil || processAlive(strings.TrimSpace(string(b))) {
		t.Errorf("what the command started still runs: %s, %v", b, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	start = time.Now()
	if _, err := w.bash(ctx, "{}", map[string]any{"command": "sleep 5", "timeout": 60}); err == nil || !strings.Contains(err.Error(), "stopped") ||
		time.Since(start) > 700*time.Millisecond {
		t.Errorf("before the script ends: %v after %s", err, time.Since(start))
	}
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := w.bash(ctx, "{}", map[string]any{"command": "true"}); err == nil || err.Error() != "the call has no time left for a command" {
		t.Errorf("without time: %v", err)
	}
}

// A shell that exits before its children must not turn a timeout during
// output draining into a successful exit, or leave those children running.
func TestTimeoutAfterShellExit(t *testing.T) {
	for _, callerDeadline := range []bool{false, true} {
		name := "tool timeout"
		if callerDeadline {
			name = "calling context"
		}
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			old := grace
			grace = 2 * time.Second
			t.Cleanup(func() { grace = old })
			ctx := context.Background()
			timeout := 1
			if callerDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
				timeout = 60
			}
			// Both a child and a grandchild hold the pipe after bash exits.
			// Keep their IDs on disk so even a failed regression is cleaned up.
			pids := filepath.Join(t.TempDir(), "pids")
			t.Cleanup(func() {
				b, _ := os.ReadFile(pids)
				for _, id := range strings.Fields(string(b)) {
					pid, _ := strconv.Atoi(id)
					if pid > 1 {
						syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			})
			start := time.Now()
			got, err := w.bash(ctx, "{}", map[string]any{
				"command": "(sleep 30 & echo $! >> " + pids + "; wait) & echo $! >> " + pids + "; echo started",
				"timeout": timeout,
			})
			if got != "" || err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "started\n") {
				t.Errorf("timeout after shell exit: %q, %v", got, err)
			}
			if took := time.Since(start); took > 2500*time.Millisecond {
				t.Errorf("timeout took %s", took)
			}
			b, err := os.ReadFile(pids)
			if err != nil || len(strings.Fields(string(b))) != 2 {
				t.Fatalf("child IDs: %q, %v", b, err)
			}
			for _, id := range strings.Fields(string(b)) {
				pid, _ := strconv.Atoi(id)
				if running(pid) {
					t.Errorf("child %d still runs after timeout", pid)
				}
			}
		})
	}
}

// A command stopped while what left its group still holds its output is not
// waited for longer than grace: what it printed until then comes back.
func TestStoppedWhileDetachedOutputStaysOpen(t *testing.T) {
	w := newWorld(t)
	old := grace
	grace = time.Second
	t.Cleanup(func() { grace = old })
	pid := filepath.Join(t.TempDir(), "pid")
	t.Cleanup(func() {
		b, _ := os.ReadFile(pid)
		if n, _ := strconv.Atoi(strings.TrimSpace(string(b))); n > 1 {
			syscall.Kill(n, syscall.SIGKILL)
		}
	})
	// The command has 800ms: the calling context ends after 1.8s, less
	// grace. The shell ends long before, so the deadline comes while the
	// output drains, and the one that left the group holds it open past grace.
	ctx, cancel := context.WithTimeout(context.Background(), 1800*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := w.bash(ctx, "{}", map[string]any{
		"command": `perl -MPOSIX -e 'exit if fork; POSIX::setsid(); open my $f, ">", "` + pid + `"; print $f $$; close $f; sleep 5'; echo started`,
	})
	if err == nil || !strings.Contains(err.Error(), "started\n") {
		t.Errorf("stopped while draining: %v", err)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("stopped after %s", took)
	}
}

// processAlive reports whether the process with the ID pid runs.
func processAlive(pid string) bool {
	n, err := strconv.Atoi(pid)
	return err == nil && syscall.Kill(n, 0) == nil
}

// Of much output the start and the end come back, with how much was left
// out between them, and no character is cut.
func TestOutput(t *testing.T) {
	o := &output{}
	o.Write([]byte("short"))
	if o.String() != "short" {
		t.Errorf("short: %q", o.String())
	}
	o = &output{}
	head := strings.Repeat("a", MaxOutput/2-1) + "ä" // the ä is cut after its first byte
	o.Write([]byte(head))
	o.Write([]byte(strings.Repeat("b", 3<<10)))
	tail := "ü" + strings.Repeat("c", MaxOutput/2-1) // the ü is cut before its last byte
	o.Write([]byte(tail))
	got := o.String()
	want := strings.Repeat("a", MaxOutput/2-1) + "\n[… 4 KiB left out …]\n" + strings.Repeat("c", MaxOutput/2-1)
	if got != want {
		t.Errorf("cut: %d bytes, %q … %q", len(got), got[MaxOutput/2-10:MaxOutput/2+30], got[len(got)-10:])
	}
	if n, err := o.Write([]byte("xyz")); n != 3 || err != nil {
		t.Errorf("write: %d, %v", n, err)
	}
}

// What is passed of the environment.
func TestPassed(t *testing.T) {
	got := environment([]string{"PATH=/bin", "API_KEY=x", "LC_TIME=C", "GOPRIVATE=y", "DISPLAY=:0", "MINE=z", "BROKEN"}, []string{"MINE"})
	if strings.Join(got, " ") != "PATH=/bin LC_TIME=C GOPRIVATE=y DISPLAY=:0 MINE=z" {
		t.Errorf("environment: %q", got)
	}
}

// The owner is shown the first line of the command.
func TestShow(t *testing.T) {
	for args, want := range map[string]string{
		`{"command": "go test ./..."}`:                   "running go test ./...",
		`{"command": "  cd x\ngo test\n"}`:               "running cd x …",
		`{"command": "npm run dev", "background": true}`: "running in the background npm run dev",
		`{"job": 2}`:               "looking at job 2",
		`{"job": 2, "stop": true}`: "stopping job 2",
		`{"command": ""}`:          "",
		`{"command": 1}`:           "",
		`{}`:                       "",
	} {
		if got := Show(json.RawMessage(args)); got != want {
			t.Errorf("%s: %q, want %q", args, got, want)
		}
	}
}

// A pipe fails when one of its commands does, so that tail does not hide a
// test that failed; output that says tests failed is pointed out when the
// command hid it anyway, and a command a pipe ended early is explained.
func TestHonestResults(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	got, err := w.bash(ctx, "{}", map[string]any{"command": "printf 'ok\\n--- FAIL: TestX\\nFAIL\\tpkg\\n'; exit 1 | tail -1; false | tail -1"})
	if err != nil || !strings.HasPrefix(got, "exit 1\n") {
		t.Errorf("a pipe that failed: %q, %v", got, err)
	}
	got, err = w.bash(ctx, "{}", map[string]any{"command": "printf -- '--- FAIL: TestX\\nFAIL\\tpkg 0.1s\\n' || true"})
	if err != nil || !strings.HasPrefix(got, "exit 0\n") || !strings.Contains(got, "[the output says that tests failed, though the command ended with exit 0") {
		t.Errorf("failed tests hidden: %q, %v", got, err)
	}
	got, err = w.bash(ctx, "{}", map[string]any{"command": "echo '=== RUN TestX'; echo '--- PASS: TestX'; echo ok"})
	if err != nil || strings.Contains(got, "[the output says") {
		t.Errorf("tests that passed: %q, %v", got, err)
	}
	got, err = w.bash(ctx, "{}", map[string]any{"command": "yes | head -2"})
	if err != nil || !strings.HasPrefix(got, "exit 141\ny\ny\n") || !strings.Contains(got, "[exit 141 is a command of a pipe that was ended") {
		t.Errorf("a pipe head ended: %q, %v", got, err)
	}
}
