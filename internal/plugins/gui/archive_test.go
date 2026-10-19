package gui

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/history"
	"ouroboros/internal/home"
	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

func TestWindowArchivesByProfile(t *testing.T) {
	var profiles []*Profile
	for _, content := range []string{"first profile", "second profile"} {
		dir := home.Dir(t.TempDir())
		a := history.Open(dir.History())
		if err := a.Append(history.Who{Session: "shared"}, []openai.Message{
			{Role: "user", Content: content},
			{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "call", Type: "function", Function: openai.FunctionCall{Name: "read", Arguments: "{}"}}}},
			{Role: "tool", Content: "result", ToolCallID: "call"},
		}, nil); err != nil {
			t.Fatal(err)
		}
		o := plugin.OwnerOf(dir, nil)
		p := fake(content, content)
		p.archive = archiveAccess{records: o.ArchiveRecords, page: o.ArchivePage}
		p.sessions = &testSessions{list: func(context.Context) ([]plugin.SessionInfo, error) {
			return []plugin.SessionInfo{{ID: "shared", Title: content}}, nil
		}}
		profiles = append(profiles, p)
	}
	Together([]*Profile{profiles[0], nil, profiles[1]})
	p, err := profiles[1].page()
	if err != nil {
		t.Fatal(err)
	}
	w := newWindow(p)
	// The window keeps its own callback slice, just like its profile snapshot.
	p.archives[0] = archiveAccess{}
	for i, content := range []string{"first profile", "second profile"} {
		sessions, err := w.Sessions(i)
		if err != nil || len(sessions) != 1 || sessions[0].ID != "owner/shared" {
			t.Fatalf("profile %d sessions: %+v, %v", i, sessions, err)
		}
		records, err := w.SessionRecords(i, sessions[0].ID)
		if err != nil || len(records) != 4 || records[0].Content != content {
			t.Fatalf("profile %d records: %+v, %v", i, records, err)
		}
		paged, err := w.SessionPage(i, sessions[0].ID, 1)
		if err != nil || len(paged.Records) != 3 || paged.Next != 4 || paged.More || paged.Records[0].Role != "assistant" {
			t.Fatalf("profile %d page: %+v, %v", i, paged, err)
		}
		first, err := w.SessionPage(i, sessions[0].ID, 0)
		if err != nil || first.Records[0].Content != content {
			t.Fatalf("profile %d leaked: %+v, %v", i, first, err)
		}
		b, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{`"seq":1`, `"role":"user"`, `"content":`, `"time":`, `"tool_calls":`, `"outcome":"answered"`} {
			if !strings.Contains(string(b), field) {
				t.Fatalf("missing %s: %s", field, b)
			}
		}
	}
	for _, i := range []int{-1, 2, 100} {
		if _, err := w.Sessions(i); err == nil {
			t.Fatalf("accepted index %d", i)
		}
		if _, err := w.SessionRecords(i, "owner/shared"); err == nil {
			t.Fatalf("accepted index %d", i)
		}
	}
	for _, id := range []string{"", "../shared", "owner/../../shared", "owner/missing"} {
		if _, err := w.SessionRecords(0, id); err == nil {
			t.Fatalf("accepted ID %q", id)
		}
	}
	for _, i := range []int{-1, 2, 100} {
		if _, err := w.SessionPage(i, "owner/shared", 0); err == nil {
			t.Fatalf("accepted page profile %d", i)
		}
	}
	if _, err := w.SessionPage(0, "owner/shared", -1); err == nil {
		t.Fatal("negative page offset")
	}
	// A session that exists only in one profile cannot leak into the other.
	dir := home.Dir(filepath.Join(t.TempDir(), "absent"))
	o := plugin.OwnerOf(dir, nil)
	empty := newWindow(page{choices: []choice{{"empty", ""}}, profiles: []*Profile{{sessions: &testSessions{}}}, archives: []archiveAccess{{records: o.ArchiveRecords, page: o.ArchivePage}}})
	if sessions, err := empty.Sessions(0); err != nil || sessions == nil || len(sessions) != 0 {
		t.Fatalf("empty: %+v, %v", sessions, err)
	}
	if _, err := empty.SessionPage(0, "owner/shared", 0); !errors.Is(err, history.ErrNotArchived) {
		t.Fatalf("cross-profile page: %v", err)
	}
	if _, err := empty.SessionRecords(0, "owner/shared"); !errors.Is(err, history.ErrNotArchived) {
		t.Fatalf("cross-profile: %v", err)
	}
}

func TestWindowArchiveErrors(t *testing.T) {
	sentinel := errors.New("archive unavailable")
	w := newWindow(page{choices: []choice{{"broken", ""}}, profiles: []*Profile{{sessions: &testSessions{list: func(context.Context) ([]plugin.SessionInfo, error) { return nil, sentinel }}}}, archives: []archiveAccess{{
		page:    func(string, int) (plugin.ArchivedPage, error) { return plugin.ArchivedPage{}, sentinel },
		records: func(string) ([]plugin.ArchivedRecord, error) { return nil, sentinel },
	}}})
	if _, err := w.Sessions(0); !errors.Is(err, sentinel) {
		t.Fatalf("list: %v", err)
	}
	if _, err := w.SessionRecords(0, "owner/session"); !errors.Is(err, sentinel) {
		t.Fatalf("records: %v", err)
	}
	if _, err := w.SessionPage(0, "owner/session", 0); !errors.Is(err, sentinel) {
		t.Fatalf("page: %v", err)
	}
	unavailable := newWindow(page{choices: []choice{{"no callbacks", ""}}})
	if _, err := unavailable.SessionPage(0, "owner/session", 0); err == nil {
		t.Fatal("missing page callback")
	}
	if _, err := unavailable.Sessions(0); err == nil {
		t.Fatal("missing list callback")
	}
	if _, err := unavailable.SessionRecords(0, "owner/session"); err == nil {
		t.Fatal("missing records callback")
	}
}

// An archive without records lists none: an empty list, not JSON null.
func TestWindowArchiveWithoutRecords(t *testing.T) {
	w := newWindow(page{choices: []choice{{"empty", ""}}, archives: []archiveAccess{{records: func(string) ([]plugin.ArchivedRecord, error) { return nil, nil }}}})
	records, err := w.SessionRecords(0, "owner/session")
	if err != nil || records == nil || len(records) != 0 {
		t.Fatalf("records: %#v, %v", records, err)
	}
}
