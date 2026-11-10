package config

import (
	"fmt"
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

// peer_tool_max that is no number from 1 to MaxPeerToolMax is not read from
// the file, and where one gets past that the default holds.
func TestPeerToolMaxIsChecked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	for _, v := range []int{0, -3, MaxPeerToolMax + 1} {
		if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"name":"n","model":"m","access":"a","peer_tool_max":%d}`, v)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Errorf("peer_tool_max %d was read", v)
		}
		if got := (Config{PeerToolMax: &v}).PeerRounds(); got != DefaultPeerToolMax {
			t.Errorf("%d holds as %d", v, got)
		}
	}
	if got := (Config{}).PeerRounds(); got != DefaultPeerToolMax {
		t.Errorf("without a value: %d", got)
	}
	v := 7
	if got := (Config{PeerToolMax: &v}).PeerRounds(); got != 7 {
		t.Errorf("7 holds as %d", got)
	}
}
