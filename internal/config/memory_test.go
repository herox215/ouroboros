package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The interval is written with the configuration and read again: it survives
// a restart, and off stays off.
func TestMemoryIntervalSurvivesSave(t *testing.T) {
	dir := t.TempDir()
	v := "24h"
	if err := Save(dir, Config{Name: "node", Model: "model", Access: "access", MemoryInterval: &v}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d, on := c.MemoryArchive(); !on || d != 24*time.Hour {
		t.Fatalf("interval after load: %v, %v", d, on)
	}
	off := "off"
	if err := Save(dir, Config{Name: "node", Model: "model", Access: "access", MemoryInterval: &off}); err != nil {
		t.Fatal(err)
	}
	if c, err = Load(dir); err != nil {
		t.Fatal(err)
	}
	if _, on := c.MemoryArchive(); on {
		t.Fatal("off was read as on")
	}
}

// A bad interval that got past Load, and one of no time, turn it off; a
// configuration that says nothing of it leaves it off too.
func TestMemoryArchive(t *testing.T) {
	for _, v := range []string{"sometimes", "0s", "-1h", ""} {
		if _, on := (Config{MemoryInterval: &v}).MemoryArchive(); on {
			t.Errorf("%q is on", v)
		}
	}
	if _, on := (Config{}).MemoryArchive(); on {
		t.Error("a configuration without memory_interval runs the archivist")
	}
}

// An interval that is no duration, or one of no time, is not read.
func TestMemoryIntervalIsChecked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	for _, v := range []string{"sometimes", "0s", "-1h"} {
		if err := os.WriteFile(path, []byte(`{"name":"n","model":"m","access":"a","memory_interval":"`+v+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Errorf("the interval %q was read", v)
		}
	}
}
