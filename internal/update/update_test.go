package update_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
// once the tests of the checkout pass.
func TestRebuildInstallsTheWorkingTree(t *testing.T) {
	src, _ := updatetest.NewRepo(t, "v1")
	updatetest.Change(t, src, "v2") // not committed
	updatetest.Tests(t, src, `test "$$OURO_SOURCE" = "`+src+`"`)
	exe := filepath.Join(t.TempDir(), "ouro")
	u := update.Updater{Source: src, Dir: t.TempDir(), Exe: exe}
	if err := u.Rebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectFile(t, exe, "v2")
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
	if err := u.Rebuild(context.Background()); err == nil || err.Error() != "sh: broken build" || strings.Contains(out.String(), "a test failed") {
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
