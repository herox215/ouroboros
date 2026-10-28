// Package shell is the plugin that contributes the tool bash, which runs a
// command of the shell for the owner, as the owner: in no sandbox, so that
// it reaches what the owner's shell reaches, the desktop too.
package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/guard"
	"ouroboros/internal/home"
	"ouroboros/internal/workdir"
	"ouroboros/plugin"
)

// The limits of a command.
const (
	// DefaultTimeout is how long a command runs when the call does not say;
	// a call may say longer, up to the deadline of its context.
	DefaultTimeout = 10 * time.Minute
	// MaxOutput is how much of what a command printed comes back: the start
	// and the end of it, half each.
	MaxOutput = 16 << 10
)

// Changed by tests.
var (
	// shell is the bash that runs a command: not one looked up in PATH,
	// where a tool for files could put another.
	shell = "/bin/bash"
	// grace is how long a command that was stopped may take to end, with
	// what it started, before its output is no longer waited for.
	grace = 2 * time.Second
)

// passed are the variables of the environment a command gets besides those
// that start with LC_ and those the owner names in "env": what programs need
// to run, the desktop for a program with windows, and nothing that is likely
// a secret.
var passed = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "TERM", "TMPDIR", "TZ",
	"DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR",
	"GOPATH", "GOROOT", "GOCACHE", "GOMODCACHE", "GOPROXY", "GOPRIVATE", "GOFLAGS", "GOTOOLCHAIN",
}

// Plugin contributes bash, which asks g where a command may start, and what
// the owner is shown of its calls.
func Plugin(dir home.Dir, g *guard.Guard) plugin.Plugin {
	return plugin.New("shell", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, Tool(g))
		plugin.Add(ctx, plugin.ToolView{Tool: assistant.ToolBash, Show: Show, Detail: assistant.HowItEnded})
		// The services of the profile: the node that serves it ends what a
		// node before left of them, and stops them all when it stops.
		sv := &supervisor{file: dir.Services(), logf: dir.Log}
		plugin.Add(ctx, ServiceTool(sv, g))
		plugin.Add(ctx, plugin.ToolView{Tool: ToolService, Show: ShowService, Detail: assistant.HowItEnded})
		plugin.Add(ctx, plugin.Services{List: sv.list, Stop: sv.stopID})
		plugin.Add(ctx, plugin.Serving{Start: sv.start, Stop: sv.stopAll})
		return nil
	})
}

// Show is what the owner is shown of a call of bash: the first line of its
// command, or the job it looks at.
func Show(raw json.RawMessage) string {
	var call args
	if json.Unmarshal(raw, &call) != nil {
		return ""
	}
	switch {
	case call.Job != 0 && call.Stop:
		return fmt.Sprintf("stopping job %d", call.Job)
	case call.Job != 0:
		return fmt.Sprintf("looking at job %d", call.Job)
	case call.Command == "":
		return ""
	}
	line := firstLine(call.Command)
	if call.Background {
		return "running in the background " + line
	}
	return "running " + line
}

// firstLine is the first line of command, with "…" if more follow.
func firstLine(command string) string {
	line, _, more := strings.Cut(strings.TrimSpace(command), "\n")
	if more {
		line += " …"
	}
	return line
}

// settings are what the owner can say about bash in the configuration.
type settings struct {
	// Env are the variables of the environment a command gets besides
	// passed.
	Env []string `json:"env" about:"the variables of the environment a command gets besides the usual ones, by name" unset:"none"`
}

type args struct {
	Command    string `json:"command"`
	Dir        string `json:"dir"`
	Timeout    int    `json:"timeout"`
	Background bool   `json:"background"`
	Job        int    `json:"job"`
	Stop       bool   `json:"stop"`
}

