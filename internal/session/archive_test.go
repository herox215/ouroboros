package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
	"ouroboros/internal/storage"
)

// logs is what a Service logged.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) log(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logs) has(text string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.ContainsFunc(l.lines, func(line string) bool { return strings.Contains(line, text) })
}

// archived is a Service with a and the archive in dir, closed when the test
// ends. Its clock is at now; a peer is named "beta".
func archived(t *testing.T, a *fakeAgent, dir string, now time.Time) (*Service, *history.Archive, *logs) {
	t.Helper()
	s := service(t, a)
	s.now = func() time.Time { return now }
	arch := history.Open(dir)
	l := &logs{}
	s.Archive(arch, l.log, func(party string) string { return "beta of " + party[:9] })
	return s, arch, l
}

// echo is an LLM that answers with the last message it was sent.
type echo struct{}

func (echo) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	return openai.Message{Role: "assistant", Content: msgs[len(msgs)-1].Content}, nil
}

// call is a call of a tool with id.
func call(id, name, args string) openai.ToolCall {
	return openai.ToolCall{ID: id, Type: "function", Function: openai.FunctionCall{Name: name, Arguments: args}}
}

// A session in the archive goes on: the front ends see what happened in it,
// the agent remembers it as it did, and is told once how long ago its last
// answer was.
func TestArchivedSession(t *testing.T) {
	dir := t.TempDir()
	arch := history.Open(dir)
	who := history.Who{Session: "s1"}
	arch.Append(who, []openai.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "looking", ToolCalls: []openai.ToolCall{call("c1", "read", `{"path":"a"}`), call("c2", "run", `{not json`)}},
		{Role: "tool", ToolCallID: "c1", Content: "error: no such file"},
		{Role: "tool", ToolCallID: "c2", Content: "ok"},
		{Role: "tool", ToolCallID: "unknown", Content: "?"},
		{Role: "assistant", Content: "done"},
	}, nil)
	arch.Append(who, []openai.Message{{Role: "user", Content: resumed(time.Now(), time.Now()) + "again"}, {Role: "assistant", Content: "ok"}}, nil)
	arch.Append(who, []openai.Message{{Role: "user", Content: "fail"}, {Role: "assistant", ToolCalls: []openai.ToolCall{call("c3", "read", `{}`)}}}, errors.New("no LLM"))
	arch.Append(who, []openai.Message{{Role: "user", Content: "stop"}}, context.Canceled)
	arch.Append(history.Who{Session: "p1", Peer: "key"}, []openai.Message{{Role: "user", Content: "from a peer"}}, nil)

	var mu sync.Mutex
	var told, sent []string
	b := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		// What the model is sent of the message, from what the session
		// hands the agent besides the text.
		got, err := agent.New(echo{}, "m", "").Respond(ctx, "s1", text)
		mu.Lock()
		defer mu.Unlock()
		told, sent = append(told, text), append(sent, got)
		return "pong", err
	}}
	later := time.Now().Add(3 * time.Hour)
	s, _, l := archived(t, b, dir, later)
	ctx := context.Background()
	list, _ := s.List(ctx)
	if len(list) != 1 || list[0].ID != "s1" || list[0].Title != "hi" || list[0].Running || list[0].Peer != "" {
		t.Fatalf("list %+v", list)
	}
	if len(b.resumed) != 0 {
		t.Fatalf("a session is loaded once it is needed: %v", b.resumed)
	}

	past, _, err := s.Watch(ctx, "s1")
	if err != nil || kinds(past) != "message say step step step step answer message answer message say step failed message canceled" {
		t.Fatalf("past %v, %v", kinds(past), err)
	}
	if past[0].Text != "hi" || past[1].Text != "looking" || past[2].Tool != "read" || string(past[2].Args) != `{"path":"a"}` || past[2].Done ||
		string(past[3].Args) != `"{not json"` || !past[4].Done || past[4].Err != "no such file" || !past[5].Done || past[5].Err != "" ||
		past[6].Text != "done" || past[7].Text != "again" || past[12].Text != "the answer failed" {
		t.Fatalf("past %+v", past)
	}
	var memory []openai.Message
	json.Unmarshal(b.resumed["s1"], &memory)
	var roles []string
	for _, m := range memory {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, " ") != "user assistant tool tool tool assistant user assistant user assistant tool assistant user assistant" ||
		memory[10].Content != "[This call did not finish.]" || memory[11].Content != memory[13].Content || !strings.HasPrefix(memory[13].Content, "[This answer broke off") ||
		memory[1].ToolCalls[0].Function.Name != "read" {
		t.Fatalf("memory %+v", memory)
	}

	asked(t, s, "s1", "and now?")
	asked(t, s, "s1", "and then?")
	if len(told) != 2 || !strings.HasPrefix(told[0], "[A note of ouro, which the owner did not write: ouro started anew since the last answer of this conversation, 3 hours ago.") ||
		!strings.HasSuffix(told[0], "]\n\nand now?") || told[1] != "and then?" {
		t.Fatalf("the agent was told %q", told)
	}
	if len(sent) != 2 || !strings.HasSuffix(sent[0], told[0]) || strings.Contains(sent[1], "A note of ouro") {
		t.Fatalf("the model was sent %q", sent)
	}
	past, _, _ = s.Watch(ctx, "s1")
	if past[len(past)-4].Text != "and now?" {
		t.Fatalf("the owner is shown %+v", past[len(past)-4])
	}

	// The context of an archived session is that of its agent once it is
	// loaded.
	arch.Append(history.Who{Session: "s2"}, []openai.Message{{Role: "user", Content: "other"}}, nil)
	s.Contents(ctx, "s2")
	if _, ok := b.resumed["s2"]; !ok {
		t.Fatal("the context loads the session")
	}
	// A session that was read, or that is not archived, is nothing to log.
	s.Watch(ctx, "s3")
	if l.has("cannot be read") {
		t.Fatalf("log %q", l.lines)
	}
}

