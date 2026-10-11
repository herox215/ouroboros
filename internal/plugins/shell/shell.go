// Package shell is the plugin that contributes the tool bash, which runs a
// command of the shell for the owner: in a sandbox that holds the rules of
// the tools for files, see guard.Box, unless the owner lets it run without
// one.
package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"ouroboros/internal/assistant"
	"ouroboros/internal/config"
	"ouroboros/internal/guard"
	"ouroboros/internal/home"
	"ouroboros/internal/sandbox"
	"ouroboros/plugin"
)

// The limits of a command.
const (
	// DefaultTimeout is how long a command runs when the call does not say;
	// a call may say longer, up to what is left of its script.
	DefaultTimeout = time.Minute
	// MaxOutput is how much of what a command printed comes back: the start
	// and the end of it, half each.
	MaxOutput = 64 << 10
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
// to run, and nothing that is likely a secret.
var passed = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "TERM", "TMPDIR", "TZ",
	"GOPATH", "GOROOT", "GOCACHE", "GOMODCACHE", "GOPROXY", "GOPRIVATE", "GOFLAGS", "GOTOOLCHAIN",
}

// refusals are what the output of a command says of what a sandbox refused.
var refusals = []string{"operation not permitted", "read-only file system", "network is unreachable", "could not resolve host"}

// Plugin contributes bash, which asks g for the sandbox of every command and
// reads in the configuration of the node in dir whether to run it in one,
// and what the owner is shown of its calls.
func Plugin(dir home.Dir, g *guard.Guard) plugin.Plugin {
	return plugin.New("shell", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, Tool(g, func() (bool, error) {
			cfg, err := dir.Config()
			return cfg.BashWithoutSandbox, err
		}))
		plugin.Add(ctx, plugin.ToolView{Tool: assistant.ToolBash, Show: Show, Detail: assistant.HowItEnded})
		return nil
	})
}

// Show is what the owner is shown of a call of bash: the first line of its
// command.
func Show(args json.RawMessage) string {
	var call struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(args, &call) != nil || call.Command == "" {
		return ""
	}
	line, _, more := strings.Cut(strings.TrimSpace(call.Command), "\n")
	if more {
		line += " …"
	}
	return "running " + line
}

// settings are what the owner can say about bash in the configuration.
type settings struct {
	// Allow are the paths a command may write. Missing is everything the
	// tools for files may write.
	Allow *[]string `json:"allow"`
	// Deny are paths within those it neither reads nor writes.
	Deny []string `json:"deny"`
	// Network lets a command in the sandbox reach other hosts.
	Network bool `json:"network"`
	// Env are the variables of the environment a command gets besides
	// passed.
	Env []string `json:"env"`
}

type args struct {
	Command string `json:"command"`
	Dir     string `json:"dir"`
	Timeout int    `json:"timeout"`
}

// Tool returns bash, which asks g for the sandbox of every command, and
// unsandboxed whether to run it without one.
func Tool(g *guard.Guard, unsandboxed func() (bool, error)) plugin.Tool {
	t := plugin.Tool{
		Name: assistant.ToolBash,
		Description: "Run a command with bash -c. Returns its exit status, like \"exit 0\", and below it what it printed, " +
			fmt.Sprintf("its output and its errors as they came; of more than %d KiB the start and the end. ", MaxOutput>>10) +
			"Every call starts anew in dir: cd and variables do not carry over. " +
			"Unless the owner turned it off, it runs in a sandbox: it writes only where the tools for files may, " +
			"reads nothing they may not, and reaches no other host unless the owner allows it with \"network\": true in the settings of bash. " +
			"What the sandbox refuses does not pass when tried again: tell the user, and do not look for another way. " +
			"Use the tools for files to read, search and change files, and bash for builds, tests, git and other programs.",
		Schema: `{"type":"object","properties":{` +
			`"command":{"type":"string","description":"the command, as bash -c takes it"},` +
			`"dir":{"type":"string","description":"the directory it runs in: absolute, starting with \"~\", the home directory, or relative to the directory commands start in, where it runs if missing"},` +
			fmt.Sprintf(`"timeout":{"type":"integer","description":"how many seconds it may run, %d if missing, at most what is left of the script"}`, int(DefaultTimeout/time.Second)) +
			`},"required":["command"]}`,
		OwnerOnly: true,
	}
	return plugin.Configurable(t, func(s settings) (plugin.RunFunc, error) {
		if s.Allow != nil {
			if err := guard.Valid(*s.Allow); err != nil {
				return nil, err
			}
		}
		if err := guard.Valid(s.Deny); err != nil {
			return nil, err
		}
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
			return run(ctx, g, unsandboxed, s, a)
		}, nil
	})
}

