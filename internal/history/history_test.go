package history

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/openai"
)

var (
	owner    = Who{Session: "chat-1"}
	peerA    = Who{Session: "conv", Peer: "key-a"}
	peerB    = Who{Session: "conv", Peer: "key-b"}
	question = []openai.Message{{Role: "user", Content: "What did we decide about the Roof?"}}
)

// at returns an archive in a directory of the test whose clock stands at
// 2026-10-06 12:00 UTC and goes on a minute with every answer.
func at(t *testing.T) *Archive {
	t.Helper()
	a := Open(filepath.Join(t.TempDir(), "history"))
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	t.Cleanup(func() { a.Close() })
	a.now = func() time.Time {
		now = now.Add(time.Minute)
		return now
	}
	return a
}

// answer is an answer with a call of a tool.
func answer(user, result, reply string) []openai.Message {
	return []openai.Message{
		{Role: "user", Content: user},
		{Role: "assistant", Content: "Let me look.", ToolCalls: []openai.ToolCall{{ID: "c1", Type: "function",
			Function: openai.FunctionCall{Name: "run", Arguments: `{"script":"read({path: \"~/notes.md\"})"}`}}}},
		{Role: "tool", ToolCallID: "c1", Content: result},
		{Role: "assistant", Content: reply},
	}
}

// all returns the records of the conversation of who.
func all(t *testing.T, a *Archive, who Who) []Record {
	t.Helper()
	var out []Record
	if err := a.records(a.file(who, Name(who.Session)), func(r Record) bool { out = append(out, r); return true }); err != nil {
		t.Fatal(err)
	}
	return out
}

// Every answer is kept in full, with how it ended, and goes on where the
// last one ended: also in another process, and after a restart.
func TestAppend(t *testing.T) {
	a := at(t)
	big := strings.Repeat("the whole file\n", 10000)
	if err := a.Append(owner, answer("read my notes", big, "Done."), nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Append(owner, question, errors.New("llm down")); err != nil {
		t.Fatal(err)
	}
	if err := a.Append(owner, question, fmt.Errorf("stopped: %w", context.Canceled)); err != nil {
		t.Fatal(err)
	}
	got := all(t, a, owner)
	if len(got) != 9 {
		t.Fatalf("records %+v", got)
	}
	for i, r := range got {
		if r.Seq != i+1 {
			t.Errorf("record %d has seq %d", i, r.Seq)
		}
	}
	if got[0].Answer != 1 || got[4].Answer != 1 || got[5].Answer != 2 || got[8].Answer != 3 {
		t.Errorf("answers %+v", got)
	}
	if got[2].Content != big || got[2].ToolCallID != "c1" || got[1].ToolCalls[0] != (Call{"c1", "run", `{"script":"read({path: \"~/notes.md\"})"}`}) {
		t.Errorf("not kept in full: %+v", got[1:3])
	}
	if got[4].Outcome != Answered || got[6].Outcome != Failed || got[8].Outcome != Canceled || got[4].Role != "end" {
		t.Errorf("outcomes %+v", got)
	}
	if strings.Contains(fmt.Sprint(got), "llm down") {
		t.Error("why an answer failed is kept")
	}
	if !got[0].Time.Equal(time.Date(2026, 10, 6, 12, 1, 0, 0, time.UTC)) || !got[8].Time.Equal(time.Date(2026, 10, 6, 12, 3, 0, 0, time.UTC)) {
		t.Errorf("times %v, %v", got[0].Time, got[8].Time)
	}
	for _, p := range []string{filepath.Join(a.dir, "ouroboros.sqlite"), a.dir} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s: %v", p, info.Mode())
		}
	}

	// Another process went on with the conversation: this one goes on
	// after it.
	other := Open(a.dir)
	other.now = a.now
	if err := other.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	if got := all(t, a, owner); len(got) != 13 || got[12].Seq != 13 || got[12].Answer != 5 {
		t.Fatalf("after another process: %+v", got[9:])
	}
	// A fresh handle continues the authoritative SQL state.
	restarted := Open(a.dir)
	defer restarted.Close()
	restarted.now = a.now
	if err := restarted.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	got = all(t, a, owner)
	if len(got) != 15 || got[14].Answer != 6 {
		t.Fatalf("restart: %+v", got)
	}

}