// Tool returns bash, which asks g where a command may start.
func Tool(g *guard.Guard) plugin.Tool {
	t := plugin.Tool{
		Name: assistant.ToolBash,
		Description: "Run a command with bash -c. Returns its exit status, like \"exit 0\", and below it what it printed, " +
			fmt.Sprintf("its output and its errors as they came; of more than %d KiB a size summary, the full log path, and the start and the end. ", MaxOutput>>10) +
			"Large outputs are saved in full to private temporary log files, retained until the system cleans its temporary directory; use read or search on the path for details. " +
			"Recognized large rustfmt and unified diffs include a bounded affected-file summary and a compact excerpt. " +
			"Every call starts anew in dir: cd and variables do not carry over. " +
			"It runs as the user, in no sandbox: it reaches every file, the network and the desktop, so a program with windows opens on the screen. " +
			"Use the tools for files to read, search and change files, and bash for builds, tests, git and other programs. " +
			"What it starts with & or nohup runs on once it ended, unseen: do not. " +
			"It runs with pipefail: a pipe fails when any command of it fails, so that | tail does not hide a test that failed. " +
			"What has to keep running while you go on, like a server you test against, runs with \"background\": true, as a job of this answer: " +
			"the call returns its number at once. Completion is reported automatically between tool rounds, with its exit status and output. " +
			"Go on with other work; if you need its result next, {\"job\": n} waits for completion without polling. " +
			"With a timeout it waits at most that many seconds; do not poll in short intervals. {\"job\": n, \"stop\": true} stops it. " +
			"It ends with the answer at the latest. " +
			"A build, a race test or a git hook can take many minutes: give a git commit a generous \"timeout\", like 600, rather than skipping its hooks with --no-verify.",
		Schema: `{"type":"object","properties":{` +
			`"command":{"type":"string","description":"the command, as bash -c takes it; needed but with \"job\""},` +
			`"dir":{"type":"string","description":"the directory it runs in: absolute, starting with \"~\", the home directory, or relative to the directory commands start in, where it runs if missing"},` +
			fmt.Sprintf(`"timeout":{"type":"integer","description":"how many seconds it may run, %d if missing, at most what is left of the call context; in the background as long as the answer if missing; with \"job\" limit the wait for completion (without a timeout it waits until done)"},`, int(DefaultTimeout/time.Second)) +
			`"background":{"type":"boolean","description":"run the command as a job that keeps running, and return at once"},` +
			`"job":{"type":"integer","description":"the number of a job: shows how it does, without \"command\""},` +
			`"stop":{"type":"boolean","description":"with \"job\": stop the job"}` +
			`}}`,
		Trusted: true,
	}
	return plugin.Configurable(t, func(s settings) (plugin.RunFunc, error) {
		for _, name := range s.Env {
			if name == "" || strings.ContainsAny(name, "= ") {
				return nil, fmt.Errorf("%q in env is no name of a variable", name)
			}
		}
		return func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a args
			if err := plugin.Decode(assistant.ToolBash, raw, &a); err != nil {
				return "", err
			}
			return run(ctx, g, s, a)
		}, nil
	})
}

