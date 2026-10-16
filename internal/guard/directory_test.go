package guard

import (
	"strings"
	"testing"
)

func TestProjectDirectoryIsOnlyAPathBase(t *testing.T) {
	w := newWorld(t)
	w.guard.Workspace = w.home
	project := w.home + "/share"
	a := Access{Tool: "read", Directory: project}
	if got, err := w.check(a, Read, "hello.txt"); err != nil || got != project+"/hello.txt" {
		t.Fatalf("project: %q %v", got, err)
	}
	if got, err := w.check(Access{Tool: "read"}, Read, "notes.md"); err != nil || got != w.home+"/notes.md" {
		t.Fatalf("workspace changed: %q %v", got, err)
	}
	a.Allow = &[]string{w.home + "/notes.md"}
	if _, err := w.check(a, Read, "hello.txt"); err == nil {
		t.Fatal("directory granted access")
	}
	a.Allow = nil
	if _, err := w.check(a, Read, "../.ssh/id_ed25519"); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("protected: %v", err)
	}
	a.Peer = true
	a.Allow = &[]string{"/"}
	if _, err := w.check(a, Read, "hello.txt"); err == nil {
		t.Fatal("peer got relative paths")
	}
}