// The owner and every peer have a directory of their own, named by the key
// of the peer, and a conversation is a file named by its ID, which is hashed
// when it could name something else.
func TestParties(t *testing.T) {
	a := at(t)
	for _, who := range []Who{owner, peerA, peerB, {Session: "../../etc/passwd"}} {
		if err := a.Append(who, question, nil); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := a.List()
	if err != nil || len(entries) != 4 {
		t.Fatalf("sessions: %+v %v", entries, err)
	}
	for _, e := range entries {
		dir, name, _ := strings.Cut(e.ID, "/")
		if !party.MatchString(dir) || !named.MatchString(name) {
			t.Errorf("ID %q", e.ID)
		}
	}

	if Name("../../etc/passwd") == "../../etc/passwd" || len(Name("../../etc/passwd")) != 32 || Name("a-B_9") != "a-B_9" {
		t.Error("session names")
	}
	if a.partyDir(peerA) == a.partyDir(peerB) || a.partyDir(peerA) == a.partyDir(owner) {
		t.Error("parties share a directory")
	}
}

// Nothing is kept where the archive cannot write, and the caller is told.
func TestAppendFails(t *testing.T) {
	a := at(t)
	os.WriteFile(a.dir, nil, 0o600) // a file where the directory would be
	if err := a.Append(owner, question, nil); err == nil {
		t.Error("an archive below a file should fail")
	}

	a = at(t)
	os.MkdirAll(a.file(owner, "chat-1"), 0o700) // a directory where the file would be
	if err := a.Append(owner, question, nil); err == nil {
		t.Error("a conversation that is a directory should fail")
	}

}

// SQL write failures roll back the whole answer and leave sequencing intact.
func TestWriteFails(t *testing.T) {
	a := at(t)
	if err := a.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	db, err := a.database()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TRIGGER reject_end BEFORE INSERT ON history_records WHEN new.seq=4 BEGIN SELECT RAISE(ABORT,'broken'); END"); err != nil {
		t.Fatal(err)
	}
	if err = a.Append(owner, question, nil); err == nil {
		t.Fatal("write should fail")
	}
	if got := all(t, a, owner); len(got) != 2 {
		t.Fatalf("partial answer: %+v", got)
	}
	if _, err = db.Exec("DROP TRIGGER reject_end"); err != nil {
		t.Fatal(err)
	}
	if err = a.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	if got := all(t, a, owner); len(got) != 4 || got[2].Seq != 3 || got[2].Answer != 2 {
		t.Fatalf("retry: %+v", got)
	}
}

