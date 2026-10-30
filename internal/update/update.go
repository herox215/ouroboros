// Package update replaces the running binary with a fresh build: of the newest
// main, or of the checkout it was built from as it is now.
package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"ouroboros/internal/health"
)

// Repo is the git repository updates come from. The Makefile sets it to the
// origin of the checkout the binary was built from.
var Repo = "https://github.com/herox215/ouroboros.git"

// Source is the checkout the binary was built from, "" if unknown. The
// Makefile sets it; a build by Run keeps the one of the binary it replaces.
var Source string

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
	// Tests are the packages of Source whose tests Rebuild runs before it
	// builds, like ./internal/tui/: if one fails, nothing is built, unless
	// Measure says the tests are a reading instead of a gate.
	Tests []string
	// Measure makes the tests a health reading instead of a gate: they run,
	// and a failure does not keep the build out, see Health.
	Measure bool
	// Health, if not nil, is filled with what the run of the tests found:
	// which packages ran, whether they passed, and the first failure.
	Health *health.Record
	// Skills is the skills directory of the node, home.Dir.Skills, which
	// Rebuild installs the skills of Source into. Empty: no skill of the
	// checkout is installed.
	Skills string
}

// testPackage is what a package of Tests looks like: a path in the
// checkout, and nothing go test would take as a flag.
var testPackage = regexp.MustCompile(`^\./[A-Za-z0-9_/.-]*$`)

// ErrTestPackage is the error of a package of Tests that is no path in the
// checkout.
var ErrTestPackage = errors.New(`a package to test is a path in the checkout, like "./internal/tui/"`)

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
	if err := clearStage(stage); err != nil {
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
// as they are now, committed or not. Nothing is installed unless the tests
// of Tests and the build pass. The build goes to the stage: the checkout is
// not written.
//
// It runs the Go that GoTool finds, in an environment the frame fixes: no
// file of go env, no go.work, no other toolchain, modules only as go.mod and
// go.sum say, and none from a version control system.
func (u Updater) Rebuild(ctx context.Context) error {
	if u.Source == "" {
		return ErrNoSource
	}
	gobin, err := GoTool()
	if err != nil {
		return err
	}
	stage := filepath.Join(u.Dir, "stage")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return err
	}
	if err := clearStage(stage); err != nil {
		return err
	}
	if len(u.Tests) > 0 {
		for _, pkg := range u.Tests {
			if !testPackage.MatchString(pkg) || strings.Contains(pkg, "..") {
				return fmt.Errorf("%w: not %q", ErrTestPackage, pkg)
			}
		}
		// The tests run as the build does, not for the node that runs them.
		env := slices.DeleteFunc(goEnv(gobin), func(kv string) bool { return strings.HasPrefix(kv, "OURO_") })
		out, err := u.runIn(ctx, u.Source, env, gobin, append([]string{"test", "-count=1"}, u.Tests...)...)
		if u.Health != nil {
			*u.Health = health.Record{Ran: true, OK: err == nil, Tests: slices.Clone(u.Tests), At: time.Now()}
			if err != nil {
				u.Health.Fail = failure(out)
			}
		}
		if err != nil && !u.Measure {
			return fmt.Errorf("the tests failed, so nothing was built: %w", err)
		}
	}
	if _, err := u.runIn(ctx, u.Source, goEnv(gobin), "make", "install", "BIN="+filepath.Join(stage, "ouro")); err != nil {
		return err
	}
	// The skills of the checkout go with the build: the installed copy of a
	// skill is current, and the agent copies nothing by hand.
	if err := u.installSkills(); err != nil {
		return err
	}
	return replace(filepath.Join(stage, "bin", "ouro"), u.Exe)
}

