// Package updatetest provides a fake source repository for update tests.
package updatetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// NewRepo creates a git repository whose install.sh "builds" a binary that
// consists of the given content. It returns the repository and its commit.
// Its go.mod names no module to download.
func NewRepo(t *testing.T, content string) (dir, rev string) {
	t.Helper()
	dir = t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, "go.mod"), "module fake\n\ngo 1.21\n")
	return dir, Commit(t, dir, content)
}

// Commit publishes a new version of the binary on main and returns its commit.
func Commit(t *testing.T, dir, content string) string {
	t.Helper()
	Change(t, dir, content)
	git(t, dir, "add", "install.sh")
	git(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", content)
	return git(t, dir, "rev-parse", "HEAD")
}

// Change makes the working tree of dir build a binary with the given
// content, without a commit.
func Change(t *testing.T, dir, content string) {
	t.Helper()
	script := "mkdir -p \"$PREFIX/bin\" && printf '%s' '" + content + "' > \"$PREFIX/bin/ouro\"\n"
	write(t, filepath.Join(dir, "install.sh"), script)
}

// Tests gives dir the tests that "make test" runs: the shell command recipe.
// "make install" runs install.sh.
func Tests(t *testing.T, dir, recipe string) {
	t.Helper()
	write(t, filepath.Join(dir, "Makefile"), "install:\n\t@sh install.sh\ntest:\n\t@"+recipe+"\n")
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