// run runs the command of a with the settings s, or looks at the job a
// names.
func run(ctx context.Context, g *guard.Guard, s settings, a args) (string, error) {
	if a.Timeout < 0 {
		return "", errors.New("timeout is a number of seconds")
	}
	if a.Job != 0 {
		return look(ctx, a)
	}
	if a.Stop {
		return "", errors.New(`"stop" goes with "job", the job to stop`)
	}
	if a.Command == "" {
		return "", errors.New(assistant.ToolBash + ` needs "command"`)
	}
	var started *jobs
	if a.Background {
		if started = jobsOf(ctx); started == nil {
			return "", errors.New(assistant.ToolBash + " runs a command in the background within an answer alone")
		}
	}
	dir, err := prepare(ctx, assistant.ToolBash, g, a.Dir)
	if err != nil {
		return "", err
	}

	var cancel context.CancelFunc
	if a.Background {
		// It runs until it ends, or its time is up, or the answer is.
		ctx, cancel = context.WithCancel(started.ctx)
		if a.Timeout > 0 {
			ctx, cancel = context.WithTimeout(started.ctx, time.Duration(a.Timeout)*time.Second)
		}
	} else {
		timeout := DefaultTimeout
		if a.Timeout > 0 {
			timeout = time.Duration(a.Timeout) * time.Second
		}
		// Leave time to return the result before the calling context ends.
		if deadline, ok := ctx.Deadline(); ok {
			timeout = min(timeout, time.Until(deadline)-grace)
		}
		if timeout <= 0 {
			return "", errors.New("the call has no time left for a command")
		}
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	p, err := launch(ctx, cancel, dir, s, a.Command, nil)
	if err != nil {
		cancel()
		return "", err
	}
	if a.Background {
		n, err := started.add(p)
		if err != nil {
			cancel()
			<-p.done
			return "", err
		}
		started.answer.Watch(p.done, func() string {
			return started.ending(n, true)
		})
		return fmt.Sprintf(`job %d runs in the background: completion will be reported automatically between tool rounds. `+
			`Go on with other work, or use {"job": %[1]d} to wait without polling; `+
			`with "timeout" it waits at most that many seconds; {"job": %[1]d, "stop": true} stops it. `+
			`It ends with this answer at the latest`, n), nil
	}
	<-p.done
	result, stopped := p.ending()
	if stopped {
		return "", errors.New("the command was " + result)
	}
	return result, nil
}

// prepare is the directory a command of the tool for the conversation of
// ctx runs in: dir as g allows reading it, the home directory or the
// workspace if it is "". A peer runs none: there is no sandbox to hold it.
func prepare(ctx context.Context, tool string, g *guard.Guard, dir string) (string, error) {
	if _, peer := plugin.OriginFrom(ctx); peer {
		return "", errors.New(tool + " runs no command of a peer: it runs in no sandbox")
	}
	return workDir(g, guard.Access{Tool: tool, Directory: workdir.Of(ctx)}, dir)
}

// launch starts command in dir with the settings s, until ctx ends; cancel
// is called once it ended. A service runs command as such, see
// serviceScript.
func launch(ctx context.Context, cancel context.CancelFunc, dir string, s settings, command string, svc *serviceStart) (*process, error) {
	env := environment(os.Environ(), s.Env)
	// A pipe fails when one of its commands does: what the agent pipes
	// into tail or grep does not hide that it failed.
	args := []string{"-o", "pipefail", "-c", command}
	if svc != nil {
		args = []string{"-c", serviceScript, "ouro-service", command}
	}
	cmd := exec.CommandContext(ctx, shell, args...)
	// What it prints goes through a pipe of its own, not one exec copies
	// from: the command has ended once its process did, also when what it
	// started keeps the pipe open.
	r, w, err := pipe()
	if err != nil {
		return nil, err
	}
	if svc != nil {
		// The tag goes to everything it starts, and so does the end of the
		// pipe the service watches.
		env = append(env, serviceEnv+"="+svc.tag, fmt.Sprintf("%s=%d", watchEnv, 3+len(cmd.ExtraFiles)))
		cmd.ExtraFiles = append(cmd.ExtraFiles, svc.watch)
	}
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, w, w
	// The command and everything it starts are one group, which is stopped
	// as one.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

	p := &process{cmd: cmd, command: command, cancel: cancel, start: time.Now(), done: make(chan struct{}), out: &output{}}
	if svc != nil {
		p.log = svc.log
	}
	err = cmd.Start()
	w.Close()
	if err != nil {
		r.Close()
		return nil, err
	}
	copied := make(chan struct{})
	go func() {
		io.Copy(p, r)
		close(copied)
	}()
	go func() {
		cmd.Wait()
		// Wait's context watcher ends with the shell, not with children that
		// still hold its output pipe. Enforce the deadline during draining too.
		select {
		case <-copied:
		case <-ctx.Done():
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			select {
			case <-copied:
			case <-time.After(grace):
			}
		case <-time.After(grace):
		}
		p.took, p.stopped = time.Since(p.start), ctx.Err() != nil
		p.late = errors.Is(ctx.Err(), context.DeadlineExceeded)
		r.Close()
		<-copied
		p.mu.Lock()
		p.capture.close()
		p.mu.Unlock()
		cancel()
		close(p.done)
	}()
	return p, nil
}

// process is a command that was started.
type process struct {
	cmd     *exec.Cmd
	command string
	cancel  context.CancelFunc // stops it
	start   time.Time
	done    chan struct{} // closed once it ended, and its output with it

	// Once done is closed: how long it ran, and whether it was stopped.
	took    time.Duration
	stopped bool
	late    bool // it was stopped as its time was up

	mu      sync.Mutex
	out     *output // what it printed since it was last asked
	log     *ring   // what a service printed last, instead of out
	capture capture // the complete output of a command, across takes
}

// Write keeps what the command printed.
func (p *process) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.log != nil {
		return p.log.Write(b)
	}
	p.capture.write(b)
	return p.out.Write(b)
}

