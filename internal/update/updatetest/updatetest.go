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
func NewRepo(t *testing.T, content string) (dir, rev string) {
	t.Helper()
	dir = t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	return dir, Commit(t, dir, content)
}

// Commit publishes a new version of the binary on main and returns its commit.
func Commit(t *testing.T, dir, content string) string {
	t.Helper()
	script := "mkdir -p \"$PREFIX/bin\" && printf '%s' '" + content + "' > \"$PREFIX/bin/ouro\"\n"
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "install.sh")
	git(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", content)
	return git(t, dir, "rev-parse", "HEAD")
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
