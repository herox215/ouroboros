package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// devices are what a command may write besides what its policy allows: what
// is written to be thrown away, terminals and pseudo-terminals.
var devices = []string{os.DevNull, "/dev/zero", "/dev/tty", "/dev/ptmx"}

// sandboxExec is the program that runs a command in a sandbox of macOS; tests
// replace it.
var sandboxExec = "/usr/bin/sandbox-exec"

func available() error {
	if _, err := os.Stat(sandboxExec); err != nil {
		return fmt.Errorf("%w: %s is missing", ErrUnavailable, sandboxExec)
	}
	return nil
}

func command(ctx context.Context, writable []string, path string, args []string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, sandboxExec, append([]string{"-p", profile(writable), path}, args...)...), nil
}

// profile is the profile of sandbox-exec that keeps a command off the network
// and lets it write to writable alone, with the devices.
//
// A rule that comes later wins over one before it. "local ip" is not used for
// what goes out: it lets every connection out.
func profile(writable []string) string {
	var b strings.Builder
	b.WriteString(`(version 1)
(allow default)
(deny network*)
(allow network-bind (local ip "*:*"))
(allow network-inbound (local ip "localhost:*"))
(allow network-outbound (remote ip "localhost:*"))
(deny file-write*)
(allow file-write*`)
	for _, w := range writable {
		fmt.Fprintf(&b, "\n  (subpath %s)", quote(w))
	}
	for _, d := range devices {
		fmt.Fprintf(&b, "\n  (literal %s)", quote(d))
	}
	b.WriteString("\n  (regex #\"^/dev/ttys[0-9]+$\")\n  (regex #\"^/dev/fd/[0-9]+$\"))\n")
	return b.String()
}

// quote is s as a string of a profile.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
