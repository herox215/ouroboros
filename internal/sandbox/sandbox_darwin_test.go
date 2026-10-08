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

// A path is quoted in the profile the way the profile reads it, and what is
// read only and hidden comes after what is writable, so that it wins.
func TestProfile(t *testing.T) {
	p := profile(Policy{Writable: []string{`/a "b"\c`}})
	if !strings.Contains(p, `(subpath "/a \"b\"\\c")`) || !strings.HasPrefix(p, "(version 1)\n(allow default)\n(deny network*)\n") {
		t.Errorf("profile:\n%s", p)
	}
	if strings.Contains(p, "(deny file-read*") || strings.Count(p, "(deny file-write*") != 1 {
		t.Errorf("a profile without what is read only or hidden:\n%s", p)
	}
	p = profile(Policy{Writable: []string{"/"}, ReadOnly: []string{"/r", "/s"}, Hidden: []string{"/h"}, Network: true})
	want := "(deny file-write*\n  (subpath \"/r\")\n  (subpath \"/s\"))\n(deny file-read* file-write*\n  (subpath \"/h\"))\n"
	if !strings.HasSuffix(p, want) || strings.Contains(p, "(deny network*)") ||
		!strings.Contains(p, "(deny network-outbound (remote unix-socket))\n(allow network-outbound (remote unix-socket (path-literal \"/private/var/run/mDNSResponder\")))\n") {
		t.Errorf("profile with network:\n%s", p)
	}
}