// The owner searches its current conversation or all of its own, a peer
// only the one it is in: no peer reaches what the owner or another peer
// said.
func TestSearchScope(t *testing.T) {
	a := at(t)
	a.Append(owner, answer("what about the roof?", "notes: the ROOF is red", "It is red."), nil)
	a.Append(Who{Session: "chat-2"}, answer("and the roof of the shed?", "nothing", "Green."), nil)
	a.Append(Who{Session: "chat-3"}, answer("the garden", "nothing", "Later."), nil)
	a.Append(peerA, answer("the roof of peer a", "x", "secret of a"), nil)
	// The most recent last: a file system may give the same time to all.
	for i, name := range []string{"chat-1", "chat-2", "chat-3"} {
		at := time.Now().Add(time.Duration(i-3) * time.Minute)
		db, _ := a.database()
		db.Exec("UPDATE history_sessions SET modified=? WHERE id=?", at.UnixNano(), Owner(name))
	}

	out, hits, err := a.Search(owner, "roof", false, 0)
	if err != nil || hits != 2 || strings.Contains(out, "chat-2") || strings.Contains(out, "peer a") {
		t.Fatalf("current: %q, %d, %v", out, hits, err)
	}
	if !strings.Contains(out, "chat-1 (this conversation) · 2026-10-06 ") || !strings.Contains(out, " · #1 user: what about the roof?\n") ||
		!strings.Contains(out, "#3 tool: notes: the ROOF is red\n") || !strings.HasSuffix(out, "[2 hits in this conversation]") {
		t.Errorf("current: %q", out)
	}
	out, hits, err = a.Search(Who{Session: "chat-3"}, "ROOF", true, 0)
	if err != nil || hits != 3 || strings.Contains(out, "peer a") || !strings.HasSuffix(out, "[3 hits in 3 conversations]") {
		t.Fatalf("all: %q, %d, %v", out, hits, err)
	}
	// The current conversation first, then the most recent.
	if strings.Index(out, "chat-2") > strings.Index(out, "chat-1") {
		t.Errorf("order: %q", out)
	}
	out, _, _ = a.Search(Who{Session: "chat-1"}, "garden", true, 0)
	if !strings.HasPrefix(out, "chat-3 · ") {
		t.Errorf("another conversation: %q", out)
	}
	// From the oldest and from the middle one, each conversation comes once.
	for _, current := range []string{"chat-1", "chat-2"} {
		out, hits, _ := a.Search(Who{Session: current}, "roof", true, 0)
		other, times := map[string]string{"chat-1": "chat-2", "chat-2": "chat-1"}[current], map[string]int{"chat-1": 1, "chat-2": 2}[current]
		if hits != 3 || !strings.HasPrefix(out, current+" (this conversation) · ") || strings.Count(out, other+" · ") != times {
			t.Errorf("from %s: %q", current, out)
		}
	}
	if out, _, _ := a.Search(owner, "script", false, 0); !strings.Contains(out, "#2 assistant: run {\"script\"") {
		t.Errorf("calls are searched by name and arguments: %q", out)
	}

	// A peer finds what it said itself, and nothing else.
	out, hits, err = a.Search(peerA, "roof", false, 0)
	if err != nil || hits != 1 || !strings.Contains(out, "the roof of peer a") {
		t.Fatalf("peer: %q, %d, %v", out, hits, err)
	}
	for _, who := range []Who{peerB, {Session: "chat-1", Peer: "key-b"}, {Session: "chat-1", Peer: "key-a"}} {
		if out, hits, err := a.Search(who, "roof", false, 0); err != nil || hits != 0 || out != `no record of this conversation contains "roof"` {
			t.Errorf("%+v: %q, %d, %v", who, out, hits, err)
		}
	}
	if _, _, err := a.Search(peerA, "roof", true, 0); !errors.Is(err, ErrScope) {
		t.Errorf("a peer searched all: %v", err)
	}
	if _, _, err := a.Search(owner, " ", false, 0); err == nil {
		t.Error("an empty query should fail")
	}
	if out, _, err := a.Search(Who{Session: "new"}, "roof", true, 0); err != nil || !strings.Contains(out, "[3 hits in 3 conversations]") {
		t.Errorf("from a conversation that is not archived yet: %q, %v", out, err)
	}
	empty := at(t)
	if out, _, err := empty.Search(owner, "roof", true, 0); err != nil || out != `no record of 0 conversations contains "roof"` {
		t.Errorf("an empty archive: %q, %v", out, err)
	}
}

