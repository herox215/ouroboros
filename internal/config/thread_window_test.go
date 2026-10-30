package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A window that is no duration, or one of no time, is not read from the
// file, and where one gets past that the default holds.
func TestThreadWindowIsChecked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	for _, v := range []string{"soon", "0s", "-1h"} {
		if err := os.WriteFile(path, []byte(`{"name":"n","model":"m","access":"a","thread_window":"`+v+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Errorf("the window %q was read", v)
		}
		if got := (Config{ThreadWindow: &v}).ThreadSessions(); got != DefaultThreadWindow {
			t.Errorf("the window %q holds as %v", v, got)
		}
	}
	if got := (Config{}).ThreadSessions(); got != DefaultThreadWindow {
		t.Errorf("without a window: %v", got)
	}
	v := " 2h "
	if got := (Config{ThreadWindow: &v}).ThreadSessions(); got != 2*time.Hour {
		t.Errorf("a window of 2h: %v", got)
	}
}
