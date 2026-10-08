// Package update replaces the running binary with a fresh build: of the newest
// main, or of the checkout it was built from as it is now.
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
	"slices"
	"strings"

	"ouroboros/internal/sandbox"
)

// Repo is the git repository updates come from. The Makefile sets it to the
// origin of the checkout the binary was built from.
var Repo = "https://github.com/herox215/ouroboros.git"

// Source is the checkout the binary was built from, "" if unknown. The
// Makefile sets it; a build by Run keeps the one of the binary it replaces.
var Source string

// Checkouts are the checkouts of ouroboros the binary knows: the one it was
// built from, if it knows it.
func Checkouts() []string {
	if Source == "" {
		return nil
	}
	return []string{Source}
}

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

// goDirs are where Go is looked for after PATH, as the Makefile does: in
// OURO_GO_DIR or ~/.local/go, where install.sh puts it, and /usr/local/go.
func goDirs() []string {
	dir := os.Getenv("OURO_GO_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return []string{systemGo}
		}
		dir = filepath.Join(home, ".local", "go")
	}
	return []string{dir, systemGo}
}

// systemGo is where Go is installed for all users; tests replace it.
var systemGo = "/usr/local/go"

// goPlaces are the files named go that GoTool looks at, in its order.
func goPlaces() []string {
	var places []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(dir) {
			places = append(places, filepath.Join(dir, "go"))
		}
	}
	for _, dir := range goDirs() {
		places = append(places, filepath.Join(dir, "bin", "go"))
	}
	return places
}

// GoTool returns the go command that Rebuild builds with: the first in
// PATH, in OURO_GO_DIR or ~/.local/go, or in /usr/local/go, where every
// link to it leads.
func GoTool() (string, error) {
	for _, place := range goPlaces() {
		info, err := os.Stat(place)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			continue
		}
		return filepath.EvalSymlinks(place)
	}
	return "", errors.New("rebuild needs Go, and there is none in PATH, in ~/.local/go or in /usr/local/go: run install.sh once")
}

// GoPaths are the paths of Go that no tool for files may change, as Rebuild
// runs what is there with network: every place GoTool looks at, the
// directories it looks in after PATH, and the installation of the go it
// finds, with all below them.
func GoPaths() []string {
	paths := append(goPlaces(), goDirs()...)
	if gobin, err := GoTool(); err == nil {
		paths = append(paths, filepath.Dir(filepath.Dir(gobin)))
	}
	return paths
}

// Short abbreviates a commit hash for display.
func Short(rev string) string {
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// Updater builds the newest main, or the checkout at Source, and installs it
// over Exe.
type Updater struct {
	Repo     string    // git URL or path
	Source   string    // checkout Rebuild builds
	Dir      string    // working directory kept between updates
	Exe      string    // binary to replace
	Revision string    // commit Exe was built from
	Out      io.Writer // git and build output
	// Unsandboxed reports whether the owner lets Rebuild build without a
	// sandbox where there is none. Nil is no.
	Unsandboxed func() bool
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

// ErrNoSource means that the binary does not know the checkout it was built
// from.
var ErrNoSource = errors.New("this ouro does not know its source: build it with \"make install\" in a checkout")

// Rebuild installs a build of the checkout at Source over Exe: of its files
// as they are now, committed or not. Nothing is installed unless the build
// and the tests of the checkout pass.
//
// It goes in two steps. First the frame downloads the modules go.mod and
// go.sum name, with network, and Go checks them against go.sum: nothing of
// the checkout runs then. Then the build and the tests run in a sandbox,
// without network and writing only to a temporary directory, the stage and
// the build cache of Go, with the modules downloaded. Where there is no
// sandbox, Rebuild builds only if Unsandboxed says so, as before: with
// network, and with a warning in the output.
//
// Both steps run the Go that GoTool finds, in an environment the frame
// fixes: no file of go env, no go.work, no other toolchain, modules only as
// go.mod and go.sum say, and none from a version control system.
func (u Updater) Rebuild(ctx context.Context) error {
	if u.Source == "" {
		return ErrNoSource
	}
	gobin, err := GoTool()
	if err != nil {
		return err
	}
	env := goEnv(gobin)
	if _, err := u.runIn(ctx, u.Source, env, nil, gobin, "mod", "download"); err != nil {
		return fmt.Errorf("downloading the modules go.mod names: %w", err)
	}
	// Without Out: the answer is not worth showing.
	cache, err := Updater{Dir: u.Dir}.runIn(ctx, u.Source, env, nil, gobin, "env", "GOCACHE")
	if err != nil {
		return err
	}
	stage := filepath.Join(u.Dir, "stage")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return err
	}
	// The temporary directory has the same path at every rebuild: Go keeps
	// the result of a test with the TMPDIR the test read, and with a new
	// path every test would run again.
	tmp := filepath.Join(u.Dir, "build")
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	cache = strings.TrimSpace(cache)
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return err
	}
	env = append(env, "GOPROXY=off", "TMPDIR="+tmp)
	box := &sandbox.Policy{Writable: []string{tmp, stage, cache}}
	if why := sandboxAvailable(); why != nil {
		if u.Unsandboxed == nil || !u.Unsandboxed() {
			return fmt.Errorf("%w: %w", ErrNoSandbox, why)
		}
		box = nil
		if u.Out != nil {
			fmt.Fprintf(u.Out, "warning: %v; the owner lets the build run without one, with network\n", why)
		}
	}
	// The build goes to the temporary directory: the checkout is not
	// written.
	steps := [][]string{
		{"make", "install", "BIN=" + filepath.Join(tmp, "ouro")},
		{"make", "test"},
	}
	for _, step := range steps {
		if _, err := u.runIn(ctx, u.Source, env, box, step[0], step[1:]...); err != nil {
			return err
		}
	}
	return replace(filepath.Join(stage, "bin", "ouro"), u.Exe)
}

