package plugin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/plugin"
)

// thread_window round-trips through the settings: the default holds while the
// configuration says nothing of it, a duration holds from the next message of
// the thread on and survives a restart, and one that does not parse is
// refused and leaves the old window.
func TestThreadWindowSettingsRoundTrip(t *testing.T) {
	o, dir, _ := built(t, config.Config{Name: "alpha", Model: "m", BaseURL: "http://x/v1"})
	if s, err := o.Settings(nil, nil); err != nil || s.ThreadWindow != "3h" {
		t.Fatalf("absent: %q, %v", s.ThreadWindow, err)
	}
	holds, err := o.Change(plugin.Setting{Kind: plugin.SetThreadWindow, Value: "90m"}, tools(), nil)
	if err != nil || holds == "" {
		t.Fatalf("set 90m: %q, %v", holds, err)
	}
	cfg, err := config.Load(dir)
	if err != nil || cfg.ThreadWindow == nil || *cfg.ThreadWindow != "90m" {
		t.Fatalf("saved: %+v, %v", cfg.ThreadWindow, err)
	}
	if window := cfg.ThreadSessions(); window != 90*time.Minute {
		t.Fatalf("window %v", window)
	}
	// The next start reads the same configuration.
	again := plugin.OwnerOf(home.Dir(dir), nil)
	if s, err := again.Settings(nil, nil); err != nil || s.ThreadWindow != "90m" {
		t.Fatalf("after a restart: %q, %v", s.ThreadWindow, err)
	}
	// One that does not hold is refused and the old window stays.
	if _, err := o.Change(plugin.Setting{Kind: plugin.SetThreadWindow, Value: "0"}, tools(), nil); err == nil {
		t.Fatal("a window of zero is accepted")
	}
	if _, err := o.Change(plugin.Setting{Kind: plugin.SetThreadWindow, Value: "soon"}, tools(), nil); err == nil {
		t.Fatal("a window that does not parse is accepted")
	}
	if s, _ := again.Settings(nil, nil); s.ThreadWindow != "90m" {
		t.Errorf("shown as %q", s.ThreadWindow)
	}
	if cfg, err := config.Load(dir); err != nil || cfg.ThreadWindow == nil || *cfg.ThreadWindow != "90m" {
		t.Fatalf("the old window stays: %+v, %v", cfg.ThreadWindow, err)
	}
}

// The window is read and set through the owner's methods too: one that is
// no duration is refused, and a configuration that cannot be read fails both.
func TestThreadWindowOfTheOwner(t *testing.T) {
	o, dir, _ := built(t, config.Config{Name: "alpha", Model: "m"})
	if got, err := o.ThreadWindow(); err != nil || got != "3h" {
		t.Fatalf("absent: %q, %v", got, err)
	}
	if holds, err := o.SetThreadWindow(" 2h "); err != nil || holds == "" {
		t.Fatalf("set 2h: %q, %v", holds, err)
	}
	if got, err := o.ThreadWindow(); err != nil || got != "2h" {
		t.Fatalf("after set: %q, %v", got, err)
	}
	if _, err := o.SetThreadWindow("-1h"); err == nil || !strings.Contains(err.Error(), "duration") {
		t.Fatalf("a negative window: %v", err)
	}
	if got, _ := o.ThreadWindow(); got != "2h" {
		t.Fatalf("a refused window changed it: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := o.ThreadWindow(); err == nil {
		t.Fatal("read the window of a broken configuration")
	}
	if _, err := o.SetThreadWindow("1h"); err == nil {
		t.Fatal("set the window of a broken configuration")
	}
}
