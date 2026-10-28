package plugin_test

import (
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/plugin"
)

// memory_interval round-trips through the settings: an interval turns the
// archivist on and survives a restart, off turns it off and leaves it absent.
func TestMemoryIntervalSettingsRoundTrip(t *testing.T) {
	o, dir, _ := built(t, config.Config{Name: "alpha", Model: "m", BaseURL: "http://x/v1"})
	if s, err := o.Settings(nil, nil); err != nil || s.MemoryInterval != "off" {
		t.Fatalf("absent: %q, %v", s.MemoryInterval, err)
	}
	holds, err := o.Change(plugin.Setting{Kind: plugin.SetMemoryInterval, Value: "24h"}, tools(), nil)
	if err != nil || holds == "" {
		t.Fatalf("set 24h: %q, %v", holds, err)
	}
	cfg, err := config.Load(dir)
	if err != nil || cfg.MemoryInterval == nil || *cfg.MemoryInterval != "24h" {
		t.Fatalf("saved: %+v, %v", cfg.MemoryInterval, err)
	}
	if interval, on := cfg.MemoryArchive(); !on || interval != 24*time.Hour {
		t.Fatalf("archive: %v, %v", interval, on)
	}
	// The next start reads the same configuration.
	again := plugin.OwnerOf(home.Dir(dir), nil)
	if s, err := again.Settings(nil, nil); err != nil || s.MemoryInterval != "24h" {
		t.Fatalf("after a restart: %q, %v", s.MemoryInterval, err)
	}
	if _, err := o.Change(plugin.Setting{Kind: plugin.SetMemoryInterval, Value: "off"}, tools(), nil); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load(dir)
	if err != nil || cfg.MemoryInterval != nil {
		t.Fatalf("off is absent: %+v, %v", cfg.MemoryInterval, err)
	}
	if _, on := cfg.MemoryArchive(); on {
		t.Error("the archivist is on after off")
	}
	if s, _ := again.Settings(nil, nil); s.MemoryInterval != "off" {
		t.Errorf("shown as %q", s.MemoryInterval)
	}
}