// A search returns at most limit hits and maxSearchBytes, with excerpts
// around the hit.
func TestSearchBounds(t *testing.T) {
	a := at(t)
	var msgs []openai.Message
	for i := range 2 * MaxHits {
		msgs = append(msgs, openai.Message{Role: "user", Content: fmt.Sprintf("needle %d", i)})
	}
	a.Append(owner, msgs, nil)
	if out, hits, _ := a.Search(owner, "needle", false, 0); hits != DefaultHits ||
		!strings.HasSuffix(out, "[more hits are not shown; searched this conversation: search for something more precise]") {
		t.Errorf("default: %d, %q", hits, out)
	}
	if _, hits, _ := a.Search(owner, "needle", false, 1000); hits != MaxHits {
		t.Errorf("at most %d: %d", MaxHits, hits)
	}
	if _, hits, _ := a.Search(owner, "needle", false, 1); hits != 1 {
		t.Errorf("limit 1: %d", hits)
	}
	if _, hits, _ := a.Search(owner, "needle", false, 3); hits != 3 {
		t.Errorf("limit 3: %d", hits)
	}
	if out, hits, _ := a.Search(owner, "needle", false, 2*MaxHits); hits != MaxHits || !strings.Contains(out, "are not shown") {
		t.Errorf("more than shown: %d", hits)
	}

	long := strings.Repeat("a", 500) + " NEEDLE\n\nhere " + strings.Repeat("b", 500)
	b := at(t)
	b.Append(owner, []openai.Message{{Role: "user", Content: long}}, nil)
	out, _, _ := b.Search(owner, "needle", false, 0)
	want := "…" + strings.Repeat("a", excerpt-1) + " NEEDLE here " + strings.Repeat("b", excerpt-7) + "…"
	if !strings.Contains(out, ": "+want+"\n") {
		t.Errorf("excerpt: %q", out)
	}
	// Where lower case changes the length, the excerpt is in lower case.
	c := at(t)
	c.Append(owner, []openai.Message{{Role: "user", Content: "İstanbul needle"}}, nil)
	if out, _, _ := c.Search(owner, "needle", false, 0); !strings.Contains(out, ": istanbul needle\n[1 hit in this conversation]") {
		t.Errorf("unicode: %q", out)
	}

	// A hit that fills maxSearchBytes exactly is given.
	e := at(t)
	e.Append(owner, question, nil)
	prefix := len(fmt.Sprintf("chat-1 (this conversation) · %s · #1 user: ", when(time.Date(2026, 10, 6, 12, 1, 0, 0, time.UTC))))
	exact := strings.Repeat("x", maxSearchBytes-prefix-1)
	e.Append(owner, []openai.Message{{Role: "user", Content: exact}}, nil)
	if out, hits, _ := e.Search(Who{Session: "chat-1"}, exact, false, 0); hits != 1 || len(out) != maxSearchBytes+len("[1 hit in this conversation]") {
		t.Errorf("exactly full: %d hits, %d bytes", hits, len(out))
	}

	// Long hits end the search at maxSearchBytes.
	d := at(t)
	msgs = nil
	needle := strings.Repeat("needle ", 60)
	for range MaxHits {
		msgs = append(msgs, openai.Message{Role: "user", Content: strings.Repeat("x ", 200) + needle + strings.Repeat(" y", 200)})
	}
	d.Append(owner, msgs, nil)
	if out, hits, _ := d.Search(owner, needle, false, MaxHits); len(out) > maxSearchBytes+200 || hits >= MaxHits || hits == 0 || !strings.Contains(out, "are not shown") {
		t.Errorf("bytes: %d hits in %d bytes", hits, len(out))
	}
}

// A conversation that cannot be read fails the search.
func TestSearchFails(t *testing.T) {
	a := at(t)
	os.MkdirAll(a.file(owner, "chat-1"), 0o700)
	if _, _, err := a.Search(owner, "x", false, 0); err == nil {
		t.Error("a conversation that is a directory should fail")
	}
	b := at(t)
	os.MkdirAll(b.dir, 0o700)
	os.WriteFile(filepath.Join(b.dir, ownerDir), nil, 0o600)
	if _, _, err := b.Search(owner, "x", true, 0); err == nil {
		t.Error("an owner directory that is a file should fail")
	}
}

