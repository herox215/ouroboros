package shell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/guard"
	"ouroboros/internal/sandbox"
	"ouroboros/plugin"
)

var errTest = errors.New("test error")

// world is a machine with a user, a node and bash for it.
type world struct {
	t           *testing.T
	home        string // of the user
	node        string // the directory of the node
	guard       *guard.Guard
	protected   []string
	failing     error    // what reading the configuration fails with
	unsandboxed bool     // what the configuration says of the sandbox
	told        []string // what the owner was told of the jobs of an answer, see answer
	toldMu      sync.Mutex
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

// tool is bash with settings, which the configuration of w says whether to
// run in a sandbox.
func (w *world) tool(settings string) plugin.Tool {
	w.t.Helper()
	tool, err := Tool(w.guard, func() (bool, error) { return w.unsandboxed, w.failing }).Configure(json.RawMessage(settings))
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

// needSandbox skips a test where no sandbox can be had.
func needSandbox(t *testing.T) {
	t.Helper()
	if err := sandbox.Available(); err != nil {
		t.Skip(err)
	}
}

// A command runs in the home directory or the one it names, and its exit
// status and what it printed come back, its errors among it.
func TestRun(t *testing.T) {
	needSandbox(t)
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

// In the sandbox a command keeps to the rules for files: the node and the
// secrets stay as they are, the secrets and what is protected are not read,
// and with "allow" it writes there alone.
func TestSandboxKeepsTheRules(t *testing.T) {
	needSandbox(t)
	w := newWorld(t)
	ctx := context.Background()
	got, err := w.bash(ctx, "{}", map[string]any{"command": "echo x > ~/.config/ouroboros/state"})
	if err != nil || !strings.HasPrefix(got, "exit 1\n") || !strings.Contains(got, "[it ran in a sandbox") || w.content("~/.config/ouroboros/state") != "<none>" {
		t.Errorf("writing the state of the node: %q, %v", got, err)
	}
	for _, path := range []string{"~/.config/ouroboros/config.json", "~/.ssh/id_ed25519"} {
		got, err := w.bash(ctx, "{}", map[string]any{"command": "cat " + path})
		if err != nil || strings.Contains(got, "the secret") || strings.Contains(got, "the key") {
			t.Errorf("reading %s: %q, %v", path, got, err)
		}
	}
	// With allow, the owner's commands write the temporary files too.
	if err := os.MkdirAll(w.path("~/tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", w.path("~/tmp"))
	got, err = w.bash(ctx, `{"allow": ["~/share"]}`, map[string]any{"command": "echo x > ~/elsewhere; echo y > ~/share/y; echo z > \"$TMPDIR/z\""})
	if err != nil || !strings.HasPrefix(got, "exit 0\n") || w.content("~/elsewhere") != "<none>" || w.content("~/share/y") != "y\n" || w.content("~/tmp/z") != "z\n" {
		t.Errorf("with allow: %q, %v", got, err)
	}
	got, err = w.bash(ctx, `{"deny": ["~/share"]}`, map[string]any{"command": "cat ~/share/hello.txt"})
	if err != nil || strings.HasPrefix(got, "exit 0\n") {
		t.Errorf("with deny: %q, %v", got, err)
	}
	if _, err := w.bash(ctx, `{"deny": ["~/share"]}`, map[string]any{"command": "true", "dir": "~/share"}); err == nil || !strings.Contains(err.Error(), `is in "deny" of the tool bash`) {
		t.Errorf("in a directory it is denied: %v", err)
	}
}

// A command gets of the environment what programs need, and what the owner
// names; in the sandbox it knows that it is in one.
func TestEnvironment(t *testing.T) {
	needSandbox(t)
	w := newWorld(t)
	t.Setenv("OURO_TEST_TOKEN", "s3cret")
	t.Setenv("LC_ALL", "C")
	got, err := w.bash(context.Background(), "{}", map[string]any{"command": "env"})
	if err != nil || !strings.Contains(got, "\nLC_ALL=C\n") || !strings.Contains(got, "\nHOME="+w.home+"\n") ||
		!strings.Contains(got, "\n"+sandbox.Env+"=1\n") || strings.Contains(got, "s3cret") {
		t.Errorf("env: %q, %v", got, err)
	}
	got, err = w.bash(context.Background(), `{"env": ["OURO_TEST_TOKEN"]}`, map[string]any{"command": "echo $OURO_TEST_TOKEN"})
	if err != nil || got != "exit 0\ns3cret\n" {
		t.Errorf("named by the owner: %q, %v", got, err)
	}
}

// Without the sandbox a command runs as the user, where no rule for files
// holds; where there is no sandbox, only that runs.
func TestWithoutSandbox(t *testing.T) {
	w := newWorld(t)
	t.Setenv(sandbox.Env, "1") // a sandbox cannot be had in another one
	ctx := context.Background()
	_, err := w.bash(ctx, "{}", map[string]any{"command": "true"})
	if !errors.Is(err, sandbox.ErrUnavailable) || !strings.Contains(err.Error(), `"bash_without_sandbox": true`) {
		t.Errorf("without a sandbox: %v", err)
	}
	w.unsandboxed = true
	got, err := w.bash(ctx, "{}", map[string]any{"command": "cat ~/.config/ouroboros/config.json; echo x > ~/.config/ouroboros/state"})
	if err != nil || got != "exit 0\nthe secret" || w.content("~/.config/ouroboros/state") != "x\n" {
		t.Errorf("without a sandbox: %q, %v", got, err)
	}
	// What fails is no refusal of a sandbox.
	if got, err := w.bash(ctx, "{}", map[string]any{"command": "echo operation not permitted; exit 1"}); err != nil || strings.Contains(got, "[it ran") {
		t.Errorf("a failure: %q, %v", got, err)
	}
}

// A call that cannot run fails, and so does one for a peer without a
// sandbox.
func TestRunRefuses(t *testing.T) {
	w := newWorld(t)
	w.unsandboxed = true
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
		{peer, `{"command": "true"}`, "bash runs the commands of a peer in a sandbox alone, and the owner turned it off"},
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

	w.failing = errTest
	if _, err := w.bash(ctx, "{}", map[string]any{"command": "true"}); !errors.Is(err, errTest) || !strings.Contains(err.Error(), "no command runs") {
		t.Errorf("a configuration that cannot be read: %v", err)
	}
	w.failing = nil
	w.guard.Protected = func() ([]string, error) { return nil, errTest }
	if _, err := w.bash(ctx, "{}", map[string]any{"command": "true"}); !errors.Is(err, errTest) {
		t.Errorf("rules that cannot be read: %v", err)
	}
	// The rules are read for the directory and again for the sandbox: they
	// may have become unreadable in between.
	reads := 0
	w.guard.Protected = func() ([]string, error) {
		if reads++; reads > 1 {
			return nil, errTest
		}
		return nil, nil
	}
	w.unsandboxed = false
	if _, err := w.bash(ctx, "{}", map[string]any{"command": "true"}); !errors.Is(err, errTest) || reads != 2 {
		t.Errorf("rules that became unreadable: %v after %d reads", err, reads)
	}
	w.unsandboxed = true
	w.guard.Protected = func() ([]string, error) { return nil, nil }

	old := shell
	t.Cleanup(func() { shell = old })
	shell = filepath.Join(t.TempDir(), "bash")
	if _, err := w.bash(ctx, "{}", map[string]any{"command": "true"}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("without bash: %v", err)
	}
	if sandbox.Available() == nil {
		w.unsandboxed = false
		if _, err := w.bash(ctx, "{}", map[string]any{"command": "true"}); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("without bash in the sandbox: %v", err)
		}
	}
}

// Settings bash cannot run with keep it from being built.
func TestSettings(t *testing.T) {
	w := newWorld(t)
	for settings, want := range map[string]string{
		`{"allow": ["share"]}`:   `the path "share" has to be absolute`,
		`{"deny": ["share"]}`:    `the path "share" has to be absolute`,
		`{"env": [""]}`:          `"" in env is no name of a variable`,
		`{"env": ["A=B"]}`:       `"A=B" in env is no name of a variable`,
		`{"env": ["A B"]}`:       `"A B" in env is no name of a variable`,
		`{"network": "yes"}`:     "cannot unmarshal string",
		`{"timeout": 10}`:        `unknown field "timeout"`,
		`{"allow": ["~"]}`:       "",
		`{"env": ["GOPRIVATE"]}`: "",
	} {
		_, err := Tool(w.guard, nil).Configure(json.RawMessage(settings))
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", settings, err, want)
		}
	}
	tool := Tool(w.guard, nil)
	if tool.Name != "bash" || tool.OwnerOnly || !tool.Trusted || !json.Valid([]byte(tool.Schema)) || !strings.Contains(tool.Description, "64 KiB") || !strings.Contains(tool.Description, `"network": true in the settings of bash`) ||
		!strings.Contains(tool.Schema, `"timeout":{"type":"integer","description":"how many seconds it may run, 60 if missing`) {
		t.Errorf("tool: %+v", tool)
	}
}

// A peer the owner listed bash for runs commands only where "allow" lets it
// write: in the first of those paths, or in one below them.
func TestForAPeer(t *testing.T) {
	w := newWorld(t)
	w.file("~/work/a.txt", "a")
	peer := a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"})
	for settings, want := range map[string]string{
		`{}`:                 `bash has no "allow" for this peer, so it runs nothing`,
		`{"allow": []}`:      `bash has no "allow" for this peer, so it runs nothing`,
		`{"allow": ["~/x"]}`: "~/x is no directory",
	} {
		if _, err := w.bash(peer, settings, map[string]any{"command": "true"}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", settings, err, want)
		}
	}
	for dir, want := range map[string]string{
		"~/share": `reading ~/share is refused: it is not in "allow" of the tool bash`,
		"work":    "a peer names a path in full",
	} {
		if _, err := w.bash(peer, `{"allow": ["~/work"]}`, map[string]any{"command": "true", "dir": dir}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("in %s: %v, want %q", dir, err, want)
		}
	}

	needSandbox(t)
	got, err := w.bash(peer, `{"allow": ["~/work"]}`, map[string]any{"command": "pwd; echo b > b.txt; echo x > ../share/x.txt"})
	if err != nil || !strings.HasPrefix(got, "exit 1\n"+w.path("~/work")+"\n") {
		t.Errorf("got %q, %v", got, err)
	}
	if w.content("~/work/b.txt") != "b\n" || w.content("~/share/x.txt") != "<none>" {
		t.Errorf("wrote %q and %q", w.content("~/work/b.txt"), w.content("~/share/x.txt"))
	}
}

// What a command leaves running in the sandbox, in the background, is
// stopped once it ended, and the agent is told so; without the sandbox it
// runs on. The output of one that left the group is not waited for.
func TestNothingIsLeftRunning(t *testing.T) {
	needSandbox(t)
	w := newWorld(t)
	late := w.path("~/share/late")
	writeLate := "(sleep 1; echo late >> " + late + ")"
	for command, left := range map[string]bool{
		"true":           false,
		writeLate + " &": true,
		"nohup sh -c '" + writeLate + "' >/dev/null 2>&1 & (" + writeLate + " &)": true,
	} {
		got, err := w.bash(context.Background(), `{"allow": ["~/share"]}`, map[string]any{"command": command})
		// On Linux the sandbox ends them itself, and nobody is told.
		told := left && runtime.GOOS != "linux"
		if err != nil || !strings.HasPrefix(got, "exit 0\n") || strings.Contains(got, leftRunning) != told {
			t.Errorf("%s: %q, %v", command, got, err)
		}
	}
	// What was left is stopped, so it writes nothing: the file stays away,
	// or one stopped as it opened the file to append to it leaves it empty.
	// What says late is watched for until well after the sleep of the
	// command, so that a slow one is not missed under the load of a run
	// with coverage.
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := w.content("~/share/late")
		if strings.Contains(got, "late") {
			t.Errorf("what the commands left ran on: %q", got)
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// One that left the group keeps the pipe of the output open: the
	// command ends all the same, after grace.
	old := grace
	grace = 100 * time.Millisecond
	t.Cleanup(func() { grace = old })
	detached := `perl -MPOSIX -e 'exit if fork; POSIX::setsid(); sleep 2'; echo done`
	if got, err := w.bash(context.Background(), `{"allow": ["~/share"]}`, map[string]any{"command": detached}); err != nil || !strings.Contains(got, "done") {
		t.Errorf("one that left the group: %q, %v", got, err)
	}

	w.unsandboxed = true
	got, err := w.bash(context.Background(), "{}", map[string]any{"command": "sleep 30 & echo $!"})
	pid, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(got, "exit 0\n")))
	if err != nil || strings.Contains(got, leftRunning) || !running(pid) {
		t.Errorf("without a sandbox: %q, %v", got, err)
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
// what it printed comes along; it ends before the script it runs for.
func TestTimeout(t *testing.T) {
	w := newWorld(t)
	w.unsandboxed = true
	old := grace
	t.Cleanup(func() { grace = old })
	grace = 200 * time.Millisecond
	pid := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	// bash may yet tell that what it started was killed, or not.
	_, err := w.bash(context.Background(), "{}", map[string]any{"command": "echo started; (sleep 30; echo late > " + pid + ".late) & echo $! > " + pid + "; wait", "timeout": 1})
	if err == nil || !strings.HasPrefix(err.Error(), "the command was stopped after 1s; what it printed:\nstarted\n") || time.Since(start) > 5*time.Second ||
		!strings.HasSuffix(err.Error(), `[its time was up: run it again with a larger "timeout", as far as the script has time left, or with "background": true]`) {
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
	if _, err := w.bash(ctx, "{}", map[string]any{"command": "true"}); err == nil || err.Error() != "the script has no time left for a command" {
		t.Errorf("without time: %v", err)
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

// What is passed of the environment, and what a sandbox refused.
func TestEnvironmentAndRefusals(t *testing.T) {
	got := environment([]string{"PATH=/bin", "API_KEY=x", "LC_TIME=C", "GOPRIVATE=y", "MINE=z", "BROKEN"}, []string{"MINE"})
	if strings.Join(got, " ") != "PATH=/bin LC_TIME=C GOPRIVATE=y MINE=z" {
		t.Errorf("environment: %q", got)
	}
	for out, want := range map[string]bool{
		"mkdir: x: Operation not permitted": true, "x: Read-only file system": true, "connect: Network is unreachable": true,
		"curl: (6) Could not resolve host: example.com": true, "FAIL: TestX": false,
		"socket.gaierror: [Errno 8] nodename nor servname provided, or not known": true,
		"dial tcp: lookup x: Temporary failure in name resolution":                true,
	} {
		if refused(out) != want {
			t.Errorf("%q: %v", out, !want)
		}
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
	needSandbox(t)
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
