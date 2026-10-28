package claudesessions_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/claudesessions"
)

// session1 is a transcript with what was said, a tool that failed and a file
// that was changed.
var session1 = []string{
	`{"type":"mode","mode":"normal","sessionId":"11111111-aaaa-4e5a-8b1c-000000000001"}`,
	`{"type":"user","sessionId":"11111111-aaaa-4e5a-8b1c-000000000001","cwd":"/home/user/alpha","timestamp":"2026-03-01T10:00:05Z","message":{"role":"user","content":"Please deploy the app"}}`,
	`{"type":"assistant","sessionId":"11111111-aaaa-4e5a-8b1c-000000000001","cwd":"/home/user/alpha","timestamp":"2026-03-01T10:00:09Z","message":{"role":"assistant","content":[{"type":"text","text":"I look at the config first."},{"type":"tool_use","id":"toolu_bash","name":"Bash","input":{"command":"make deploy"}}]}}`,
	`{"type":"user","sessionId":"11111111-aaaa-4e5a-8b1c-000000000001","cwd":"/home/user/alpha","timestamp":"2026-03-01T10:00:12Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_bash","is_error":true,"content":[{"type":"text","text":"make: *** No rule to make target 'deploy'"}]}]}}`,
	`{"type":"assistant","sessionId":"11111111-aaaa-4e5a-8b1c-000000000001","cwd":"/home/user/alpha","timestamp":"2026-03-01T10:00:20Z","message":{"role":"assistant","content":[{"type":"text","text":"The deploy failed, there is no such target."},{"type":"tool_use","id":"toolu_edit","name":"Edit","input":{"file_path":"/home/user/alpha/main.go","old_string":"a","new_string":"b"}}]}}`,
	`{"type":"user","sessionId":"11111111-aaaa-4e5a-8b1c-000000000001","cwd":"/home/user/alpha","timestamp":"2026-03-01T10:00:24Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_edit","content":"The file /home/user/alpha/main.go has been updated."}]}}`,
}

var session2 = []string{
	`{"type":"user","sessionId":"22222222-bbbb-4e5a-8b1c-000000000002","cwd":"/home/user/alpha","timestamp":"2026-03-02T08:00:00Z","message":{"role":"user","content":"How do I deploy otherwise?"}}`,
}

var session3 = []string{
	`{"type":"user","sessionId":"33333333-cccc-4e5a-8b1c-000000000003","cwd":"/home/user/beta","timestamp":"2026-03-03T09:00:00Z","message":{"role":"user","content":"Where are the tests?"}}`,
}

func write(t *testing.T, dir, id string, lines []string, written time.Time) {
	t.Helper()
	path := filepath.Join(dir, "proj", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, written, written); err != nil {
		t.Fatal(err)
	}
}

// fixture is a directory of transcripts as Claude Code keeps them, one per
// project directory, the third and newest in another project.
func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "-home-user-alpha"), "11111111-aaaa-4e5a-8b1c-000000000001", session1, time.Date(2026, 3, 1, 10, 1, 0, 0, time.UTC))
	write(t, filepath.Join(dir, "-home-user-alpha"), "22222222-bbbb-4e5a-8b1c-000000000002", session2, time.Date(2026, 3, 2, 8, 1, 0, 0, time.UTC))
	write(t, filepath.Join(dir, "-home-user-beta"), "33333333-cccc-4e5a-8b1c-000000000003", session3, time.Date(2026, 3, 3, 9, 1, 0, 0, time.UTC))
	return dir
}

