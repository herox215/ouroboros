package update_test

import (
	"bytes"
	"context"
	"os"
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
	u := update.Updater{Repo: filepath.Join(t.TempDir(), "missing"), Dir: t.TempDir(), Batch: true}
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