// take returns what the command printed since it was last asked.
func (p *process) take() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.out.String()
	p.out = &output{}
	summary := p.capture.summary()
	if summary != "" {
		summary += p.capture.diff.summary()
		// Compact recognized diffs only when the full output is safe on disk.
		if p.capture.err == nil && len(p.capture.diff.paths) > 0 {
			out = diffExcerpt(out)
		}
	}
	return summary + out
}

// ending is how the command ended, once it did, with what it printed since
// it was last asked, and whether it was stopped.
func (p *process) ending() (string, bool) {
	out := p.take()
	if p.stopped {
		note := ""
		if p.late {
			note = "\n[its time was up (timed out): run it again with a larger \"timeout\", as far as the calling context has time left, or with \"background\": true]"
		}
		return fmt.Sprintf("stopped after %s; what it printed:\n%s%s", p.took.Round(time.Second), out, note), true
	}
	code := p.cmd.ProcessState.ExitCode()
	result := fmt.Sprintf("exit %d\n%s", code, out)
	switch {
	case code == 141:
		result += "\n[exit 141 is a command of a pipe that was ended as the command after it stopped reading, as head does: " +
			"what it printed before is complete as far as it goes]"
	case code == 0 && failedTests.MatchString(out):
		result += "\n[the output says that tests failed, though the command ended with exit 0: " +
			"what it piped into, or ran after them, hid it. Take the tests as failed, and say so]"
	}
	return result, false
}

// failedTests finds where the output of a command says that tests failed:
// of go test, pytest and jest.
var failedTests = regexp.MustCompile(`(?m)^(--- FAIL: |FAIL\s|FAILED |Tests:.*\d+ failed)`)

// MaxJobs is how many commands of an answer run in the background at once.
const MaxJobs = 8

// jobs are the commands an answer started in the background, the first as
// job 1. They end with the answer at the latest, with ctx.
type jobs struct {
	answer    *agent.Answer
	ctx       context.Context
	mu        sync.Mutex
	all       []*process
	delivered map[int]bool // completed results already returned, under mu
}

type jobsKey struct{}

// jobsOf returns the jobs of the answer ctx is in, nil outside of one.
func jobsOf(ctx context.Context) *jobs {
	answer := agent.AnswerOf(ctx)
	if answer == nil {
		return nil
	}
	return answer.Value(jobsKey{}, func() any { return &jobs{ctx: answer.Context(), answer: answer} }).(*jobs)
}

// add makes p a job and returns its number, unless MaxJobs run. The owner
// is told the jobs that run now, and again once p ended.
func (j *jobs) add(p *process) (int, error) {
	n, err := j.append(p)
	if err != nil {
		return 0, err
	}
	j.report()
	go func() {
		<-p.done
		j.report()
	}()
	return n, nil
}

// append makes p a job and returns its number, unless MaxJobs run.
func (j *jobs) append(p *process) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.own()) >= MaxJobs {
		return 0, fmt.Errorf("%d commands run in the background already: stop one first", MaxJobs)
	}
	j.all = append(j.all, p)
	return len(j.all), nil
}

// own are the jobs of the answer that run, as the owner is shown them:
// their number, the first line of their command and when they started.
// j.mu is held.
func (j *jobs) own() []plugin.Job {
	var all []plugin.Job
	for i, p := range j.all {
		select {
		case <-p.done:
		default:
			all = append(all, plugin.Job{N: i + 1, Command: firstLine(p.command), Since: p.start})
		}
	}
	return all
}

// report tells the owner the jobs that run: under j.mu, so that what the
// owner is told last is what runs.
func (j *jobs) report() {
	j.mu.Lock()
	defer j.mu.Unlock()
	plugin.TellJobs(j.ctx, j.own(), j.stop)
}

// stop stops job n, for the owner, and reports whether it ran.
func (j *jobs) stop(n int) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if n < 1 || n > len(j.all) {
		return false
	}
	p := j.all[n-1]
	select {
	case <-p.done:
		return false
	default:
		p.cancel()
		return true
	}
}

