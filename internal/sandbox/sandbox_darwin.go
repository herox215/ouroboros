package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// devices are what a command may write besides what its policy allows: what
// is written to be thrown away, terminals and pseudo-terminals.
var devices = []string{os.DevNull, "/dev/zero", "/dev/tty", "/dev/ptmx"}

// resolver is the socket through which macOS resolves names: with network a
// command reaches it, or it reaches no host by its name.
const resolver = "/private/var/run/mDNSResponder"

// sandboxExec is the program that runs a command in a sandbox of macOS; tests
// replace it.
var sandboxExec = "/usr/bin/sandbox-exec"

func available() error {
	if _, err := os.Stat(sandboxExec); err != nil {
		return fmt.Errorf("%w: %s is missing", ErrUnavailable, sandboxExec)
	}
	return nil
}

// stop kills the group until it is empty: a process that was being forked
// as it was killed may have missed it. A group of nothing but processes that
// ended and wait to be collected is given up on after a while.
func stop(pgid int) bool {
	if syscall.Kill(-pgid, syscall.SIGKILL) != nil {
		return false
	}
	for range 50 {
		time.Sleep(10 * time.Millisecond)
		if syscall.Kill(-pgid, syscall.SIGKILL) != nil {
			break
		}
	}
	return true
}

func command(ctx context.Context, p Policy, path string, args []string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, sandboxExec, append([]string{"-p", profile(p), path}, args...)...), nil
}

// profile is the profile of sandbox-exec that keeps a command to what p
// allows, with the devices.
//
// A rule that comes later wins over one before it. Signals reach only the
// processes of the same sandbox. "local ip" is not used for
// what goes out: it lets every connection out. A path the profile names need
// not exist: what does not is not created either.
func profile(p Policy) string {
	var b strings.Builder
	// A command signals what it started, and no other process of the user:
	// pkill would reach the editor, the servers and the node itself.
	b.WriteString("(version 1)\n(allow default)\n(deny signal)\n(allow signal (target same-sandbox))\n")
	if p.Network {
		fmt.Fprintf(&b, "(deny network-outbound (remote unix-socket))\n"+
			"(allow network-outbound (remote unix-socket (path-literal %s)))\n", quote(resolver))
	} else {
		b.WriteString(`(deny network*)
(allow network-bind (local ip "*:*"))
(allow network-inbound (local ip "localhost:*"))
(allow network-outbound (remote ip "localhost:*"))
`)
	}
	b.WriteString("(deny file-write*)\n(allow file-write*")
	for _, w := range p.Writable {
		fmt.Fprintf(&b, "\n  (subpath %s)", quote(w))
	}
	for _, d := range devices {
		fmt.Fprintf(&b, "\n  (literal %s)", quote(d))
	}
	b.WriteString("\n  (regex #\"^/dev/ttys[0-9]+$\")\n  (regex #\"^/dev/fd/[0-9]+$\"))\n")
	rule(&b, "file-write*", p.ReadOnly)
	rule(&b, "file-read* file-write*", p.Hidden)
	return b.String()
}

// rule writes the rule that denies ops on paths, each with everything below
// it, if there are any.
func rule(b *strings.Builder, ops string, paths []string) {
	if len(paths) == 0 {
		return
	}
	fmt.Fprintf(b, "(deny %s", ops)
	for _, p := range paths {
		fmt.Fprintf(b, "\n  (subpath %s)", quote(p))
	}
	b.WriteString(")\n")
}

// quote is s as a string of a profile.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