// Read returns the records in order, bounded, with where to go on.
func TestRead(t *testing.T) {
	a := at(t)
	a.Append(owner, answer("read my notes", "notes", "Done."), nil)
	a.Append(owner, question, context.Canceled)

	out, err := a.Read(owner, "", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"#1 · answer 1 · 2026-10-06 ", " · user\nread my notes\n",
		"#2 · answer 1 · ", " · assistant\nLet me look.\n→ run {\"script\"",
		"#3 · answer 1 · ", " · tool result\nnotes\n",
		"#4 · answer 1 · ", " · assistant\nDone.\n",
		"#5 · answer 1 · ", " · the answer answered\n",
		"#7 · answer 2 · ", " · the answer canceled\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "[records 1 to 7 of 7]") {
		t.Errorf("end: %q", out)
	}
	out, _ = a.Read(owner, "chat-1", 2, 2, 0)
	if !strings.HasPrefix(out, "#2 ") || strings.Contains(out, "#4 ") || !strings.HasSuffix(out, "[records 2 to 3 of 7: continue with from 4]") {
		t.Errorf("a part: %q", out)
	}

	if out, _ := a.Read(owner, "", 0, 1, 0); !strings.HasSuffix(out, "[records 1 to 1 of 7: continue with from 2]") {
		t.Errorf("one record: %q", out)
	}
	if out, _ := a.Read(owner, "", 8, 0, 0); out != "the session has 7 records: there is none from 8 on" {
		t.Errorf("beyond: %q", out)
	}
	if out, err := a.Read(Who{Session: "new"}, "", 0, 0, 0); err != nil || out != "nothing of this conversation is archived yet" {
		t.Errorf("not archived yet: %q, %v", out, err)
	}
	for _, session := range []string{"chat-9", "../owner/chat-1", ""} {
		if session == "" {
			continue
		}
		if _, err := a.Read(Who{Session: "new"}, session, 0, 0, 0); err == nil || !strings.Contains(err.Error(), "there is no session") {
			t.Errorf("%q: %v", session, err)
		}
	}

	// Many records: at most MaxRecords, and MaxReadBytes.
	b := at(t)
	var msgs []openai.Message
	for i := range 2 * MaxRecords {
		msgs = append(msgs, openai.Message{Role: "user", Content: fmt.Sprint(i)})
	}
	b.Append(owner, msgs, nil)
	if out, _ := b.Read(owner, "", 0, 10000, 0); !strings.HasSuffix(out, fmt.Sprintf("[records 1 to %d of %d: continue with from %d]", MaxRecords, 2*MaxRecords+1, MaxRecords+1)) {
		t.Errorf("at most %d: %q", MaxRecords, out[len(out)-80:])
	}
	if out, _ := b.Read(owner, "", 0, 0, 0); !strings.HasSuffix(out, fmt.Sprintf("[records 1 to %d of %d: continue with from %d]", DefaultRecords, 2*MaxRecords+1, DefaultRecords+1)) {
		t.Errorf("default: %q", out[len(out)-80:])
	}
	c := at(t)
	c.Append(owner, []openai.Message{{Role: "user", Content: strings.Repeat("x", 10<<10)}, {Role: "user", Content: strings.Repeat("y", 10<<10)}, {Role: "user", Content: "z"}}, nil)
	out, _ = c.Read(owner, "", 0, 0, 0)
	if len(out) > MaxReadBytes || strings.Contains(out, "yyy") || !strings.HasSuffix(out, "[records 1 to 1 of 4: continue with from 2]") {
		t.Errorf("bytes: %d, %q", len(out), out[len(out)-80:])
	}
	// What fills the payload budget exactly is given.
	head := len(show(Record{Seq: 1, Answer: 1, Time: time.Date(2026, 10, 6, 12, 1, 0, 0, time.UTC), Role: "user"}))
	f2 := at(t)
	f2.Append(owner, []openai.Message{{Role: "user", Content: strings.Repeat("x", MaxReadBytes-readNote-head-1)}}, nil)
	if out, _ := f2.Read(owner, "", 0, 1, 0); !strings.HasSuffix(out, "x\n[records 1 to 1 of 2: continue with from 2]") || strings.Contains(out, "[cut") {
		t.Errorf("exactly full: %q", out[len(out)-80:])
	}

	// A conversation that cannot be read fails.
	e := at(t)
	os.MkdirAll(e.file(owner, "chat-1"), 0o700)
	if _, err := e.Read(owner, "", 0, 0, 0); err == nil {
		t.Error("a conversation that is a directory should fail")
	}
}

// A peer reads the conversation it is in and no other.
func TestReadScope(t *testing.T) {
	a := at(t)
	a.Append(owner, question, nil)
	a.Append(peerA, answer("peer a asks", "x", "y"), nil)
	if out, err := a.Read(peerA, "", 0, 0, 0); err != nil || !strings.Contains(out, "peer a asks") {
		t.Fatalf("own: %q, %v", out, err)
	}
	if out, err := a.Read(peerA, Name(peerA.Session), 0, 0, 0); err != nil || !strings.Contains(out, "peer a asks") {
		t.Fatalf("own, named: %q, %v", out, err)
	}
	for _, session := range []string{"chat-1", "../owner/chat-1"} {
		if _, err := a.Read(peerA, session, 0, 0, 0); !errors.Is(err, ErrScope) {
			t.Errorf("%q: %v", session, err)
		}
	}
	if out, _ := a.Read(peerB, "", 0, 0, 0); out != "nothing of this conversation is archived yet" {
		t.Errorf("another peer, same conversation ID: %q", out)
	}
}

