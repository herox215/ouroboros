package session_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	sessionplugin "ouroboros/internal/plugins/session"
)

// The sessions read the window of the thread from the configuration with
// every call; one that cannot be read leaves the default.
func TestThreadWindowFromTheConfiguration(t *testing.T) {
	dir := home.Dir(t.TempDir())
	window := "90m"
	if err := dir.SaveConfig(config.Config{Name: "n", Model: "m", Access: "a", ThreadWindow: &window}); err != nil {
		t.Fatal(err)
	}
	s := sessionplugin.In(dir, nil)
	t.Cleanup(s.Close)
	if got := s.ThreadWindow(); got != 90*time.Minute {
		t.Fatalf("configured: %v", got)
	}
	if err := os.WriteFile(filepath.Join(string(dir), "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.ThreadWindow(); got != config.DefaultThreadWindow {
		t.Fatalf("broken configuration: %v", got)
	}
}