// sandboxAvailable reports why there is no sandbox; tests replace it.
var sandboxAvailable = sandbox.Available

// ErrNoSandbox is the error of a rebuild on a system without a sandbox that
// the owner did not allow to build without one.
var ErrNoSandbox = errors.New("rebuild builds and tests in a sandbox, and there is none: " +
	`only the owner can let it build without one, with "rebuild_without_sandbox": true in the configuration`)

// refusals are what the output of a command says of what a sandbox refused.
var refusals = []string{"operation not permitted", "read-only file system", "network is unreachable"}

// sandboxed is what the error of a step that failed in the sandbox adds,
// when its output says that something was refused.
const sandboxed = "; it ran in a sandbox: without network beyond localhost, without sockets of the file system, " +
	"and writing only to a temporary directory, the stage and the build cache of Go. What the output says was not permitted, " +
	"on a read-only file system or with an unreachable network was refused by the sandbox: that does not pass when tried again. " +
	"Change what needs it, or tell the owner"

// goEnv is the environment of the build with the go at gobin: that of this
// process, with what decides where modules and tools come from fixed, and
// without a directory of its own for the files of Go, which go to TMPDIR.
func goEnv(gobin string) []string {
	fixed := []string{"GOFLAGS", "GOENV", "GOWORK", "GOTOOLCHAIN", "GOVCS", "GOROOT", "GOTMPDIR", "PATH",
		"MAKEFLAGS", "MAKELEVEL", "MFLAGS"} // of a make that runs ouro, as the tests of ouro do
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		key, _, _ := strings.Cut(kv, "=")
		return slices.Contains(fixed, key)
	})
	return append(env, "GOFLAGS=-mod=readonly", "GOENV=off", "GOWORK=off", "GOTOOLCHAIN=local", "GOVCS=*:off",
		"PATH="+filepath.Dir(gobin)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// run executes a command in dir and returns its output, which also goes to
// Out. Its error carries the reason found in the output.
func (u Updater) run(ctx context.Context, dir, name string, args ...string) (string, error) {
	return u.runIn(ctx, dir, os.Environ(), nil, name, args...)
}

// runIn is run with the environment env, in the sandbox of box unless it is
// nil.
func (u Updater) runIn(ctx context.Context, dir string, env []string, box *sandbox.Policy, name string, args ...string) (string, error) {
	var buf bytes.Buffer
	var out io.Writer = &buf
	if u.Out != nil {
		out = io.MultiWriter(u.Out, &buf)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if box != nil {
		var err error
		if cmd, err = sandbox.Command(ctx, *box, name, args...); err != nil {
			return "", err
		}
	}
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	// The build is staged next to the sources and installed by replace.
	cmd.Env = append(slices.Clone(env), "PREFIX="+filepath.Join(u.Dir, "stage"), "OURO_REPO="+u.Repo, "OURO_SOURCE="+u.Source, "OURO_UPDATE=1")
	if box != nil {
		cmd.Env = append(cmd.Env, sandbox.Env+"=1")
	}
	// reason reads what git says in English, whatever language it speaks.
	if name == "git" {
		cmd.Env = append(cmd.Env, "LC_ALL=C")
	}
	if err := cmd.Run(); err != nil {
		if line := reason(buf.String()); line != "" {
			err = fmt.Errorf("%s: %s", filepath.Base(name), line)
		} else {
			err = fmt.Errorf("%s: %w", filepath.Base(name), err)
		}
		if box != nil && refused(buf.String()) {
			err = fmt.Errorf("%w%s", err, sandboxed)
		}
		return "", err
	}
	return buf.String(), nil
}

// refused reports whether output says that a sandbox refused something.
func refused(output string) bool {
	output = strings.ToLower(output)
	return slices.ContainsFunc(refusals, func(r string) bool { return strings.Contains(output, r) })
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