// failure names the first failing package and test in the output of a run of
// go test, like "./internal/tui/: TestFrameHeader", for the health record.
func failure(output string) string {
	var pkg, test string
	for line := range strings.Lines(output) {
		f := strings.Fields(line)
		switch {
		case len(f) >= 3 && f[0] == "---" && f[1] == "FAIL:" && test == "":
			test = f[2]
		case len(f) >= 2 && f[0] == "FAIL" && strings.HasPrefix(f[1], "./") && pkg == "":
			pkg = f[1]
		}
	}
	switch {
	case pkg != "" && test != "":
		return pkg + ": " + test
	case pkg != "":
		return pkg
	case test != "":
		return test
	}
	return "the tests"
}

// Changes are the uncommitted changes of the checkout at Source, as git
// status --short names them, one per line: what is in the working tree and
// in no commit.
func (u Updater) Changes(ctx context.Context) ([]string, error) {
	if u.Source == "" {
		return nil, ErrNoSource
	}
	out, err := u.runIn(ctx, u.Source, nil, "git", "status", "--short")
	if err != nil {
		return nil, err
	}
	var lines []string
	for line := range strings.Lines(out) {
		if line = strings.TrimRight(line, "\n"); strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// installSkills installs the skills of the checkout at Source, its directory
// skills/, into Skills: every file of every skill. A skill that the checkout
// has not, and a file of one that it has not, stays as it is: the skills the
// owner wrote are the owner's. A checkout without skills/ has none to
// install.
func (u Updater) installSkills() error {
	if u.Skills == "" {
		return nil
	}
	root := filepath.Join(u.Source, "skills")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue // a skill is a directory, as the tool skill reads it
		}
		err := filepath.WalkDir(filepath.Join(root, e.Name()), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			dst := filepath.Join(u.Skills, strings.TrimPrefix(path, root))
			if d.IsDir() {
				return os.MkdirAll(dst, 0o755)
			}
			if !d.Type().IsRegular() {
				return nil // a link or a device of the checkout is no file of a skill
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(dst, b, 0o644)
		})
		if err != nil {
			return fmt.Errorf("installing the skill %s: %w", e.Name(), err)
		}
	}
	return nil
}

// clearStage removes what an earlier build left in the stage: a build that
// does not put its binary where it is installed from fails then, instead of
// installing the one of the build before.
func clearStage(stage string) error {
	for _, path := range []string{filepath.Join(stage, "ouro"), filepath.Join(stage, "bin", "ouro")} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// goEnv is the environment of the build with the go at gobin: that of this
// process, with what decides where modules and tools come from fixed.
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
	return u.runIn(ctx, dir, os.Environ(), name, args...)
}

// runIn is run with the environment env.
func (u Updater) runIn(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	var buf bytes.Buffer
	var out io.Writer = &buf
	if u.Out != nil {
		out = io.MultiWriter(u.Out, &buf)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	// The build is staged next to the sources and installed by replace.
	cmd.Env = append(slices.Clone(env), "PREFIX="+filepath.Join(u.Dir, "stage"), "OURO_REPO="+u.Repo, "OURO_SOURCE="+u.Source, "OURO_UPDATE=1")
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
		return "", err
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

// replace installs the binary at src as dst, and keeps the binary that was
// dst as Previous(dst), for Back. A running dst keeps its old file.
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
		err = keepPrevious(dst)
	}
	if err == nil {
		if err = rename(tmp, dst); err != nil {
			rename(Previous(dst), dst) // dst is what it was
		}
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// rename renames a file; tests replace it to make it fail.
var rename = os.Rename

// Previous is where the binary exe was before its last build or update is
// kept: Back installs it again.
func Previous(exe string) string { return exe + ".previous" }

// keepPrevious keeps the binary dst as Previous(dst), if there is one.
func keepPrevious(dst string) error {
	if err := rename(dst, Previous(dst)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ErrNoPrevious means that there is no build to go back to.
var ErrNoPrevious = errors.New("there is no build before this one to go back to")

// Back installs the binary exe was before its last build or update again,
// and keeps the one it replaces in its place: a second Back undoes the
// first.
func Back(exe string) error {
	if _, err := os.Stat(Previous(exe)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNoPrevious
		}
		return err
	}
	return replace(Previous(exe), exe)
}
