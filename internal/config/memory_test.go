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

// A bad interval that got past Load, and one of no time, turn it off; none
// keeps the default.
func TestMemoryArchive(t *testing.T) {
	for _, v := range []string{"sometimes", "0s", "-1h", ""} {
		if _, on := (Config{MemoryInterval: &v}).MemoryArchive(); on {
			t.Errorf("%q is on", v)
		}
	}
	if d, on := (Config{}).MemoryArchive(); !on || d != DefaultMemoryInterval {
		t.Errorf("the default is %v, %v", d, on)
	}
}

func TestMemoryIntervalIsChecked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte(`{"name":"n","model":"m","access":"a","memory_interval":"sometimes"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("a bad interval was read")
	}
}