func TestSessions(t *testing.T) {
	a := claudesessions.Open(fixture(t))
	out, n, err := a.Sessions(claudesessions.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("listed %d sessions:\n%s", n, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !strings.HasPrefix(lines[0], "33333333") || !strings.HasPrefix(lines[1], "22222222") {
		t.Errorf("the newest first:\n%s", out)
	}
	if !strings.Contains(out, "/home/user/alpha") || !strings.Contains(out, "Please deploy the app") {
		t.Errorf("a session shows where it ran and what was asked:\n%s", out)
	}

	out, n, err = a.Sessions(claudesessions.Filter{Project: "beta"})
	if err != nil || n != 1 || !strings.Contains(out, "Where are the tests?") {
		t.Fatalf("by project: %d, %v:\n%s", n, err, out)
	}
}

func TestSessionsWithoutTranscripts(t *testing.T) {
	out, n, err := claudesessions.Open(filepath.Join(t.TempDir(), "nothing")).Sessions(claudesessions.Filter{})
	if err != nil || n != 0 || !strings.Contains(out, "no Claude Code session") {
		t.Fatalf("%d, %v: %q", n, err, out)
	}
}

func TestSearch(t *testing.T) {
	a := claudesessions.Open(fixture(t))
	out, hits, err := a.Search(claudesessions.Query{Text: "DEPLOY"})
	if err != nil {
		t.Fatal(err)
	}
	if hits < 3 {
		t.Fatalf("ignoring case found %d hits:\n%s", hits, out)
	}
	if !strings.Contains(out, "11111111") || !strings.Contains(out, "22222222") {
		t.Errorf("hits name their session:\n%s", out)
	}
	if !strings.Contains(out, "make deploy") {
		t.Errorf("a hit in the arguments of a call:\n%s", out)
	}

	if _, hits, err = a.Search(claudesessions.Query{Text: "deploy", Project: "beta"}); err != nil || hits != 0 {
		t.Errorf("another project: %d, %v", hits, err)
	}

	if out, hits, err = a.Search(claudesessions.Query{Text: "deploy", Tool: "Bash"}); err != nil || hits != 2 {
		t.Errorf("the calls of one tool: %d, %v:\n%s", hits, err, out)
	}

	if out, hits, err = a.Search(claudesessions.Query{Text: "deploy", Role: "assistant"}); err != nil || hits == 0 || strings.Contains(out, " · user · ") {
		t.Errorf("what the assistant said: %d, %v:\n%s", hits, err, out)
	}

	if out, hits, err = a.Search(claudesessions.Query{Text: "suchtarget"}); err != nil || hits != 0 || !strings.Contains(out, "no record") {
		t.Errorf("nothing found: %d, %v: %q", hits, err, out)
	}
}

func TestSearchNeedsAText(t *testing.T) {
	if _, _, err := claudesessions.Open(fixture(t)).Search(claudesessions.Query{Text: "  "}); err == nil {
		t.Fatal("a search without a text")
	}
}

func TestRead(t *testing.T) {
	a := claudesessions.Open(fixture(t))
	out, err := a.Read(claudesessions.Read{Session: "11111111", From: 2, Count: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Please deploy the app") || !strings.Contains(out, "I look at the config first.") {
		t.Fatalf("the first two records:\n%s", out)
	}
	if !strings.Contains(out, "continue with from 4") {
		t.Errorf("a note says where to continue:\n%s", out)
	}

	out, err = a.Read(claudesessions.Read{Session: "11111111", From: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "has been updated") {
		t.Fatalf("the last record:\n%s", out)
	}

	out, err = a.Read(claudesessions.Read{Session: "11111111", Only: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Please deploy the app") || !strings.Contains(out, "· Bash") || !strings.Contains(out, "· Edit") {
		t.Fatalf("only the calls:\n%s", out)
	}

	if _, err = a.Read(claudesessions.Read{Session: "11111111", Only: "everything"}); err == nil {
		t.Fatal("only takes the kinds it names")
	}
}

func TestToolCalls(t *testing.T) {
	a := claudesessions.Open(fixture(t))
	out, n, err := a.ToolCalls(claudesessions.Calls{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("listed %d calls:\n%s", n, out)
	}
	if !strings.Contains(out, "Bash failed · make deploy") {
		t.Errorf("a call that failed says so:\n%s", out)
	}
	if !strings.Contains(out, "Edit · /home/user/alpha/main.go") {
		t.Errorf("a call names the file it touched:\n%s", out)
	}

	out, n, err = a.ToolCalls(claudesessions.Calls{Path: "main.go"})
	if err != nil || n != 1 || !strings.Contains(out, "Edit") {
		t.Fatalf("the calls on a file: %d, %v:\n%s", n, err, out)
	}

	if out, n, err = a.ToolCalls(claudesessions.Calls{Tool: "Bash"}); err != nil || n != 1 {
		t.Fatalf("the calls of one tool: %d, %v:\n%s", n, err, out)
	}
}

func TestResolve(t *testing.T) {
	a := claudesessions.Open(fixture(t))
	if out, err := a.Read(claudesessions.Read{Session: "22222222-bbb"}); err != nil || !strings.Contains(out, "How do I deploy") {
		t.Fatalf("%v:\n%s", err, out)
	}
	if out, err := a.Read(claudesessions.Read{Session: "1"}); err != nil || !strings.Contains(out, "Please deploy") {
		t.Fatalf("a prefix of one session: %v:\n%s", err, out)
	}
	if _, err := a.Read(claudesessions.Read{Session: "9"}); err == nil {
		t.Fatal("a prefix that names no session")
	}
	if _, err := a.Read(claudesessions.Read{Session: "zzz"}); err == nil {
		t.Fatal("there is no such session")
	}
}