// run runs the command of a with the settings s.
func run(ctx context.Context, g *guard.Guard, unsandboxed func() (bool, error), s settings, a args) (string, error) {
	if a.Command == "" {
		return "", errors.New(assistant.ToolBash + ` needs "command"`)
	}
	if _, peer := plugin.OriginFrom(ctx); peer {
		return "", errors.New(assistant.ToolBash + " is for the owner of the node alone")
	}
	if a.Timeout < 0 {
		return "", errors.New("timeout is a number of seconds")
	}
	without, err := unsandboxed()
	if err != nil {
		return "", fmt.Errorf("the configuration cannot be read, so no command runs: %w", err)
	}
	access := guard.Access{Tool: assistant.ToolBash, Allow: s.Allow, Deny: s.Deny}
	// "allow" says where a command writes; it reads everywhere else too.
	dir, err := workDir(g, guard.Access{Tool: assistant.ToolBash, Deny: s.Deny}, a.Dir)
	if err != nil {
		return "", err
	}

	timeout := DefaultTimeout
	if a.Timeout > 0 {
		timeout = time.Duration(a.Timeout) * time.Second
	}
	// The command ends before its script, so that the script gets its
	// result.
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline)-grace)
	}
	if timeout <= 0 {
		return "", errors.New("the script has no time left for a command")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	env := environment(os.Environ(), s.Env)
	if without {
		cmd = exec.CommandContext(ctx, shell, "-c", a.Command)
	} else {
		p, err := g.Box(access, s.Network)
		if err != nil {
			return "", err
		}
		if cmd, err = sandbox.Command(ctx, p, shell, "-c", a.Command); errors.Is(err, sandbox.ErrUnavailable) {
			return "", fmt.Errorf("%s runs its commands in a sandbox, and there is none: %w; "+
				`only the owner can let it run without one, with "%s": true in the configuration`, assistant.ToolBash, err, config.FieldBashUnsandboxed)
		} else if err != nil {
			return "", err
		}
		env = append(env, sandbox.Env+"=1")
	}
	out := &output{}
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, out, out
	// The command and everything it starts are one group, which is stopped
	// as one.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = grace

	start := time.Now()
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("the command was stopped after %s; what it printed:\n%s", time.Since(start).Round(time.Second), out)
	case errors.As(err, &exit):
	case err != nil:
		return "", err
	}
	result := fmt.Sprintf("exit %d\n%s", cmd.ProcessState.ExitCode(), out)
	if !without && cmd.ProcessState.ExitCode() != 0 && refused(out.String()) {
		result += "\n[it ran in a sandbox: what the output says was not permitted, on a read-only file system or " +
			"without network was refused by the sandbox, and does not pass when tried again. Tell the user, who sets what bash may]"
	}
	return result, nil
}

// workDir is the directory a command runs in: dir, as g allows reading it
// for access, or the home directory of the user.
func workDir(g *guard.Guard, access guard.Access, dir string) (string, error) {
	if dir == "" {
		dir = g.Workspace
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

// refused reports whether the output of a command says that something was
// refused that a sandbox refuses.
func refused(out string) bool {
	out = strings.ToLower(out)
	return slices.ContainsFunc(refusals, func(r string) bool { return strings.Contains(out, r) })
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
