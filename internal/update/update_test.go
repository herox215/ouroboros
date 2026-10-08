package update_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/sandbox"
	"ouroboros/internal/update"
	"ouroboros/internal/update/updatetest"
)

// sandboxed skips a test that builds in the sandbox when it runs in one
// already, as the tests do that rebuild runs: sandboxes do not nest.
func sandboxed(t *testing.T) {
	t.Helper()
	if sandbox.Nested() {
		t.Skip("in a sandbox already, where no other one starts")
	}
}

func TestRunInstallsNewestMain(t *testing.T) {
	repo, rev := updatetest.NewRepo(t, "v1")
	exe := filepath.Join(t.TempDir(), "ouro")
	if err := os.WriteFile(exe, []byte("v0"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := update.Updater{Repo: repo, Dir: t.TempDir(), Exe: exe, Revision: "old"}

	got, updated, err := u.Run(context.Background())
	if err != nil || !updated || got != rev {
		t.Fatalf("Run = %q, %v, %v; want %q, true, nil", got, updated, err, rev)
	}
	expectFile(t, exe, "v1")
	if info, _ := os.Stat(exe); info.Mode().Perm()&0o100 == 0 {
		t.Fatal("the installed binary should be executable")
	}

	// A second update reuses the checkout.
	rev2 := updatetest.Commit(t, repo, "v2")
	u.Revision = rev
	if got, updated, err := u.Run(context.Background()); err != nil || !updated || got != rev2 {
		t.Fatalf("Run = %q, %v, %v; want %q, true, nil", got, updated, err, rev2)
	}
	expectFile(t, exe, "v2")
}

func TestRunKeepsCurrentBinary(t *testing.T) {
	repo, rev := updatetest.NewRepo(t, "v1")
	exe := filepath.Join(t.TempDir(), "ouro")
	u := update.Updater{Repo: repo, Dir: t.TempDir(), Exe: exe, Revision: rev}
	got, updated, err := u.Run(context.Background())
	if err != nil || updated || got != rev {
		t.Fatalf("Run = %q, %v, %v; want %q, false, nil", got, updated, err, rev)
	}
	if _, err := os.Stat(exe); err == nil {
		t.Fatal("an up-to-date binary should not be touched")
	}
}

func TestRunReportsFailures(t *testing.T) {
	u := update.Updater{Repo: filepath.Join(t.TempDir(), "missing"), Dir: t.TempDir()}
	_, updated, err := u.Run(context.Background())
	if err == nil || updated {
		t.Fatalf("Run = %v, %v; want an error", updated, err)
	}
	if !strings.HasPrefix(err.Error(), "git: fatal: ") {
		t.Fatalf("error %q should carry what git said", err)
	}

	// A failing build leaves the binary alone and shows its output.
	repo, _ := updatetest.NewRepo(t, "'; echo broken build; exit 1; echo '") // breaks out of the fake install.sh
	var out bytes.Buffer
	exe := filepath.Join(t.TempDir(), "ouro")
	u = update.Updater{Repo: repo, Dir: t.TempDir(), Exe: exe, Out: &out}
	if _, updated, err := u.Run(context.Background()); err == nil || updated || err.Error() != "sh: broken build" {
		t.Fatalf("Run = %v, %v; want the build error", updated, err)
	}
	if _, err := os.Stat(exe); err == nil || !strings.Contains(out.String(), "broken build") {
		t.Fatalf("a failed build should not install anything; output %q", out.String())
	}
}

func TestShort(t *testing.T) {
	if got := update.Short("0123456789abcdef"); got != "0123456" {
		t.Fatalf("Short = %q", got)
	}
	if got := update.Short(""); got != "" {
		t.Fatalf("Short of an unknown revision = %q", got)
	}
}

func expectFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s = %q, %v; want %q", path, got, err, want)
	}
}

func TestRunWithoutMainBranch(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	exe := filepath.Join(t.TempDir(), "ouro")
	_, updated, err := update.Updater{Repo: repo, Dir: t.TempDir(), Exe: exe}.Run(context.Background())
	if err == nil || updated || !strings.HasSuffix(err.Error(), "has no main branch") {
		t.Fatalf("Run = %v, %v; want a missing branch", updated, err)
	}
}