// Sessions lists what is in scope, the most recent first.
func TestSessions(t *testing.T) {
	a := at(t)
	if out, n, err := a.Sessions(owner); err != nil || n != 0 || out != "no conversation is archived yet" {
		t.Fatalf("empty: %q, %v", out, err)
	}
	a.Append(Who{Session: "old"}, []openai.Message{{Role: "assistant", Content: "hello"}, {Role: "user", Content: "first\n  question " + strings.Repeat("ü", 100)}}, nil)
	db, _ := a.database()
	db.Exec("UPDATE history_sessions SET modified=? WHERE id=?", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano(), Owner("old"))
	a.Append(owner, question, nil)
	a.Append(peerA, []openai.Message{{Role: "user", Content: "peer"}}, nil)

	out, n, err := a.Sessions(owner)
	if err != nil || n != 2 || strings.Contains(out, "peer") {
		t.Fatalf("owner: %q, %d, %v", out, n, err)
	}
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "chat-1 (this conversation) · started 2026-10-06 ") || !strings.HasSuffix(lines[0], ` · "What did we decide about the Roof?"`) {
		t.Errorf("first: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "old · started ") || !strings.Contains(lines[1], `"first question ü`) || !strings.HasSuffix(lines[1], `…"`) || lines[2] != "[2 sessions]" {
		t.Errorf("then: %q", lines[1:])
	}
	out, n, _ = a.Sessions(peerA)
	if n != 1 || !strings.Contains(out, "(this conversation)") || !strings.HasSuffix(out, "\"peer\"\n[1 session]") {
		t.Errorf("peer: %q", out)
	}
	if out, n, _ := a.Sessions(peerB); n != 0 || out != "no conversation is archived yet" {
		t.Errorf("another peer: %q", out)
	}

	// A title of maxTitle bytes is whole.
	whole := at(t)
	whole.Append(owner, []openai.Message{{Role: "user", Content: strings.Repeat("t", maxTitle)}}, nil)
	if out, _, _ := whole.Sessions(owner); !strings.Contains(out, `"`+strings.Repeat("t", maxTitle)+`"`) {
		t.Errorf("title of %d bytes: %q", maxTitle, out)
	}

	// Beyond MaxSessions the older ones are counted.
	b := at(t)
	for i := range MaxSessions + 2 {
		b.Append(Who{Session: fmt.Sprint("s", i)}, question, nil)
	}
	os.WriteFile(filepath.Join(b.dir, ownerDir, "not a session.jsonl"), nil, 0o600)
	os.WriteFile(filepath.Join(b.dir, ownerDir, "notes.txt"), nil, 0o600)
	os.Mkdir(filepath.Join(b.dir, ownerDir, "dir.jsonl"), 0o700)
	out, n, _ = b.Sessions(owner)
	if n != MaxSessions || !strings.HasSuffix(out, "[2 sessions more, older, not shown]") {
		t.Errorf("more: %d, %q", n, out[len(out)-60:])
	}

	// What cannot be read fails.
	c := at(t)
	os.MkdirAll(c.dir, 0o700)
	os.WriteFile(filepath.Join(c.dir, ownerDir), nil, 0o600)
	if _, _, err := c.Sessions(owner); err == nil {
		t.Error("an owner directory that is a file should fail")
	}
	os.WriteFile(c.partyDir(peerA), nil, 0o600)
	if _, _, err := c.Sessions(peerA); err == nil {
		t.Error("a peer directory that is a file should fail")
	}
}

