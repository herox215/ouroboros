// Package update replaces the running binary with a fresh build of the newest main.
package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// Repo is the git repository updates come from. The Makefile sets it to the
// origin of the checkout the binary was built from.
var Repo = "https://github.com/herox215/ouroboros.git"

const branch = "main"

// Revision is the commit the running binary was built from, or "" if unknown.
func Revision() string {
	info, _ := debug.ReadBuildInfo()
	return revision(info)
}

// revision reads the commit from the build information, nil if there is none.
func revision(info *debug.BuildInfo) string {
	if info == nil {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}

// Short abbreviates a commit hash for display.
func Short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// Updater builds the newest main and installs it over Exe.
type Updater struct {
	Repo     string    // git URL or path
	Dir      string    // working directory kept between updates
	Exe      string    // binary to replace
	Revision string    // commit Exe was built from
	Out      io.Writer // git and build output
}

// Latest returns the newest commit on main.
func (u Updater) Latest(ctx context.Context) (string, error) {
	ref := "refs/heads/" + branch
	// Without Out: the answer is not worth showing.
	out, err := Updater{}.run(ctx, "", "git", "ls-remote", u.Repo, ref)
	if err != nil {
		return "", err
	}
	for line := range strings.Lines(out) {
		if rev, ok := strings.CutSuffix(strings.TrimSpace(line), "\t"+ref); ok {
			return rev, nil
		}
	}
	return "", fmt.Errorf("%s has no %s branch", u.Repo, branch)
}

// Run installs the newest main unless Exe is already built from it. It returns
// the newest commit and whether Exe was replaced.
func (u Updater) Run(ctx context.Context) (rev string, updated bool, err error) {
	if rev, err = u.Latest(ctx); err != nil || rev == u.Revision {
		return rev, false, err
	}
	src, stage := filepath.Join(u.Dir, "src"), filepath.Join(u.Dir, "stage")
	if err := os.MkdirAll(src, 0o700); err != nil {
		return rev, false, err
	}
	steps := [][]string{
		{"git", "init", "-q"},
		{"git", "fetch", "--depth", "1", u.Repo, rev},
		{"git", "checkout", "-q", "-f", "--detach", "FETCH_HEAD"},
		{"sh", "install.sh"},
	}
	for _, step := range steps {
		if _, err := u.run(ctx, src, step[0], step[1:]...); err != nil {
			return rev, false, err
		}
	}
	return rev, true, replace(filepath.Join(stage, "bin", "ouro"), u.Exe)
}

// run executes a command in dir and returns its output, which also goes to
// Out. Its error carries the reason found in the output.
func (u Updater) run(ctx context.Context, dir, name string, args ...string) (string, error) {
	var buf bytes.Buffer
	var out io.Writer = &buf
	if u.Out != nil {
		out = io.MultiWriter(u.Out, &buf)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	// The build is staged next to the sources and installed by replace.
	cmd.Env = append(os.Environ(), "PREFIX="+filepath.Join(u.Dir, "stage"), "OURO_REPO="+u.Repo, "OURO_UPDATE=1")
	if err := cmd.Run(); err != nil {
		if line := reason(buf.String()); line != "" {
			return "", fmt.Errorf("%s: %s", name, line)
		}
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return buf.String(), nil
}

// reason picks the line of a failed command's output that says why.
func reason(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "fatal: ") || strings.HasPrefix(l, "error: ") {
			return strings.TrimSpace(l)
		}
	}
	return strings.TrimSpace(lines[len(lines)-1])
}

// replace installs the binary at src as dst. A running dst keeps its old file.
func replace(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err = errors.Join(err, out.Close()); err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