// Without a working directory nothing is built and the binary stays.
func TestRunWithoutWorkingDirectory(t *testing.T) {
	repo, rev := updatetest.NewRepo(t, "v1")
	file := filepath.Join(t.TempDir(), "file")
	exe := filepath.Join(t.TempDir(), "ouro")
	for _, path := range []string{file, exe} {
		if err := os.WriteFile(path, []byte("v0"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, updated, err := update.Updater{Repo: repo, Dir: file, Exe: exe}.Run(context.Background())
	if err == nil || updated || got != rev {
		t.Fatalf("Run = %q, %v, %v; want %q and an error", got, updated, err, rev)
	}
	expectFile(t, exe, "v0")
}

// Rebuild builds the checkout as its files are now, and installs the build
// once the tests of the checkout pass. They run in the sandbox, in an
// environment of Go the frame fixes, whatever the environment of the process
// says.
func TestRebuildInstallsTheWorkingTree(t *testing.T) {
	sandboxed(t)
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOROOT", "/somewhere/else")
	t.Setenv("GOTMPDIR", t.TempDir())
	src, _ := updatetest.NewRepo(t, "v1")
	updatetest.Change(t, src, "v2") // not committed
	updatetest.Tests(t, src, `test "$$OURO_SOURCE" = "`+src+`" && test "$$GOFLAGS" = -mod=readonly && test "$$GOENV" = off && `+
		`test "$$GOWORK" = off && test "$$GOTOOLCHAIN" = local && test "$$GOVCS" = '*:off' && test "$$GOPROXY" = off && `+
		`test -z "$$GOROOT" && test -z "$$GOTMPDIR" && test "$$OURO_SANDBOX" = 1 && test -d "$$TMPDIR"`)
	exe := filepath.Join(t.TempDir(), "ouro")
	dir := t.TempDir()
	u := update.Updater{Source: src, Dir: dir, Exe: exe}
	if err := u.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectFile(t, exe, "v2")
	if _, err := os.Stat(filepath.Join(dir, "build")); !os.IsNotExist(err) {
		t.Errorf("the temporary directory is left: %v", err)
	}
}

// The temporary directory has the same path at every rebuild, and is empty
// at each: Go runs a test again when the TMPDIR it read is another.
func TestRebuildKeepsThePathOfTheTemporaryDirectory(t *testing.T) {
	sandboxed(t)
	src, _ := updatetest.NewRepo(t, "v1")
	dir := t.TempDir()
	seen := filepath.Join(dir, "stage", "seen")
	updatetest.Tests(t, src, `test ! -e "$$TMPDIR/left" && touch "$$TMPDIR/left" && echo "$$TMPDIR" >> "`+seen+`"`)
	u := update.Updater{Source: src, Dir: dir, Exe: filepath.Join(t.TempDir(), "ouro")}
	for range 2 {
		if err := u.Rebuild(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if paths := strings.Fields(string(got)); len(paths) != 2 || paths[0] != paths[1] {
		t.Errorf("TMPDIR of two rebuilds = %q; want the same path twice", paths)
	}
}

// What the build writes goes to the temporary directory, not to the
// checkout, and what it writes elsewhere is refused, also to what it
// starts: the error says that the sandbox refused it.
func TestRebuildIsSandboxed(t *testing.T) {
	sandboxed(t)
	src, _ := updatetest.NewRepo(t, "v1")
	os.WriteFile(filepath.Join(src, "Makefile"), []byte("BIN := ouro\ninstall:\n\t@echo built > $(BIN) && sh install.sh\n"+
		"test:\n\t@test ! -e ouro && sh -c 'echo x > "+filepath.Join(t.TempDir(), "outside")+"'\n"), 0o644)
	exe := filepath.Join(t.TempDir(), "ouro")
	err := update.Updater{Source: src, Dir: t.TempDir(), Exe: exe}.Rebuild(context.Background())
	if err == nil || !strings.Contains(err.Error(), "; it ran in a sandbox: without network beyond localhost") {
		t.Fatalf("a write outside: %v", err)
	}
	if _, err := os.Stat(exe); err == nil {
		t.Error("a build whose tests fail was installed")
	}
	if _, err := os.Stat(filepath.Join(src, "ouro")); err == nil {
		t.Error("the build was written to the checkout")
	}
}

// Without a sandbox, Rebuild builds only as far as the owner allows it.
func TestRebuildWithoutSandbox(t *testing.T) {
	update.WithoutSandbox(t, errors.New("no sandbox here"))
	src, _ := updatetest.NewRepo(t, "v1")
	updatetest.Tests(t, src, `test "$$OURO_SANDBOX" = "`+os.Getenv(sandbox.Env)+`"`) // not one of its own
	exe := filepath.Join(t.TempDir(), "ouro")
	var out bytes.Buffer
	u := update.Updater{Source: src, Dir: t.TempDir(), Exe: exe, Out: &out}
	for _, unsandboxed := range []func() bool{nil, func() bool { return false }} {
		u.Unsandboxed = unsandboxed
		err := u.Rebuild(context.Background())
		if !errors.Is(err, update.ErrNoSandbox) || !strings.HasSuffix(err.Error(), ": no sandbox here") {
			t.Fatalf("not allowed: %v", err)
		}
	}
	if _, err := os.Stat(exe); err == nil {
		t.Fatal("built without a sandbox, unasked")
	}
	u.Unsandboxed = func() bool { return true }
	if err := u.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectFile(t, exe, "v1")
	if !strings.Contains(out.String(), "warning: the owner lets the build run without a sandbox, with network\n") {
		t.Errorf("output %q", out.String())
	}
	// Outside of the sandbox, nothing says the sandbox refused something.
	updatetest.Tests(t, src, "echo operation not permitted; exit 1")
	if err := u.Rebuild(context.Background()); err == nil || strings.Contains(err.Error(), "sandbox") {
		t.Errorf("failed without a sandbox: %v", err)
	}
}

// Where there is a sandbox, the owner lets the build run without one all the
// same, with network.
func TestRebuildUnsandboxed(t *testing.T) {
	sandboxed(t)
	src, _ := updatetest.NewRepo(t, "v1")
	updatetest.Tests(t, src, `test "$$OURO_SANDBOX" = "`+os.Getenv(sandbox.Env)+`"`) // not one of its own
	exe := filepath.Join(t.TempDir(), "ouro")
	var out bytes.Buffer
	u := update.Updater{Source: src, Dir: t.TempDir(), Exe: exe, Out: &out, Unsandboxed: func() bool { return true }}
	if err := u.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectFile(t, exe, "v1")
	if !strings.Contains(out.String(), "warning: the owner lets the build run without a sandbox, with network\n") {
		t.Errorf("output %q", out.String())
	}
	// Without the switch the tests run in the sandbox, which this test
	// tells apart from where it runs.
	u.Unsandboxed = func() bool { return false }
	if err := u.Rebuild(context.Background()); err == nil {
		t.Error("the tests ran without a sandbox")
	}
}

// The owner lets the build be installed without running the tests; without
// that, tests that fail keep it from being installed.
func TestRebuildUntested(t *testing.T) {
	update.WithoutSandbox(t, errors.New("no sandbox here"))
	src, _ := updatetest.NewRepo(t, "v1")
	updatetest.Tests(t, src, "exit 1")
	exe := filepath.Join(t.TempDir(), "ouro")
	var out bytes.Buffer
	u := update.Updater{Source: src, Dir: t.TempDir(), Exe: exe, Unsandboxed: func() bool { return true }}
	for _, untested := range []func() bool{nil, func() bool { return false }} {
		u.Untested = untested
		if err := u.Rebuild(context.Background()); err == nil {
			t.Fatal("installed with tests that fail")
		}
	}
	if _, err := os.Stat(exe); err == nil {
		t.Fatal("installed with tests that fail")
	}
	u.Out, u.Untested = &out, func() bool { return true }
	if err := u.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectFile(t, exe, "v1")
	if !strings.Contains(out.String(), "warning: the owner lets the build be installed without running the tests\n") {
		t.Errorf("output %q", out.String())
	}
	// Without an output nothing is said.
	u.Out = nil
	if err := u.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// fakeGo puts a go first in PATH that writes what it was run with to a log
// and answers env with cache; it fails what fails names, mod or env.
func fakeGo(t *testing.T, cache, fails string) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	script := "#!/bin/sh\necho \"$*: GOFLAGS=$GOFLAGS GOENV=$GOENV GOWORK=$GOWORK GOTOOLCHAIN=$GOTOOLCHAIN GOVCS=$GOVCS GOPROXY=$GOPROXY\" >> " + log + "\n" +
		"case \"$1\" in " + fails + ") echo go: it failed; exit 1;; env) echo " + cache + ";; esac\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// Rebuild first downloads the modules, with the network of the process and
// in the fixed environment, and then builds.
func TestRebuildDownloadsFirst(t *testing.T) {
	sandboxed(t)
	t.Setenv("GOPROXY", "https://proxy.example")
	cache := t.TempDir()
	log := fakeGo(t, cache, "none")
	src, _ := updatetest.NewRepo(t, "v1")
	updatetest.Tests(t, src, "true")
	exe := filepath.Join(t.TempDir(), "ouro")
	if err := (update.Updater{Source: src, Dir: t.TempDir(), Exe: exe}).Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	fixed := "GOFLAGS=-mod=readonly GOENV=off GOWORK=off GOTOOLCHAIN=local GOVCS=*:off"
	if want := "mod download: " + fixed + " GOPROXY=https://proxy.example\nenv GOCACHE: " + fixed + " GOPROXY=https://proxy.example\n"; string(b) != want {
		t.Errorf("go was run as\n%s\nwant\n%s", b, want)
	}
}

// What fails before the build installs nothing.
func TestRebuildFailsBeforeTheBuild(t *testing.T) {
	src, _ := updatetest.NewRepo(t, "v1")
	updatetest.Tests(t, src, "true")
	exe := filepath.Join(t.TempDir(), "ouro")
	u := update.Updater{Source: src, Dir: t.TempDir(), Exe: exe}

	fakeGo(t, t.TempDir(), "mod")
	if err := u.Rebuild(context.Background()); err == nil || err.Error() != "downloading the modules go.mod names: go: go: it failed" {
		t.Errorf("a download that fails: %v", err)
	}
	fakeGo(t, t.TempDir(), "env")
	if err := u.Rebuild(context.Background()); err == nil || err.Error() != "go: go: it failed" {
		t.Errorf("a build cache that is not known: %v", err)
	}
	fakeGo(t, "/dev/null/cache", "none")
	if err := u.Rebuild(context.Background()); err == nil {
		t.Error("a build cache that cannot be made should fail")
	}
	fakeGo(t, t.TempDir(), "none")
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	if err := (update.Updater{Source: src, Dir: file, Exe: exe}).Rebuild(context.Background()); err == nil {
		t.Error("a working directory that is a file should fail")
	}
	if os.Getuid() != 0 {
		dir := t.TempDir()
		os.Mkdir(filepath.Join(dir, "stage"), 0o700)
		os.Chmod(dir, 0o500)
		t.Cleanup(func() { os.Chmod(dir, 0o700) })
		if err := (update.Updater{Source: src, Dir: dir, Exe: exe}).Rebuild(context.Background()); err == nil {
			t.Error("a working directory that cannot be written should fail")
		}
		// What a build before left behind and cannot be removed.
		left := t.TempDir()
		locked := filepath.Join(left, "build", "locked")
		os.MkdirAll(locked, 0o700)
		os.WriteFile(filepath.Join(locked, "file"), nil, 0o600)
		os.Chmod(locked, 0o500)
		t.Cleanup(func() { os.Chmod(locked, 0o700) })
		if err := (update.Updater{Source: src, Dir: left, Exe: exe}).Rebuild(context.Background()); !errors.Is(err, os.ErrPermission) {
			t.Errorf("a temporary directory that cannot be removed should fail: %v", err)
		}
	}
	if _, err := os.Stat(exe); err == nil {
		t.Error("something was installed")
	}
}

// Without make the sandbox has nothing to run.
func TestRebuildWithoutMake(t *testing.T) {
	sandboxed(t)
	src, _ := updatetest.NewRepo(t, "v1")
	truth, err := exec.LookPath("true") // which the sandbox may need to start
	if err != nil {
		t.Fatal(err)
	}
	fakeGo(t, t.TempDir(), "none")
	tools := filepath.SplitList(os.Getenv("PATH"))[0]
	os.Symlink(truth, filepath.Join(tools, "true"))
	t.Setenv("PATH", tools)
	err = update.Updater{Source: src, Dir: t.TempDir(), Exe: filepath.Join(t.TempDir(), "ouro")}.Rebuild(context.Background())
	if !errors.Is(err, exec.ErrNotFound) || errors.Is(err, update.ErrNoSandbox) {
		t.Errorf("without make: %v", err)
	}
}

// Rebuild builds with the first go in PATH, and then in the places
// install.sh puts it, where links lead.
func TestGoTool(t *testing.T) {
	empty, system := t.TempDir(), t.TempDir()
	update.SystemGo(t, system)
	t.Setenv("OURO_GO_DIR", filepath.Join(empty, "go"))
	t.Setenv("PATH", "relative:"+empty)
	if _, err := update.GoTool(); err == nil || !strings.Contains(err.Error(), "rebuild needs Go") {
		t.Fatalf("without go: %v", err)
	}
	if err := (update.Updater{Source: empty, Dir: t.TempDir()}).Rebuild(context.Background()); err == nil || !strings.Contains(err.Error(), "rebuild needs Go") {
		t.Fatalf("rebuild without go: %v", err)
	}
	os.WriteFile(filepath.Join(empty, "go"), []byte("not executable"), 0o644)
	os.MkdirAll(filepath.Join(system, "bin"), 0o755)
	real := filepath.Join(system, "bin", "go")
	os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755)
	if got, _ := update.GoTool(); got != evaluated(t, real) {
		t.Errorf("the go of all users: %q", got)
	}
	linked := t.TempDir()
	os.Symlink(real, filepath.Join(linked, "go"))
	t.Setenv("PATH", linked+":"+empty)
	if got, _ := update.GoTool(); got != evaluated(t, real) {
		t.Errorf("a link in PATH: %q", got)
	}
	paths := update.GoPaths()
	for _, want := range []string{filepath.Join(linked, "go"), filepath.Join(empty, "go"), filepath.Join(empty, "go", "bin", "go"),
		filepath.Join(empty, "go"), system, filepath.Dir(filepath.Dir(evaluated(t, real)))} {
		if !slices.Contains(paths, want) {
			t.Errorf("GoPaths lacks %s: %v", want, paths)
		}
	}

	// Without OURO_GO_DIR, Go is looked for in ~/.local/go; without a home
	// too, where it is for all users.
	home := t.TempDir()
	t.Setenv("OURO_GO_DIR", "")
	t.Setenv("HOME", home)
	if paths := update.GoPaths(); !slices.Contains(paths, filepath.Join(home, ".local", "go")) {
		t.Errorf("GoPaths lacks ~/.local/go: %v", paths)
	}
	t.Setenv("HOME", "")
	t.Setenv("PATH", "")
	if got, _ := update.GoTool(); got != evaluated(t, real) {
		t.Errorf("without a home: %q", got)
	}
	for _, path := range update.GoPaths() {
		if !filepath.IsAbs(path) {
			t.Errorf("GoPaths without a home has %q", path)
		}
	}
}

func evaluated(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func TestRebuildThatFails(t *testing.T) {
	sandboxed(t)
	exe := filepath.Join(t.TempDir(), "ouro")
	if err := os.WriteFile(exe, []byte("v0"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := update.Updater{Dir: t.TempDir(), Exe: exe}
	if err := u.Rebuild(context.Background()); err != update.ErrNoSource {
		t.Fatalf("without a source: %v", err)
	}

	// A failing test leaves the binary alone and shows its output.
	var out bytes.Buffer
	u.Source, _ = updatetest.NewRepo(t, "v1")
	u.Out = &out
	updatetest.Tests(t, u.Source, "echo a test failed; exit 1")
	if err := u.Rebuild(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "make: ") || !strings.Contains(out.String(), "a test failed") {
		t.Fatalf("failing tests: %v, output %q", err, out.String())
	}
	expectFile(t, exe, "v0")

	// So does a failing build, and the tests are not run.
	out.Reset()
	updatetest.Change(t, u.Source, "'; echo broken build; exit 1; echo '")
	if err := u.Rebuild(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "make: ") || !strings.Contains(out.String(), "broken build") ||
		strings.Contains(out.String(), "a test failed") {
		t.Fatalf("failing build: %v, output %q", err, out.String())
	}
	expectFile(t, exe, "v0")
}

// A build of the newest main keeps the checkout of the binary it replaces.
func TestRunPassesOnTheSource(t *testing.T) {
	repo, _ := updatetest.NewRepo(t, `'"$OURO_SOURCE"'`) // the fake binary is what the build is told
	exe := filepath.Join(t.TempDir(), "ouro")
	u := update.Updater{Repo: repo, Source: "/the/checkout", Dir: t.TempDir(), Exe: exe}
	if _, _, err := u.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectFile(t, exe, "/the/checkout")
}

// The binary knows the checkout it was built from, if the build said so.
func TestCheckouts(t *testing.T) {
	old := update.Source
	t.Cleanup(func() { update.Source = old })
	update.Source = ""
	if got := update.Checkouts(); got != nil {
		t.Errorf("without a source: %q", got)
	}
	update.Source = "/src/ouro"
	if got := update.Checkouts(); len(got) != 1 || got[0] != "/src/ouro" {
		t.Errorf("with a source: %q", got)
	}
}