// asked asks text in the session id of s and waits for the end of its answer.
func asked(t *testing.T, s *Service, id, text string) []Event {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	_, live, err := s.Watch(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ask(ctx, id, text); err != nil {
		t.Fatal(err)
	}
	return until(t, live, Answer, Failed, Canceled)
}

// What cannot be read from the archive is logged, and the session goes on
// without it.
func TestArchiveFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	os.WriteFile(dir, nil, 0o600) // no directory
	s, _, l := archived(t, &fakeAgent{}, dir, time.Now())
	ctx := context.Background()
	if past, _, err := s.Watch(ctx, "s1"); err != nil || len(past) != 0 {
		t.Fatalf("past %+v, %v", past, err)
	}
	s.List(ctx)
	if list, err := s.Peers(ctx); err == nil || len(list) != 0 {
		t.Fatalf("peers %+v, %v", list, err)
	}
	for _, want := range []string{"the session s1 cannot be read from the archive", "the archive cannot be listed"} {
		if !l.has(want) {
			t.Errorf("not logged: %q in %q", want, l.lines)
		}
	}

	// A memory that cannot be resumed leaves what happened.
	dir = t.TempDir()
	history.Open(dir).Append(history.Who{Session: "s2"}, []openai.Message{{Role: "user", Content: "hi"}}, nil)
	s, _, l = archived(t, &fakeAgent{resume: errors.New("no memory")}, dir, time.Now())
	if past, _, _ := s.Watch(ctx, "s2"); kinds(past) != "message" {
		t.Fatalf("past %+v", past)
	}
	if !l.has("what the agent remembers of the session s2 is lost: no memory") {
		t.Errorf("log %q", l.lines)
	}
}

// breaksOnResume is an agent whose resumption of a session breaks the
// archive.
type breaksOnResume struct {
	*fakeAgent
	breaks func()
}

