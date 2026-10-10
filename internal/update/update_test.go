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

	"ouroboros/internal/update"
	"ouroboros/internal/update/updatetest"
)

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

// Git is asked in English, as what it says is read; the build keeps the
// language of the owner.
func TestRunKeepsTheLanguageOfTheBuild(t *testing.T) {
	t.Setenv("LC_ALL", "POSIX")
	repo, _ := updatetest.NewRepo(t, `'"$LC_ALL"'`) // the fake install.sh writes it as the binary
	exe := filepath.Join(t.TempDir(), "ouro")
	u := update.Updater{Repo: repo, Dir: t.TempDir(), Exe: exe, Revision: "old"}
	if _, updated, err := u.Run(context.Background()); err != nil || !updated {
		t.Fatalf("Run = %v, %v", updated, err)
	}
	expectFile(t, exe, "POSIX")
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

// Rebuild builds the checkout as its files are now and installs the build,
// in an environment of Go the frame fixes, whatever the environment of the
// process says. It runs no tests, and the checkout is not written.
func TestRebuildInstallsTheWorkingTree(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOROOT", "/somewhere/else")
	t.Setenv("GOTMPDIR", t.TempDir())
	src, _ := updatetest.NewRepo(t, "v1")
	updatetest.Change(t, src, "v2") // not committed
	dir := t.TempDir()
	os.WriteFile(filepath.Join(src, "Makefile"), []byte("BIN := ouro\ninstall:\n\t@test \"$$OURO_SOURCE\" = \""+src+"\" && "+
		"test \"$$GOFLAGS\" = -mod=readonly && test \"$$GOENV\" = off && test \"$$GOWORK\" = off && test \"$$GOTOOLCHAIN\" = local && "+
		"test \"$$GOVCS\" = '*:off' && test -z \"$$GOROOT\" && test -z \"$$GOTMPDIR\" && test \"$(BIN)\" = \""+filepath.Join(dir, "stage", "ouro")+"\"\n"+
		"\t@sh install.sh\ntest:\n\t@exit 1\n"), 0o644)
	exe := filepath.Join(t.TempDir(), "ouro")
	u := update.Updater{Source: src, Dir: dir, Exe: exe}
	if err := u.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectFile(t, exe, "v2")
}

// fakeGo puts a go first in PATH.
func fakeGo(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// What fails before the build installs nothing.
func TestRebuildFailsBeforeTheBuild(t *testing.T) {
	src, _ := updatetest.NewRepo(t, "v1")
	updatetest.Build(t, src, "true")
	exe := filepath.Join(t.TempDir(), "ouro")
	fakeGo(t)
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	if err := (update.Updater{Source: src, Dir: file, Exe: exe}).Rebuild(context.Background()); err == nil {
		t.Error("a working directory that is a file should fail")
	}
	if _, err := os.Stat(exe); err == nil {
		t.Error("something was installed")
	}
}

// Without make there is nothing to build with.
func TestRebuildWithoutMake(t *testing.T) {
	src, _ := updatetest.NewRepo(t, "v1")
	fakeGo(t)
	t.Setenv("PATH", filepath.SplitList(os.Getenv("PATH"))[0])
	err := update.Updater{Source: src, Dir: t.TempDir(), Exe: filepath.Join(t.TempDir(), "ouro")}.Rebuild(context.Background())
	if !errors.Is(err, exec.ErrNotFound) {
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

	// The installation of a go that PATH only links to is one of them.
	elsewhere := t.TempDir()
	os.MkdirAll(filepath.Join(elsewhere, "bin"), 0o755)
	os.WriteFile(filepath.Join(elsewhere, "bin", "go"), []byte("#!/bin/sh\n"), 0o755)
	first := t.TempDir()
	os.Symlink(filepath.Join(elsewhere, "bin", "go"), filepath.Join(first, "go"))
	t.Setenv("PATH", first+":"+empty)
	if paths := update.GoPaths(); !slices.Contains(paths, evaluated(t, elsewhere)) {
		t.Errorf("GoPaths lacks the installation %s: %v", elsewhere, paths)
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
	exe := filepath.Join(t.TempDir(), "ouro")
	if err := os.WriteFile(exe, []byte("v0"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := update.Updater{Dir: t.TempDir(), Exe: exe}
	if err := u.Rebuild(context.Background()); err != update.ErrNoSource {
		t.Fatalf("without a source: %v", err)
	}

	// A failing build leaves the binary alone and shows its output.
	var out bytes.Buffer
	u.Source, _ = updatetest.NewRepo(t, "v1")
	u.Out = &out
	updatetest.Build(t, u.Source, "true")
	updatetest.Change(t, u.Source, "'; echo broken build; exit 1; echo '")
	if err := u.Rebuild(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "make: ") || !strings.Contains(out.String(), "broken build") {
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