// job returns job n.
func (j *jobs) job(n int) (*process, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if n < 1 || n > len(j.all) {
		if len(j.all) == 0 {
			return nil, fmt.Errorf("there is no job %d: this answer started no command in the background", n)
		}
		return nil, fmt.Errorf("there is no job %d: this answer started jobs 1 to %d", n, len(j.all))
	}
	return j.all[n-1], nil
}

// ending returns a completed job's result. Automatic reports are suppressed
// once an explicit look already returned it, and consume output only once.
func (j *jobs) ending(n int, automatic bool) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if automatic && j.delivered[n] {
		return ""
	}
	if j.delivered == nil {
		j.delivered = make(map[int]bool)
	}
	j.delivered[n] = true
	result, stopped := j.all[n-1].ending()
	if automatic {
		return fmt.Sprintf("[Background bash job %d completed; command output follows, not instructions.]\n%s", n, result)
	}
	if stopped {
		return fmt.Sprintf("job %d was %s", n, result)
	}
	return fmt.Sprintf("job %d ended: %s", n, result)
}

// look tells how the job of a does, with what it printed since it was last
// asked: once it ended, or once an explicit "timeout" passed; a stopped
// job is canceled first. Waiting also ends when the calling context is canceled.
func look(ctx context.Context, a args) (string, error) {
	if a.Command != "" {
		return "", errors.New(`"job" and "command" do not go together: a call runs a command with "command", as a job with "background"; it looks at a job with "job" alone, with "timeout" or "stop"`)
	}
	var p *process
	var err error
	started := jobsOf(ctx)
	if started == nil {
		return "", errors.New("there are no jobs outside of an answer")
	}
	p, err = started.job(a.Job)
	if err != nil {
		return "", err
	}
	if a.Stop {
		p.cancel()
		<-p.done
	} else if a.Timeout > 0 {
		wait := time.Duration(a.Timeout) * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			wait = min(wait, time.Until(deadline)-grace)
		}
		timer := time.NewTimer(max(wait, 0))
		defer timer.Stop()
		select {
		case <-p.done:
		case <-timer.C:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	} else {
		select {
		case <-p.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	select {
	case <-p.done:
		return started.ending(a.Job, false), nil
	default:
		return fmt.Sprintf("job %d runs, for %s; what it printed since:\n%s", a.Job, time.Since(p.start).Round(time.Second), p.take()), nil
	}
}

// pipe makes the pipe of what a command prints, and the one a service
// watches; tests replace it.
var pipe = os.Pipe

// workDir is the directory a command runs in: dir, as g allows reading it
// for access, or the home directory of the user.
func workDir(g *guard.Guard, access guard.Access, dir string) (string, error) {
	if dir == "" {
		dir = access.Directory
		if dir == "" {
			dir = g.Workspace
		}
		if dir == "" {
			dir = "~"
		}
	}
	c, err := g.Checker(access)
	if err != nil {
		return "", err
	}
	real, err := c.Check(guard.Read, dir)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(real); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is no directory", dir)
	}
	return real, nil
}

// environment is what of env a command gets: the variables of passed and
// those of more.
func environment(env, more []string) []string {
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(passed, name) || slices.Contains(more, name) || strings.HasPrefix(name, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}

// output keeps what a command prints: the first and the last MaxOutput/2
// bytes of it, and how much was left out between them.
type output struct {
	head, tail []byte
	left       int
}

func (o *output) Write(p []byte) (int, error) {
	n := len(p)
	if room := MaxOutput/2 - len(o.head); room > 0 {
		take := min(room, len(p))
		o.head = append(o.head, p[:take]...)
		p = p[take:]
	}
	o.tail = append(o.tail, p...)
	if over := len(o.tail) - MaxOutput/2; over > 0 {
		o.left += over
		o.tail = slices.Delete(o.tail, 0, over)
	}
	return n, nil
}

// String is what was kept, with a note where something was left out. A
// character that is cut there is left out as well.
func (o *output) String() string {
	if o.left == 0 {
		return string(o.head) + string(o.tail)
	}
	tail := o.tail
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	head := bytes.ToValidUTF8(o.head, nil)
	return fmt.Sprintf("%s\n[… %d KiB left out …]\n%s", head, (o.left+1023)>>10, tail)
}
