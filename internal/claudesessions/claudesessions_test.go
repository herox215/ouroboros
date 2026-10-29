package claudesessions_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

	// The text is only in what Bash returned, not in the call.
	if out, hits, err = a.Search(claudesessions.Query{Text: "No rule to make target", Tool: "Bash"}); err != nil || hits != 1 || !strings.Contains(out, "user · Bash result · ") {
		t.Errorf("what one tool returned: %d, %v:\n%s", hits, err, out)
	}

	// A hit in what a tool returned names the tool even without the filter.
	if out, hits, err = a.Search(claudesessions.Query{Text: "No rule to make target"}); err != nil || hits != 1 || !strings.Contains(out, "user · Bash result · ") {
		t.Errorf("what a tool returned without a filter: %d, %v:\n%s", hits, err, out)
	}

	if out, hits, err = a.Search(claudesessions.Query{Text: "deploy", Role: "assistant"}); err != nil || hits == 0 || strings.Contains(out, " · user · ") {
		t.Errorf("what the assistant said: %d, %v:\n%s", hits, err, out)
	}

	if out, hits, err = a.Search(claudesessions.Query{Text: "suchtarget"}); err != nil || hits != 0 || !strings.Contains(out, "no record") {
		t.Errorf("nothing found: %d, %v: %q", hits, err, out)
	}
}

// A directory that cannot be read hides its own sessions, not all of them.
func TestSessionsSkipAnUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	dir := fixture(t)
	locked := filepath.Join(dir, "-home-user-beta")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	out, n, err := claudesessions.Open(dir).Sessions(claudesessions.Filter{})
	if err != nil || n != 2 || strings.Contains(out, "33333333") {
		t.Fatalf("%d, %v:\n%s", n, err, out)
	}
}