// The owner lists every conversation of the archive and deletes one.
func TestListAndDelete(t *testing.T) {
	a := at(t)
	if entries, err := a.List(); err != nil || entries != nil {
		t.Fatalf("empty: %v, %v", entries, err)
	}
	a.Append(owner, question, nil)
	a.Append(peerA, []openai.Message{{Role: "user", Content: "from a"}}, nil)
	db, _ := a.database()
	db.Exec("UPDATE history_sessions SET modified=? WHERE id=?", time.Now().Add(time.Hour).UnixNano(), Party(peerA.Peer)+"/"+Name(peerA.Session))
	os.WriteFile(filepath.Join(a.dir, "README"), nil, 0o600)
	os.Mkdir(filepath.Join(a.dir, "other"), 0o700)
	os.WriteFile(filepath.Join(a.dir, "other", "x.jsonl"), []byte(`{"seq":1}`+"\n"), 0o600)
	entries, err := a.List()
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries %+v, %v", entries, err)
	}
	if !party.MatchString(strings.Split(entries[0].ID, "/")[0]) || entries[0].Title != "from a" || entries[1].ID != "owner/chat-1" || entries[1].Size == 0 {
		t.Errorf("entries %+v", entries)
	}

	if err := a.Delete(entries[0].ID); err != nil {
		t.Fatal(err)
	}
	if out, _ := a.Read(peerA, "", 0, 0, 0); out != "nothing of this conversation is archived yet" {
		t.Errorf("deleted: %q", out)
	}
	// Archived again, it starts anew.
	a.Append(peerA, question, nil)
	if got := all(t, a, peerA); got[0].Seq != 1 || got[0].Answer != 1 {
		t.Errorf("after delete: %+v", got)
	}
	if err := a.Delete("owner/chat-2"); !errors.Is(err, ErrNotArchived) || !strings.Contains(err.Error(), "owner/chat-2") {
		t.Errorf("missing: %v", err)
	}
	for _, id := range []string{"chat-1", "owner/../x", "../owner/chat-1", "peer-xyz/conv", "owner/a/b", "other/chat-1"} {
		if err := a.Delete(id); err == nil || !strings.Contains(err.Error(), "names no session") {
			t.Errorf("%q: %v", id, err)
		}
	}
	if _, err := a.Records(Owner(owner.Session)); err != nil {
		t.Error("a session that was not named is gone")
	}

	// The records of a conversation, as List names it.
	if got, err := a.Records(Owner(owner.Session)); err != nil || len(got) != 2 || got[0].Content != question[0].Content || got[1].Role != "end" {
		t.Errorf("records %+v, %v", got, err)
	}
	if _, err := a.Records("owner/none"); !errors.Is(err, ErrNotArchived) {
		t.Errorf("none: %v", err)
	}
	if _, err := a.Records("../x"); err == nil || errors.Is(err, ErrNotArchived) {
		t.Errorf("no ID: %v", err)
	}
	if Party(peerA.Peer) != strings.Split(entries[0].ID, "/")[0] || Owner("../x") != "owner/"+Name("../x") {
		t.Errorf("party %q, owner %q", Party(peerA.Peer), Owner("../x"))
	}

	b := at(t)
	os.WriteFile(b.dir, nil, 0o600)
	if _, err := b.List(); err == nil {
		t.Error("an archive that is a file should fail")
	}
	c := at(t)
	os.MkdirAll(filepath.Join(c.dir, ownerDir), 0o700)
	os.Chmod(filepath.Join(c.dir, ownerDir), 0)
	t.Cleanup(func() { os.Chmod(filepath.Join(c.dir, ownerDir), 0o700) })
	if os.Getuid() != 0 {
		if _, err := c.List(); err == nil {
			t.Error("an owner directory that cannot be read should fail")
		}
	}
}

