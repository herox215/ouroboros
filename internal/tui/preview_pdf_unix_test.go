//go:build unix

package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestPDFTimeoutKillsTheProcessGroup checks that a timeout takes a child the
// renderer started along with it, not only the renderer itself.
func TestPDFTimeoutKillsTheProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	renderer := fakePDFRenderer(t, dir, "sleep 30 &\necho $! > \""+pidFile+"\"\nwait")
	withPDFRenderer(t, renderer)
	withPDFTimeout(t, time.Second)

	if _, err := pdfPreview(filepath.Join(dir, "slow.pdf")); err == nil {
		t.Fatal("a renderer that does not finish was not ended")
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the renderer started no child: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("child pid %q: %v", data, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %d of the renderer still runs", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
