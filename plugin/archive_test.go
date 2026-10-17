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

// A page of a session is the page the archive reads, without the node: from
// the start, after an offset, and an error for an ID that names no session.
func TestOwnerArchivePage(t *testing.T) {
	dir := home.Dir(t.TempDir())
	a := history.Open(dir.History())
	who := history.Who{Session: "long"}
	for i := 0; i < 30; i++ {
		if err := a.Append(who, []openai.Message{{Role: "user", Content: fmt.Sprint("message ", i)}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	id := history.Owner("long")
	o := plugin.OwnerOf(dir, nil)
	for _, after := range []int{0, 40, 59, 60} {
		want, err := a.Page(id, after)
		if err != nil {
			t.Fatal(err)
		}
		got, err := o.ArchivePage(id, after)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("after %d: %+v, %v; want %+v", after, got, err, want)
		}
	}
	if page, _ := o.ArchivePage(id, 0); len(page.Records) != 40 || !page.More || page.Next != 40 || page.Records[0].Content != "message 0" {
		t.Fatalf("the first page %+v", page)
	}
	if _, err := o.ArchivePage("owner/missing", 0); !errors.Is(err, history.ErrNotArchived) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := o.ArchivePage("../outside", 0); err == nil {
		t.Fatal("accepted an ID of no session")
	}
}

// A session that is archived without a record, as an empty file of the
// archive before it was a database leaves it, has no records, not none.
func TestOwnerArchiveRecordsOfAnEmptySession(t *testing.T) {
	dir := home.Dir(t.TempDir())
	legacy := filepath.Join(dir.History(), "owner")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "empty.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := plugin.OwnerOf(dir, nil).ArchiveRecords("owner/empty")
	if err != nil || records == nil || len(records) != 0 {
		t.Fatalf("records %#v, %v", records, err)
	}
	if b, _ := json.Marshal(records); string(b) != "[]" {
		t.Fatalf("JSON %s", b)
	}
}