// A conversation that was given a title is named by it, in place of its first
// message: on one line, cut to MaxTitle. The title goes with it.
func TestTitles(t *testing.T) {
	a := at(t)
	if title, err := a.Title(Owner(owner.Session)); err != nil || title != "" {
		t.Fatalf("none: %q, %v", title, err)
	}
	// A conversation is given its title before it is archived, too.
	if err := a.SetTitle(Owner(owner.Session), "  Roof\n repairs  "); err != nil {
		t.Fatal(err)
	}
	a.Append(owner, question, nil)
	if title, err := a.Title(Owner(owner.Session)); err != nil || title != "Roof repairs" {
		t.Fatalf("title %q, %v", title, err)
	}
	if out, _, _ := a.Sessions(owner); !strings.HasPrefix(out, "chat-1 (this conversation) · started 2026-10-06 ") || !strings.Contains(out, ` · "Roof repairs"`) {
		t.Errorf("sessions %q", out)
	}
	if entries, _ := a.List(); len(entries) != 1 || entries[0].Title != "Roof repairs" {
		t.Errorf("entries %+v", entries)
	}
	long := strings.Repeat("t", MaxTitle-1) + "ü"
	a.SetTitle(Owner(owner.Session), long)
	if title, _ := a.Title(Owner(owner.Session)); title != long[:MaxTitle-1] {
		t.Errorf("long title %q", title)
	}
	whole := strings.Repeat("t", MaxTitle)
	a.SetTitle(Owner(owner.Session), whole)
	if title, _ := a.Title(Owner(owner.Session)); title != whole {
		t.Errorf("title of %d bytes %q", MaxTitle, title)
	}
	if err := a.Delete(Owner(owner.Session)); err != nil {
		t.Fatal(err)
	}
	if title, err := a.Title(Owner(owner.Session)); err != nil || title != "" {
		t.Errorf("deleted: %q, %v", title, err)
	}

	// What is no title, or names no conversation, fails, as does what
	// cannot be written or read.
	if err := a.SetTitle(Owner(owner.Session), " \n "); err == nil {
		t.Error("an empty title should fail")
	}
	for _, id := range []string{"chat-1", "owner/../x"} {
		if err := a.SetTitle(id, "x"); err == nil || !strings.Contains(err.Error(), "names no session") {
			t.Errorf("set %q: %v", id, err)
		}
		if _, err := a.Title(id); err == nil || !strings.Contains(err.Error(), "names no session") {
			t.Errorf("get %q: %v", id, err)
		}
	}
	b := at(t)
	os.MkdirAll(filepath.Dir(b.dir), 0o700)
	os.WriteFile(b.dir, nil, 0o600)
	if err := b.SetTitle(Owner(owner.Session), "x"); err == nil {
		t.Error("an archive that is a file should fail")
	}
}

// A conversation is given an effort of its own, "" too, and it goes with it.
func TestEfforts(t *testing.T) {
	a := at(t)
	id := Owner(owner.Session)
	if effort, ok, err := a.Effort(id); err != nil || ok || effort != "" {
		t.Fatalf("none: %q, %v, %v", effort, ok, err)
	}
	for _, want := range []string{"high", ""} {
		if err := a.SetEffort(id, want); err != nil {
			t.Fatal(err)
		}
		if effort, ok, err := a.Effort(id); err != nil || !ok || effort != want {
			t.Fatalf("%q: %q, %v, %v", want, effort, ok, err)
		}
	}
	a.Append(owner, question, nil)
	a.SetTitle(id, "Roof")
	if err := a.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.Effort(id); ok {
		t.Error("the effort stayed after the conversation was deleted")
	}
	if title, _ := a.Title(id); title != "" {
		t.Errorf("the title stayed: %q", title)
	}

	// What names no conversation, or cannot be written or read, fails.
	if err := a.SetEffort("chat-1", "low"); err == nil || !strings.Contains(err.Error(), "names no session") {
		t.Errorf("set: %v", err)
	}
	if _, _, err := a.Effort("chat-1"); err == nil || !strings.Contains(err.Error(), "names no session") {
		t.Errorf("get: %v", err)
	}
	b := at(t)
	os.MkdirAll(filepath.Join(b.dir, ownerDir, "chat-1.effort", "x"), 0o700)
	if err := b.SetEffort(id, "low"); err == nil {
		t.Error("an effort that is a directory should fail")
	}
	if _, ok, err := b.Effort(id); err == nil || ok {
		t.Errorf("an effort that cannot be read: %v, %v", ok, err)
	}
	if err := b.Delete(id); err == nil {
		t.Error("an effort that cannot be removed should fail")
	}
}
