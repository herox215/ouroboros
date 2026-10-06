package sandbox

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// Without sandbox-exec there is no sandbox.
func TestWithoutSandboxExec(t *testing.T) {
	t.Setenv(Env, "") // what is tested here starts no sandbox
	old := sandboxExec
	sandboxExec = filepath.Join(t.TempDir(), "sandbox-exec")
	t.Cleanup(func() { sandboxExec = old })
	if err := Available(); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "is missing") {
		t.Errorf("available: %v", err)
	}
	if _, err := Command(context.Background(), Policy{}, "true"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("command: %v", err)
	}
}

// A path is quoted in the profile the way the profile reads it.
func TestProfile(t *testing.T) {
	p := profile([]string{`/a "b"\c`})
	if !strings.Contains(p, `(subpath "/a \"b\"\\c")`) || !strings.HasPrefix(p, "(version 1)\n(allow default)\n(deny network*)\n") {
		t.Errorf("profile:\n%s", p)
	}
}