func (a breaksOnResume) Resume(id string, memory json.RawMessage) error {
	a.breaks()
	return a.fakeAgent.Resume(id, memory)
}

// A title that cannot be read once the records were is logged, and the
// session goes on without it.
func TestArchiveFailsAfterTheRecords(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	arch := history.Open(dir)
	if err := arch.Append(history.Who{Session: "s1"}, []openai.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := arch.SetTitle("owner/s1", "Greeting"); err != nil {
		t.Fatal(err)
	}
	s := New(breaksOnResume{&fakeAgent{}, func() {
		arch.Close()
		os.Rename(dir, dir+".moved")
		os.WriteFile(dir, nil, 0o600)
	}})
	t.Cleanup(s.Close)
	l := &logs{}
	s.Archive(arch, l.log, func(string) string { return "" })
	if past, _, err := s.Watch(context.Background(), "s1"); err != nil || kinds(past) != "message" {
		t.Fatalf("past %+v, %v", past, err)
	}
	if !l.has("the title of the session s1 cannot be read") {
		t.Errorf("log %q", l.lines)
	}
}

// A session archived without a message, as only its end is, is resumed with
// nothing to show.
func TestResumeWithoutEvents(t *testing.T) {
	dir := t.TempDir()
	history.Open(dir).Append(history.Who{Session: "s1"}, nil, nil)
	s, _, _ := archived(t, &fakeAgent{}, dir, time.Now())
	if past, _, err := s.Watch(context.Background(), "s1"); err != nil || len(past) != 0 {
		t.Fatalf("past %+v, %v", past, err)
	}
}

// A restart that another build completed is told to the session it belongs
// to when the service is given the archive again, once it holds it: the
// session is loaded already, and finds its completion as it happened.
func TestRestartCompletedWhileLoaded(t *testing.T) {
	dir := t.TempDir()
	if err := history.Open(dir).Append(history.Who{Session: "s1"}, []openai.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	s, arch, l := archived(t, &fakeAgent{}, dir, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, live, err := s.Watch(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := arch.PendingRestart("owner/s1", "previous-image", "restart-id", "/new/ouro"); err != nil {
		t.Fatal(err)
	}
	s.Archive(arch, l.log, func(string) string { return "" })
	got := until(t, live, Restarted)
	if last := got[len(got)-1]; last.Text != "/new/ouro" || last.PresentationID != "restart-id" || last.At.IsZero() {
		t.Fatalf("restarted %+v", last)
	}
	if len(l.lines) != 0 {
		t.Fatalf("log %v", l.lines)
	}
}

// A model that cannot be read once the records were is logged, and the
// session goes on without it.
func TestArchiveFailsToReadTheModel(t *testing.T) {
	dir := t.TempDir()
	arch := history.Open(dir)
	if err := arch.Append(history.Who{Session: "s1"}, []openai.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	// The archive keeps its database open, so the column it reads the model
	// from can be taken from under it: the records are still there.
	db, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE history_sessions RENAME COLUMN model TO gone"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s := service(t, &fakeAgent{})
	l := &logs{}
	s.Archive(arch, l.log, func(string) string { return "" })
	if past, _, err := s.Watch(context.Background(), "s1"); err != nil || kinds(past) != "message" {
		t.Fatalf("past %q, %v", kinds(past), err)
	}
	if !l.has("the model of the session s1 cannot be read") {
		t.Errorf("log %q", l.lines)
	}
}

// A session is deleted from the archive and from what the agent remembers,
// unless an answer is on its way in it.
func TestDelete(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	a := &fakeAgent{run: func(_ context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if text == "wait" {
			<-release
		}
		return "pong", nil
	}}
	s, arch, _ := archived(t, a, dir, time.Now())
	ctx := context.Background()
	asked(t, s, "s1", "hi")
	arch.Append(history.Who{Session: "s1"}, []openai.Message{{Role: "user", Content: "hi"}}, nil) // as the node archives it
	_, watching, _ := s.Watch(ctx, "s1")
	if list, _ := s.List(ctx); len(list) != 1 || list[0].ID != "s1" {
		t.Fatalf("in memory and archived, listed once: %+v", list)
	}

	_, live, _ := s.Watch(ctx, "busy")
	s.Ask(ctx, "busy", "wait")
	if err := s.Delete(ctx, "busy"); !errors.Is(err, ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	close(release)
	until(t, live, Answer)

	if err := s.Delete(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-watching; ok {
		t.Error("who watches it is let go")
	}
	if _, err := arch.Records(history.Owner("s1")); !errors.Is(err, history.ErrNotArchived) {
		t.Errorf("still archived: %v", err)
	}
	if list, _ := s.List(ctx); len(list) != 1 || list[0].ID != "busy" {
		t.Errorf("list %+v", list)
	}
	if past, _, _ := s.Watch(ctx, "s1"); len(past) != 0 {
		t.Errorf("past %+v", past)
	}
	if !slices.Equal(a.forgot, []string{"s1"}) {
		t.Errorf("forgot %v", a.forgot)
	}

	// A session only archived is deleted; one that is nowhere is none.
	arch.Append(history.Who{Session: "old"}, []openai.Message{{Role: "user", Content: "before"}}, nil)
	if err := s.Delete(ctx, "old"); err != nil {
		t.Errorf("archived: %v", err)
	}
	for _, id := range []string{"old", "none", "", "../x"} {
		if err := s.Delete(ctx, id); !errors.Is(err, ErrNoSession) {
			t.Errorf("%q: %v", id, err)
		}
	}

	// What cannot be deleted is said.
	arch.Append(history.Who{Session: "stuck"}, []openai.Message{{Role: "user", Content: "hi"}}, nil)
	s.archive.Archive = unavailableArchive(t)
	if err := s.Delete(ctx, "stuck"); err == nil || errors.Is(err, ErrNoSession) {
		t.Errorf("not deleted: %v", err)
	}
}

// Without an archive a session is deleted from memory.
func TestDeleteInMemory(t *testing.T) {
	s := service(t, &fakeAgent{})
	ctx := context.Background()
	asked(t, s, "s1", "hi")
	if err := s.Delete(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "s1"); !errors.Is(err, ErrNoSession) {
		t.Fatal(err)
	}
	s.Watch(ctx, "quiet") // watched, but nothing was said in it
	if err := s.Delete(ctx, "quiet"); !errors.Is(err, ErrNoSession) {
		t.Fatal(err)
	}
	if list, err := s.Peers(ctx); err != nil || list != nil {
		t.Fatalf("peers %+v, %v", list, err)
	}
	if _, err := s.Read(ctx, "peer-x/y"); !errors.Is(err, ErrNoSession) {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "peer-x/y"); !errors.Is(err, ErrNoSession) {
		t.Fatal(err)
	}
	s.ForgetPeers(func(string) { t.Error("nothing to forget") })
}

// The conversations of peers are listed with whom they are, read and
// deleted, and the agent of the peers forgets a conversation deleted.
func TestPeers(t *testing.T) {
	dir := t.TempDir()
	s, arch, _ := archived(t, &fakeAgent{}, dir, time.Now())
	ctx := context.Background()
	beta := history.Who{Session: "conv", Peer: "key of beta"}
	arch.Append(beta, []openai.Message{{Role: "user", Content: "hello alpha"}, {Role: "assistant", Content: "hello beta"}}, nil)
	arch.Append(history.Who{Session: "mine"}, []openai.Message{{Role: "user", Content: "mine"}}, nil)
	var forgot []string
	s.ForgetPeers(func(id string) { forgot = append(forgot, id) })

	list, err := s.Peers(ctx)
	party := history.Party("key of beta")
	id := party + "/conv"
	if err != nil || len(list) != 1 || list[0].ID != id || list[0].Peer != "beta of "+party[:9] || list[0].Title != "hello alpha" {
		t.Fatalf("peers %+v, %v", list, err)
	}
	events, err := s.Read(ctx, id)
	if err != nil || kinds(events) != "message answer" || events[1].Text != "hello beta" {
		t.Fatalf("read %+v, %v", events, err)
	}
	for _, other := range []string{"mine", "owner/mine", party + "/none"} {
		if _, err := s.Read(ctx, other); !errors.Is(err, ErrNoSession) {
			t.Errorf("%q: %v", other, err)
		}
	}
	if _, err := s.Read(ctx, "peer-x/conv"); err == nil || errors.Is(err, ErrNoSession) {
		t.Errorf("a party that is none: %v", err)
	}
	// Only to read: it is no session of the owner.
	if err := s.Ask(ctx, id, "hi"); !errors.Is(err, ErrNoSession) {
		t.Errorf("ask: %v", err)
	}

	s.archive.Archive = unavailableArchive(t)
	if err := s.Delete(ctx, id); err == nil || errors.Is(err, ErrNoSession) {
		t.Errorf("not deleted: %v", err)
	}
	s.archive.Archive = arch
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(forgot, []string{"conv"}) {
		t.Errorf("forgot %v", forgot)
	}
	if err := s.Delete(ctx, id); !errors.Is(err, ErrNoSession) {
		t.Errorf("again: %v", err)
	}
	if list, _ := s.Peers(ctx); len(list) != 0 {
		t.Errorf("peers %+v", list)
	}
}

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "less than a minute", time.Minute: "1 minute", 59 * time.Minute: "59 minutes",
		time.Hour: "1 hour", 47 * time.Hour: "47 hours", 48 * time.Hour: "2 days", 400 * time.Hour: "16 days",
	} {
		if got := ago(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
	// A note of before the words changed is taken out as well.
	if got := unnoted("[Not from the owner: ouro started anew, a minute ago.]\n\nhi"); got != "hi" {
		t.Errorf("an earlier note: %q", got)
	}
	if got := unnoted("[Not from the owner: ouro started anew, never closed"); got != "[Not from the owner: ouro started anew, never closed" {
		t.Errorf("unnoted %q", got)
	}
}

// A conversation goes on under the model it was kept under: one kept without
// a model under the one of the node, and one kept under a model though the
// agent of the node cannot tell its own.
func TestArchivedSessionIsResumedWhateverTheModel(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	history.Open(dir).Append(history.Who{Session: "s1"}, []openai.Message{{Role: "user", Content: "hi"}}, nil)
	b := &fakeAgent{model: "m"}
	s, _, _ := archived(t, b, dir, time.Now())
	s.Watch(ctx, "s1")
	if _, ok := b.resumed["s1"]; !ok {
		t.Fatal("a session kept without a model is resumed under the one of the node")
	}

	dir = t.TempDir()
	arch := history.Open(dir)
	arch.Append(history.Who{Session: "s2"}, []openai.Message{{Role: "user", Content: "hi"}}, nil)
	if err := arch.SetModel(history.Owner("s2"), "m"); err != nil {
		t.Fatal(err)
	}
	b = &fakeAgent{}
	s, _, _ = archived(t, b, dir, time.Now())
	s.Watch(ctx, "s2")
	if _, ok := b.resumed["s2"]; !ok {
		t.Fatal("a session kept under a model is resumed though the agent cannot tell its own")
	}
}

// Legacy records have no identity of their own: they are numbered by their
// place in the archive, from 1.
func TestReplayNumbersLegacyRecords(t *testing.T) {
	events, _ := replay([]history.Record{
		{Role: "assistant", Content: "one"},
		{Role: "assistant", Content: "two"},
	})
	if len(events) != 2 || events[0].PresentationID != "archive:1" || events[1].PresentationID != "archive:2" {
		t.Fatalf("events %+v", events)
	}
}