// A session whose first question lies past what is read of its start ends
// its line with where it ran, not with a separator.
func TestSessionsWithoutAFirstQuestion(t *testing.T) {
	dir := t.TempDir()
	var lines []string
	for range 50 {
		lines = append(lines, `{"type":"mode","mode":"normal","sessionId":"44444444-dddd-4e5a-8b1c-000000000004","cwd":"/home/user/gamma","timestamp":"2026-03-04T09:00:00Z"}`)
	}
	lines = append(lines, `{"type":"user","sessionId":"44444444-dddd-4e5a-8b1c-000000000004","cwd":"/home/user/gamma","timestamp":"2026-03-04T09:00:01Z","message":{"role":"user","content":"Late question"}}`)
	write(t, filepath.Join(dir, "-home-user-gamma"), "44444444-dddd-4e5a-8b1c-000000000004", lines, time.Date(2026, 3, 4, 9, 1, 0, 0, time.UTC))
	out, n, err := claudesessions.Open(dir).Sessions(claudesessions.Filter{})
	if err != nil || n != 1 {
		t.Fatalf("%d, %v:\n%s", n, err, out)
	}
	first := strings.Split(out, "\n")[0]
	if !strings.HasSuffix(first, "/home/user/gamma") {
		t.Errorf("the line ends with where the session ran: %q", first)
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

// A read can be narrowed to a time and says where to go on when more of it
// follows.
func TestReadWithinATime(t *testing.T) {
	a := claudesessions.Open(fixture(t))
	out, err := a.Read(claudesessions.Read{Session: "11111111",
		Since: time.Date(2026, 3, 1, 10, 0, 12, 0, time.UTC),
		Until: time.Date(2026, 3, 1, 10, 0, 12, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "make: *** No rule to make target") || strings.Contains(out, "Please deploy the app") {
		t.Fatalf("only what was in the range:\n%s", out)
	}

	out, err = a.Read(claudesessions.Read{Session: "11111111", Count: 1,
		Since: time.Date(2026, 3, 1, 10, 0, 12, 0, time.UTC),
		Until: time.Date(2026, 3, 1, 10, 0, 24, 0, time.UTC)})
	if err != nil || !strings.Contains(out, "continue with from 5") {
		t.Errorf("more of the range follows: %v:\n%s", err, out)
	}

	if out, err = a.Read(claudesessions.Read{Session: "11111111",
		Since: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)}); err != nil || !strings.Contains(out, "no record of the session is in the range") {
		t.Errorf("a range the session does not reach: %v:\n%s", err, out)
	}

	if out, err = a.Read(claudesessions.Read{Session: "11111111", From: -1,
		Since: time.Date(2026, 3, 1, 10, 0, 20, 0, time.UTC)}); err != nil || !strings.Contains(out, "has been updated") {
		t.Errorf("the last record of the range: %v:\n%s", err, out)
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
	if !strings.Contains(out, "Bash failed · make deploy → make: *** No rule to make target 'deploy'") {
		t.Errorf("a call that failed says so, with what it returned:\n%s", out)
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

// The records after those a read returns are counted all the same.
func TestReadCountsTheRest(t *testing.T) {
	out, err := claudesessions.Open(fixture(t)).Read(claudesessions.Read{Session: "11111111", From: 2, Count: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[records 2 to 2 of 6: continue with from 3]") {
		t.Errorf("the note counts every record:\n%s", out)
	}
}

// A call without a time is as old as its session: it sorts before the calls
// of older sessions and is not cut by the limit behind them.
func TestToolCallsWithoutATime(t *testing.T) {
	dir := fixture(t)
	write(t, filepath.Join(dir, "-home-user-gamma"), "55555555-eeee-4e5a-8b1c-000000000005", []string{
		`{"type":"assistant","sessionId":"55555555-eeee-4e5a-8b1c-000000000005","cwd":"/home/user/gamma","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_ls","name":"Bash","input":{"command":"ls -la"}}]}}`,
	}, time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC))
	out, n, err := claudesessions.Open(dir).ToolCalls(claudesessions.Calls{Limit: 1})
	if err != nil || n != 1 {
		t.Fatalf("%d, %v:\n%s", n, err, out)
	}
	if !strings.Contains(out, "ls -la") || strings.Contains(out, "no time") {
		t.Errorf("the newest session's call comes first, at the time of its session:\n%s", out)
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

// A session is named so that its name tells it from the others, and the
// transcript Claude Code writes for a subagent says so.
func TestSessionsNameEachOneDistinctly(t *testing.T) {
	dir := t.TempDir()
	written := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	write(t, filepath.Join(dir, "-home-user-alpha"), "agent-aaaa-1111", session1, written)
	write(t, filepath.Join(dir, "-home-user-beta"), "agent-aaaa-2222", session3, written)
	out, n, err := claudesessions.Open(dir).Sessions(claudesessions.Filter{})
	if err != nil || n != 2 {
		t.Fatalf("%d, %v:\n%s", n, err, out)
	}
	if !strings.Contains(out, "(subagent)") {
		t.Errorf("a subagent's transcript says so:\n%s", out)
	}
	names := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name := strings.SplitN(line, " ", 2)[0]
		if names[name] {
			t.Errorf("two sessions are named %q:\n%s", name, out)
		}
		names[name] = true
	}
}

// A listing keeps to the times it is given, and says when it shows fewer
// sessions than there are.
func TestSessionsWithinTimesAndALimit(t *testing.T) {
	a := claudesessions.Open(fixture(t))
	out, n, err := a.Sessions(claudesessions.Filter{Since: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)})
	if err != nil || n != 2 || strings.Contains(out, "11111111") {
		t.Errorf("since: %d, %v:\n%s", n, err, out)
	}
	out, n, err = a.Sessions(claudesessions.Filter{Until: time.Date(2026, 3, 2, 23, 0, 0, 0, time.UTC)})
	if err != nil || n != 2 || strings.Contains(out, "33333333") {
		t.Errorf("until: %d, %v:\n%s", n, err, out)
	}
	out, n, err = a.Sessions(claudesessions.Filter{Limit: 1})
	if err != nil || n != 1 || !strings.Contains(out, "[more sessions are not shown") {
		t.Errorf("a limit: %d, %v:\n%s", n, err, out)
	}
}

// A session that ran into the next day still ran on the day before.
func TestSessionsAcrossADay(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "aaaaaaaa", []string{
		`{"type":"user","sessionId":"aaaaaaaa","cwd":"/w","timestamp":"2026-03-01T22:00:00Z","message":{"content":"Start"}}`,
		`{"type":"user","sessionId":"aaaaaaaa","cwd":"/w","timestamp":"2026-03-03T02:00:00Z","message":{"content":"End"}}`,
	}, time.Date(2026, 3, 3, 2, 0, 0, 0, time.UTC))
	out, n, err := claudesessions.Open(dir).Sessions(claudesessions.Filter{
		Since: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 3, 2, 23, 0, 0, 0, time.UTC)})
	if err != nil || n != 1 || !strings.Contains(out, "aaaaaaaa") {
		t.Fatalf("a session over the day: %d, %v:\n%s", n, err, out)
	}
}

// A directory of transcripts that cannot be read is an error for every tool,
// not an empty archive.
func TestUnreadableArchive(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	dir := fixture(t)
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	a := claudesessions.Open(dir)
	if _, _, err := a.Sessions(claudesessions.Filter{}); err == nil {
		t.Error("sessions")
	}
	if _, _, err := a.Search(claudesessions.Query{Text: "deploy"}); err == nil {
		t.Error("search")
	}
	if _, _, err := a.ToolCalls(claudesessions.Calls{}); err == nil {
		t.Error("tool calls")
	}
	if _, err := a.Read(claudesessions.Read{Session: "11111111"}); err == nil {
		t.Error("read")
	}
}

// A transcript that cannot be read is left out of a listing, a search and a
// listing of calls, and a read of it fails.
func TestUnreadableTranscript(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	dir := fixture(t)
	locked := filepath.Join(dir, "-home-user-alpha", "proj", "11111111-aaaa-4e5a-8b1c-000000000001.jsonl")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	a := claudesessions.Open(dir)
	if out, n, err := a.Sessions(claudesessions.Filter{}); err != nil || n != 2 || strings.Contains(out, "11111111") {
		t.Errorf("sessions: %d, %v:\n%s", n, err, out)
	}
	if out, _, err := a.Search(claudesessions.Query{Text: "e"}); err != nil || strings.Contains(out, "11111111") || !strings.Contains(out, "in 2 sessions") {
		t.Errorf("search: %v:\n%s", err, out)
	}
	if out, _, err := a.ToolCalls(claudesessions.Calls{}); err != nil || strings.Contains(out, "11111111") {
		t.Errorf("tool calls: %v:\n%s", err, out)
	}
	for _, from := range []int{1, -1} {
		if _, err := a.Read(claudesessions.Read{Session: "11111111", From: from}); err == nil {
			t.Errorf("read from %d", from)
		}
	}
}

// A directory whose names can be listed but whose files cannot be looked at
// hides its transcripts.
func TestSessionsOfADirectoryThatCannotBeSearched(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	dir := fixture(t)
	locked := filepath.Join(dir, "-home-user-beta", "proj")
	if err := os.Chmod(locked, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	out, n, err := claudesessions.Open(dir).Sessions(claudesessions.Filter{})
	if err != nil || n != 2 || strings.Contains(out, "33333333") {
		t.Fatalf("%d, %v:\n%s", n, err, out)
	}
}

// A transcript that is a directory under the name of one is listed, as
// nothing can be read of it, and a search leaves it out.
func TestTranscriptThatIsADirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.MkdirAll(filepath.Join(dir, "-home-user-alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "-home-user-alpha", "66666666.jsonl")); err != nil {
		t.Fatal(err)
	}
	a := claudesessions.Open(dir)
	if out, n, err := a.Sessions(claudesessions.Filter{}); err != nil || n != 1 || !strings.Contains(out, "66666666") {
		t.Fatalf("sessions: %d, %v:\n%s", n, err, out)
	}
	if out, n, err := a.Search(claudesessions.Query{Text: "deploy"}); err != nil || n != 0 || !strings.Contains(out, "of 0 sessions") {
		t.Fatalf("a search of a directory: %d, %v:\n%s", n, err, out)
	}
}

// odd is a transcript directly below the directory of the projects, with a
// short ID and without times or a working directory, and with lines that are
// no records or records of every shape Claude Code may write.
var odd = []string{
	``,
	`not json`,
	`{broken`,
	`{"type":"summary","summary":"not a record"}`,
	`{"type":"user"}`,
	`{"type":"user","message":{"content":{}}}`,
	`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"none","content":"nothing ran"}]}}`,
	`{"type":"user","message":{"content":"   "}}`,
	`{"type":"user","isSidechain":true,"message":{"content":[{"type":"text","text":"  "},{"type":"text","text":"Please say \"hi\" to Bob"}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"I ponder a greeting."},{"type":"text","text":"hi Bob"}]}}`,
	`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Ping"},{"type":"tool_use","id":"t2","name":"Pong","input":{"count":3}},{"type":"tool_use","name":"Odd","input":[1]}]}}`,
	`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1"},{"type":"tool_result","tool_use_id":"t2","content":{"pong":true}},{"type":"tool_result","tool_use_id":"t3","content":[{"type":"text","text":"one"},{"type":"image"},{"type":"text","text":"two"}]}]}}`,
}

func oddArchive(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "abc.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(odd, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	written := time.Date(2026, 3, 6, 9, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, written, written); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A transcript without times or a working directory is listed under the
// directory it lies in, with the first thing that was said as text.
func TestSessionsOfAnOddTranscript(t *testing.T) {
	dir := oddArchive(t)
	out, n, err := claudesessions.Open(dir).Sessions(claudesessions.Filter{})
	if err != nil || n != 1 {
		t.Fatalf("%d, %v:\n%s", n, err, out)
	}
	if !strings.HasPrefix(out, "abc · until ") || !strings.Contains(out, "· "+filepath.Base(dir)+" · ") ||
		!strings.Contains(out, `Please say "hi" to Bob`) {
		t.Errorf("an odd transcript:\n%s", out)
	}
}

// What cannot be a record is skipped; every record is read with its kind.
func TestReadAnOddTranscript(t *testing.T) {
	a := claudesessions.Open(oddArchive(t))
	out, err := a.Read(claudesessions.Read{Session: "abc", Count: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"#7 · no time · user · \nnothing ran",
		"#9 · no time · user · subagent\nPlease say",
		"#10 · no time · assistant · thinking\nI ponder",
		"#11 · no time · assistant · Ping\n\n",
		"#11 · no time · assistant · Pong\n{\"count\":3}\n",
		"#11 · no time · assistant · Odd\n[1]\n",
		"#12 · no time · user · Ping result\n\n",
		"#12 · no time · user · Pong result\n{\"pong\":true}\n",
		"#12 · no time · user · \none\ntwo\n",
		"[records 7 to 12 of 12]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "#6 ") || strings.Contains(out, "#8 ") {
		t.Errorf("a line without text is no record:\n%s", out)
	}

	for only, want := range map[string]string{
		"user":      "[records 9 to 9 of 12: continue with from 10]",
		"assistant": "[records 10 to 10 of 12: continue with from 11]",
		"result":    "[records 7 to 12 of 12]",
	} {
		out, err := a.Read(claudesessions.Read{Session: "abc", Only: only})
		if err != nil || !strings.Contains(out, want) {
			t.Errorf("only %s: %v:\n%s", only, err, out)
		}
	}
	if out, err := a.Read(claudesessions.Read{Session: "abc", Only: "user", From: 8}); err != nil || strings.Contains(out, "nothing ran") {
		t.Errorf("only what was said: %v:\n%s", err, out)
	}
}

// A search finds a text as it is escaped in a transcript, and keeps to the
// role and the times it is given; a record without a time is inside any.
func TestSearchAnOddTranscript(t *testing.T) {
	a := claudesessions.Open(oddArchive(t))
	out, hits, err := a.Search(claudesessions.Query{Text: `say "hi"`, Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil || hits != 1 || !strings.Contains(out, "abc · no time · #9 user · ") {
		t.Errorf("a quote: %d, %v:\n%s", hits, err, out)
	}
	if out, hits, err = a.Search(claudesessions.Query{Text: "ponder"}); err != nil || hits != 1 || !strings.Contains(out, "#10 assistant · thinking · ") {
		t.Errorf("a thought: %d, %v:\n%s", hits, err, out)
	}
	if _, _, err = a.Search(claudesessions.Query{Text: "hi", Role: "owner"}); err == nil {
		t.Error("a role that is none")
	}
}

// A search stops at its limit, wherever in a line the next hit is, and a
// record outside the times a search is given is no hit.
func TestSearchStopsAtItsLimit(t *testing.T) {
	a := claudesessions.Open(oddArchive(t))
	for _, text := range []string{"Bob", "ponder", "Pong", "true", "one"} {
		// One hit before: a text in a line of its own.
		out, hits, err := a.Search(claudesessions.Query{Text: text, Limit: 1})
		if err != nil || hits != 1 {
			t.Errorf("%s: %d, %v:\n%s", text, hits, err, out)
		}
	}
	out, hits, err := a.Search(claudesessions.Query{Text: "o", Limit: 2})
	if err != nil || hits != 2 || !strings.Contains(out, "[more hits are not shown; searched 1 session") {
		t.Errorf("a limit: %d, %v:\n%s", hits, err, out)
	}

	a = claudesessions.Open(fixture(t))
	out, hits, err = a.Search(claudesessions.Query{Text: "deploy", Since: time.Date(2026, 3, 1, 10, 0, 10, 0, time.UTC)})
	if err != nil || strings.Contains(out, "#2 ") || strings.Contains(out, "#3 ") || !strings.Contains(out, "#5 ") {
		t.Errorf("since: %d, %v:\n%s", hits, err, out)
	}
	// The session went on after until: what it said before is found all the same.
	if out, hits, err = a.Search(claudesessions.Query{Text: "deploy", Until: time.Date(2026, 3, 1, 10, 0, 10, 0, time.UTC)}); err != nil || hits != 2 ||
		strings.Contains(out, "#5 ") || strings.Contains(out, "22222222") {
		t.Errorf("until: %d, %v:\n%s", hits, err, out)
	}
}

// A read refuses an offset outside the first record or inside a character,
// and says when there is nothing from where it is to begin.
func TestReadRefusesWhatItCannotGive(t *testing.T) {
	a := claudesessions.Open(fixture(t))
	if _, err := a.Read(claudesessions.Read{Session: "11111111", Offset: -1}); err == nil {
		t.Error("a negative offset")
	}
	if _, err := a.Read(claudesessions.Read{Session: " "}); err == nil {
		t.Error("no session")
	}
	// The first record of each kind is where the read stops.
	for _, only := range []string{"user", "assistant", "tool", "result"} {
		if _, err := a.Read(claudesessions.Read{Session: "11111111", Only: only, Offset: 1 << 20}); err == nil {
			t.Errorf("only %s: an offset past the first record", only)
		}
	}
	if _, err := claudesessions.Open(oddArchive(t)).Read(claudesessions.Read{Session: "abc", Only: "assistant", Offset: 1 << 20}); err == nil {
		t.Error("an offset past a thought")
	}
	if out, err := a.Read(claudesessions.Read{Session: "11111111", From: 99}); err != nil || out != "the session has 6 records: there is none from 99 on" {
		t.Errorf("past the end: %v: %q", err, out)
	}

	dir := t.TempDir()
	write(t, dir, "77777777", []string{`{"type":"user","message":{"content":"héllo"}}`}, time.Date(2026, 3, 7, 9, 0, 0, 0, time.UTC))
	head := len("#1 · no time · user\n")
	if _, err := claudesessions.Open(dir).Read(claudesessions.Read{Session: "77777777", Offset: head + 2}); err == nil {
		t.Error("an offset inside a character")
	}
	if out, err := claudesessions.Open(dir).Read(claudesessions.Read{Session: "77777777", Offset: head + 1}); err != nil || !strings.HasPrefix(out, "éllo\n") {
		t.Errorf("an offset on a character: %v: %q", err, out)
	}
}

// A record too long for one read comes in pieces, each cut between two
// characters, and the note says where the next piece begins. A record that
// does not fit after others waits for the next read.
func TestReadALongRecord(t *testing.T) {
	dir := t.TempDir()
	written := time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC)
	// Two records whose characters lie differently, so that one of them has
	// a character where the room of a read ends.
	long := strings.Repeat("é", 15000)
	write(t, dir, "88888888", []string{`{"type":"user","message":{"content":"` + long + `"}}`}, written)
	write(t, dir, "99999999", []string{`{"type":"user","message":{"content":"x` + long + `"}}`}, written)
	a := claudesessions.Open(dir)
	for id, body := range map[string]string{"88888888": long, "99999999": "x" + long} {
		out, err := a.Read(claudesessions.Read{Session: id})
		if err != nil {
			t.Fatal(err)
		}
		i := strings.LastIndex(out, "\n[records 1 to 1 of 1: continue with from 1, offset ")
		if i < 0 || !utf8.ValidString(out) || len(out) > claudesessions.MaxReadBytes {
			t.Fatalf("%s: the first piece:\n%s", id, out[max(len(out)-200, 0):])
		}
		var offset int
		if _, err := fmt.Sscanf(out[i:], "\n[records 1 to 1 of 1: continue with from 1, offset %d]", &offset); err != nil {
			t.Fatal(err)
		}
		rest, err := a.Read(claudesessions.Read{Session: id, From: 1, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if got := out[:i] + strings.TrimSuffix(rest, "\n[records 1 to 1 of 1]"); got != "#1 · no time · user\n"+body+"\n" {
			t.Errorf("%s: the pieces do not make the record: %d bytes", id, len(got))
		}
	}

	write(t, dir, "aaaaaaaa", []string{`{"type":"user","message":{"content":"short"}}`, `{"type":"user","message":{"content":"` + long + `"}}`}, written)
	if out, err := a.Read(claudesessions.Read{Session: "aaaaaaaa"}); err != nil || !strings.HasSuffix(out, "short\n\n[records 1 to 1 of 2: continue with from 2]") {
		t.Errorf("a long record after a short one: %v:\n%s", err, out)
	}
}

// The calls of one session, of a time, and a listing cut at its limit.
func TestToolCallsNarrowed(t *testing.T) {
	a := claudesessions.Open(fixture(t))
	if out, n, err := a.ToolCalls(claudesessions.Calls{Session: "11111111", Tool: "Edit"}); err != nil || n != 1 || !strings.Contains(out, "Edit") {
		t.Errorf("one session: %d, %v:\n%s", n, err, out)
	}
	if _, _, err := a.ToolCalls(claudesessions.Calls{Session: "zzz"}); err == nil {
		t.Error("a session that is none")
	}
	if out, n, err := a.ToolCalls(claudesessions.Calls{Since: time.Date(2026, 3, 1, 10, 0, 15, 0, time.UTC)}); err != nil || n != 1 || !strings.Contains(out, "Edit") {
		t.Errorf("since: %d, %v:\n%s", n, err, out)
	}
	// The session went on after until: its calls before it are listed.
	if out, n, err := a.ToolCalls(claudesessions.Calls{Until: time.Date(2026, 3, 1, 10, 0, 15, 0, time.UTC)}); err != nil || n != 1 || !strings.Contains(out, "Bash") {
		t.Errorf("until: %d, %v:\n%s", n, err, out)
	}
	if out, n, err := a.ToolCalls(claudesessions.Calls{Since: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)}); err != nil || n != 0 || out != "no tool call matches" {
		t.Errorf("none: %d, %v: %q", n, err, out)
	}
	if out, n, err := a.ToolCalls(claudesessions.Calls{Limit: 1}); err != nil || n != 1 || !strings.Contains(out, "[more calls are not shown") {
		t.Errorf("a limit: %d, %v:\n%s", n, err, out)
	}
}

// A session is named by its whole ID, and by as much of it as names it.
func TestResolveByTheWholeID(t *testing.T) {
	a := claudesessions.Open(fixture(t))
	if out, err := a.Read(claudesessions.Read{Session: "11111111-aaaa-4e5a-8b1c-000000000001"}); err != nil || !strings.Contains(out, "Please deploy") {
		t.Fatalf("%v:\n%s", err, out)
	}
}

// Sessions whose IDs differ only at their end are named in full, and a part
// that several share names none of them.
func TestSessionsThatDifferAtTheEnd(t *testing.T) {
	dir := t.TempDir()
	written := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	same := strings.Repeat("a", 36)
	write(t, dir, same+"1", session2, written)
	write(t, dir, same+"2", session3, written)
	a := claudesessions.Open(dir)
	out, n, err := a.Sessions(claudesessions.Filter{})
	if err != nil || n != 2 || !strings.Contains(out, same+"1 ") || !strings.Contains(out, same+"2 ") {
		t.Fatalf("%d, %v:\n%s", n, err, out)
	}
	_, err = a.Read(claudesessions.Read{Session: "aaaa"})
	if err == nil || !strings.Contains(err.Error(), `"aaaa" names 2 sessions: `+same+"1, "+same+"2") && !strings.Contains(err.Error(), `"aaaa" names 2 sessions: `+same+"2, "+same+"1") {
		t.Fatalf("a part of two: %v", err)
	}
}

// A transcript whose last time cannot be read ended when it was written, and
// a session that ran over a day says both days.
func TestSessionsEndWhenTheyCan(t *testing.T) {
	dir := t.TempDir()
	written := time.Date(2026, 3, 9, 9, 0, 0, 0, time.UTC)
	start := `{"type":"user","sessionId":"x","cwd":"/w","timestamp":"2026-03-01T10:00:00Z","message":{"content":"Begin"}}`
	write(t, dir, "bbbbbbbb", []string{start, `{"type":"user","timestamp":"","message":{"content":"empty"}}`}, written)
	write(t, dir, "cccccccc", []string{start, `{"type":"user","timestamp":"soon","message":{"content":"garbage"}}`}, written)
	out, n, err := claudesessions.Open(dir).Sessions(claudesessions.Filter{})
	if err != nil || n != 2 {
		t.Fatalf("%d, %v:\n%s", n, err, out)
	}
	ran := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC).Local().Format("2006-01-02 15:04") + " – " + written.Local().Format("2006-01-02 15:04")
	if strings.Count(out, ran) != 2 {
		t.Errorf("both ran until they were written, %s:\n%s", ran, out)
	}
}

// A hit is shown with the text around it, cut between characters, marked
// where the text goes on.
func TestSearchExcerpts(t *testing.T) {
	dir := t.TempDir()
	wide := strings.Repeat("é", 100)
	write(t, dir, "dddddddd", []string{
		`{"type":"user","message":{"content":"` + wide + `xMIDDLEx` + wide + `"}}`,
		`{"type":"user","message":{"content":"` + wide + `xENDING"}}`,
		`{"type":"user","message":{"content":"STARTING ` + wide + `"}}`,
	}, time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC))
	write(t, dir, "eeeeeeee", []string{
		`{"type":"user","message":{"content":"x` + wide + wide + `"}}`,
	}, time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC))
	a := claudesessions.Open(dir)
	for text, want := range map[string]func(string) bool{
		"middle":   func(s string) bool { return strings.HasPrefix(s, "…") && strings.HasSuffix(s, "…") },
		"ending":   func(s string) bool { return strings.HasPrefix(s, "…") && strings.HasSuffix(s, "xENDING") },
		"starting": func(s string) bool { return strings.HasPrefix(s, "STARTING") && strings.HasSuffix(s, "…") },
	} {
		out, hits, err := a.Search(claudesessions.Query{Text: text})
		if err != nil || hits != 1 {
			t.Fatalf("%s: %d, %v:\n%s", text, hits, err, out)
		}
		line := strings.Split(out, "\n")[0]
		found := line[strings.LastIndex(line, " · ")+len(" · "):]
		if !utf8.ValidString(found) || !want(found) {
			t.Errorf("%s: %q", text, found)
		}
	}
	// The first question is cut short in a listing.
	out, _, err := a.Sessions(claudesessions.Filter{})
	if err != nil || !utf8.ValidString(out) || !strings.Contains(out, " · x"+strings.Repeat("é", 79)+"…\n") {
		t.Errorf("a long first question: %v:\n%s", err, out)
	}
}
