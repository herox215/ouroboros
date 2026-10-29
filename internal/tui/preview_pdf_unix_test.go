//go:build unix

package tui

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestPDFTimeoutKillsTheProcessGroup checks that a timeout takes a child the
// renderer started along with it, not only the renderer itself.
func TestPDFTimeoutKillsTheProcessGroup(t *testing.T) {
	dir := t.TempDir()
	// The child holds a FIFO open while it runs: reading the FIFO ends once
	// it is gone.
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	read := make(chan string, 1)
	go func() {
		var data []byte
		if f, err := os.Open(fifo); err == nil {
			data, _ = io.ReadAll(f)
			f.Close()
		}
		read <- string(data)
	}()
	renderer := fakePDFRenderer(t, dir, "sh -c 'echo started >&3; exec sleep 30' 3>\""+fifo+"\" &\nwait")
	withPDFRenderer(t, renderer)
	// Long enough for the shell to start its child while the whole module is
	// tested at once, as the pre-commit hook does.
	withPDFTimeout(t, 3*time.Second)

	if _, err := pdfPreview(filepath.Join(dir, "slow.pdf")); err == nil {
		t.Fatal("a renderer that does not finish was not ended")
	}
	select {
	case data := <-read:
		if data != "started\n" {
			t.Fatalf("the renderer started no child: %q", data)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the child of the renderer still runs, or never started")
	}
}
