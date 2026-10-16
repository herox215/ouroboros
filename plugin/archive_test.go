package plugin_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"ouroboros/internal/history"
	"ouroboros/internal/home"
	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

func TestOwnerArchive(t *testing.T) {
	dir := home.Dir(filepath.Join(t.TempDir(), "profile"))
	o := plugin.OwnerOf(dir, nil)
	sessions, err := o.ArchiveSessions()
	if err != nil || sessions == nil || len(sessions) != 0 {
		t.Fatalf("empty: %v, %v", sessions, err)
	}
	if _, err := os.Stat(string(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created profile: %v", err)
	}
	a := history.Open(dir.History())
	for i := 0; i < 55; i++ {
		who := history.Who{Session: fmt.Sprintf("session-%02d", i)}
		if i%2 == 1 {
			who.Peer = "peer key"
		}
		if err := a.Append(who, []openai.Message{{Role: "user", Content: who.Session}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	sessions, err = o.ArchiveSessions()
	if err != nil || len(sessions) != 55 {
		t.Fatalf("all sessions: %d, %v", len(sessions), err)
	}
	for i, s := range sessions {
		e := expected[i]
		if s.ID != e.ID || s.Title != e.Title || !s.Modified.Equal(e.Modified) {
			t.Fatalf("entry %d: %+v != %+v", i, s, e)
		}
		want, err := a.Records(s.ID)
		if err != nil {
			t.Fatal(err)
		}
		got, err := o.ArchiveRecords(s.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("records %s: %+v, %v", s.ID, got, err)
		}
	}
	b, err := json.Marshal(sessions[0])
	if err != nil {
		t.Fatal(err)
	}
	var dto struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Modified string `json:"modified"`
	}
	if err := json.Unmarshal(b, &dto); err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse(time.RFC3339Nano, dto.Modified); err != nil || dto.ID == "" {
		t.Fatalf("JSON: %s, %v", b, err)
	}
	for _, id := range []string{"", "session-00", "../outside", "owner/../../outside", "owner/x/y", "peer-bad/x"} {
		if _, err := o.ArchiveRecords(id); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
	if _, err := o.ArchiveRecords("owner/missing"); !errors.Is(err, history.ErrNotArchived) {
		t.Fatalf("missing: %v", err)
	}
	// Reads neither resume the node nor write a configuration.
	if _, err := os.Stat(filepath.Join(string(dir), "config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configuration: %v", err)
	}
}

func TestOwnerArchiveIOErrors(t *testing.T) {
	dir := home.Dir(t.TempDir())
	if err := os.WriteFile(dir.History(), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	o := plugin.OwnerOf(dir, nil)
	if _, err := o.ArchiveSessions(); err == nil {
		t.Fatal("ignored list error")
	}
	if _, err := o.ArchiveRecords("owner/session"); err == nil {
		t.Fatal("ignored records error")
	}
}
