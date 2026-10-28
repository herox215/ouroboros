package cli

import (
	"errors"
	"os"
	"strings"
	"testing"

	"ouroboros/internal/home"
)

// What goes wrong with the desktop tray ends up in the log of the node.
func TestTrayLogsToTheNode(t *testing.T) {
	dir := home.Dir(t.TempDir())
	trayLog(dir)(errors.New("no status bar"))
	log, err := os.ReadFile(dir.Service().LogFile)
	if err != nil || !strings.Contains(string(log), "desktop tray: no status bar") {
		t.Fatalf("%v: %q", err, log)
	}
}
