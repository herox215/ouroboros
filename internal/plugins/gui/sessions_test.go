package gui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/kernel"
	"ouroboros/plugin"
)

type testSessions struct {
	plugin.Sessions
	list       func(context.Context) ([]plugin.SessionInfo, error)
	peers      []plugin.SessionInfo
	watch      func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error)
	read       func(context.Context, string) ([]plugin.SessionEvent, error)
	details    func(context.Context, string, string) (plugin.SessionToolResult, error)
	newSession func(context.Context) (string, error)
	ask        func(context.Context, string, string) error
	steer      func(context.Context, string, string) error
	open       func(context.Context) (bool, error)
	release    func(context.Context) error
}

func (s *testSessions) Open(ctx context.Context) (bool, error) {
	if s.open != nil {
		return s.open(ctx)
	}
	return false, nil
}
func (s *testSessions) Release(ctx context.Context) error {
	if s.release != nil {
		return s.release(ctx)
	}
	return nil
}
func (s *testSessions) New(ctx context.Context) (string, error)        { return s.newSession(ctx) }
func (s *testSessions) Ask(ctx context.Context, id, text string) error { return s.ask(ctx, id, text) }
func (s *testSessions) Steer(ctx context.Context, id, text string) error {
	return s.steer(ctx, id, text)
}

func (s *testSessions) List(ctx context.Context) ([]plugin.SessionInfo, error) {
	if s.list != nil {
		return s.list(ctx)
	}
	return nil, nil
}
func (s *testSessions) Peers(context.Context) ([]plugin.SessionInfo, error) { return s.peers, nil }
func (s *testSessions) Watch(ctx context.Context, id string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
	return s.watch(ctx, id)
}
func (s *testSessions) Read(ctx context.Context, id string) ([]plugin.SessionEvent, error) {
	return s.read(ctx, id)
}
func (s *testSessions) ToolResult(ctx context.Context, id, call string) (plugin.SessionToolResult, error) {
	return s.details(ctx, id, call)
}
func sessionProvider(s plugin.Sessions) plugin.Plugin {
	return plugin.NewService("sessions", func(*plugin.Context, struct{}) (struct{ Sessions plugin.Sessions }, error) {
		return struct{ Sessions plugin.Sessions }{s}, nil
	})
}
func sessionWindow(t *testing.T, services ...plugin.Sessions) *Window {
	t.Helper()
	p := page{}
	for i, s := range services {
		p.choices = append(p.choices, choice{name: fmt.Sprint(i)})
		p.profiles = append(p.profiles, &Profile{sessions: s})
	}
	w := newWindow(p)
	t.Cleanup(w.Close)
	return w
}
func canceled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("watch was not canceled")
	}
}

func TestSessionServiceSelection(t *testing.T) {
	var watched context.Context
	a := &testSessions{
		list: func(context.Context) ([]plugin.SessionInfo, error) {
			return []plugin.SessionInfo{{ID: "shared", Title: "A", Last: time.Unix(1, 0)}}, nil
		},
		peers: []plugin.SessionInfo{{ID: "peer-key/conv", Title: "peer", Last: time.Unix(2, 0)}},
		watch: func(ctx context.Context, id string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
			if id != "shared" {
				t.Fatalf("owner ID: %q", id)
			}
			watched = ctx
			return []plugin.SessionEvent{{Kind: plugin.SessionMessage, Text: "owner"}}, make(chan plugin.SessionEvent), nil
		},
		read: func(_ context.Context, id string) ([]plugin.SessionEvent, error) {
			if id != "peer-key/conv" {
				t.Fatalf("peer ID: %q", id)
			}
			return []plugin.SessionEvent{{Kind: plugin.SessionAnswer, Text: "peer"}}, nil
		},
	}
	b := &testSessions{watch: func(ctx context.Context, id string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		return []plugin.SessionEvent{{Kind: plugin.SessionAnswer, Text: "profile B"}}, nil, nil
	}}
	w := sessionWindow(t, a, b)
	sessions, err := w.Sessions(0)
	if err != nil || len(sessions) != 2 || sessions[0].ID != "peer-key/conv" || sessions[1].ID != "owner/shared" || sessions[1].Title != "A" {
		t.Fatalf("sessions: %+v %v", sessions, err)
	}
	owner, err := w.OpenSession(0, "owner/shared")
	if err != nil || owner.Events[0].Text != "owner" {
		t.Fatalf("owner: %+v %v", owner, err)
	}
	peer, err := w.OpenSession(0, "peer-key/conv")
	if err != nil || peer.Events[0].Text != "peer" || peer.Token != 0 {
		t.Fatalf("peer: %+v %v", peer, err)
	}
	canceled(t, watched)
	if events := w.PollSession(owner.Token); events == nil || len(events) != 0 {
		t.Fatalf("stale: %+v", events)
	}
	if events := w.PollSession(peer.Token); events == nil || len(events) != 0 {
		t.Fatalf("peer poll: %+v", events)
	}
	other, err := w.OpenSession(1, "owner/shared")
	if err != nil || other.Events[0].Text != "profile B" {
		t.Fatalf("profile isolation: %+v %v", other, err)
	}
	if other.Token == owner.Token || other.Token == peer.Token {
		t.Fatal("token reused")
	}
	// Listing another profile is also a selection, not a second simultaneous watch.
	_, _ = w.OpenSession(0, "owner/shared")
	_, _ = w.Sessions(1)
	canceled(t, watched)
}

func TestSessionPolling(t *testing.T) {
	live := make(chan plugin.SessionEvent, 205)
	for i := 0; i < 205; i++ {
		live <- plugin.SessionEvent{Kind: plugin.SessionAnswering, Text: fmt.Sprint(i)}
	}
	close(live)
	var ctx context.Context
	w := sessionWindow(t, &testSessions{watch: func(c context.Context, _ string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		ctx = c
		return nil, live, nil
	}})
	snapshot, err := w.OpenSession(0, "owner/a")
	if err != nil || snapshot.Events == nil {
		t.Fatalf("open: %+v %v", snapshot, err)
	}
	next := 0
	for _, count := range []int{100, 100, 5, 0} {
		events := w.PollSession(snapshot.Token)
		if len(events) != count {
			t.Fatalf("batch: %d, want %d", len(events), count)
		}
		if count == 0 && events != nil {
			t.Fatal("closed stream must signal EOF, not trigger endless polling")
		}
		for _, e := range events {
			if e.Text != fmt.Sprint(next) {
				t.Fatalf("lost event %d: %+v", next, e)
			}
			next++
		}
	}
	w.Close()
	canceled(t, ctx)
	if events := w.PollSession(snapshot.Token); events == nil || len(events) != 0 {
		t.Fatalf("closed: %+v", events)
	}
}

func TestPollWaitAndShutdown(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	var watched context.Context
	live := make(chan plugin.SessionEvent, 1)
	p := page{choices: []choice{{name: "a"}}, profiles: []*Profile{{sessions: &testSessions{watch: func(ctx context.Context, _ string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		watched = ctx
		return nil, live, nil
	}}}}}
	w := newWindowContext(parent, p)
	defer w.Close()
	snapshot, err := w.OpenSession(0, "owner/a")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if got := w.PollSession(snapshot.Token); got == nil || len(got) != 0 {
		t.Fatal(got)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond || elapsed > time.Second {
		t.Fatalf("wait: %s", elapsed)
	}
	result := make(chan []ChatEvent, 1)
	go func() { result <- w.PollSession(snapshot.Token) }()
	live <- plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "later"}
	select {
	case events := <-result:
		if len(events) != 1 || events[0].Text != "later" {
			t.Fatal(events)
		}
	case <-time.After(time.Second):
		t.Fatal("poll stalled")
	}
	go func() { result <- w.PollSession(snapshot.Token) }()
	cancel()
	canceled(t, watched)
	select {
	case events := <-result:
		if len(events) != 0 {
			t.Fatal(events)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown poll stalled")
	}
}

func TestConcurrentOpenCancelsPending(t *testing.T) {
	entered := make(chan context.Context, 1)
	s := &testSessions{watch: func(ctx context.Context, id string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		if id == "slow" {
			entered <- ctx
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}
		return []plugin.SessionEvent{{Kind: plugin.SessionAnswer, Text: id}}, nil, nil
	}}
	w := sessionWindow(t, s)
	result := make(chan error, 1)
	go func() { _, err := w.OpenSession(0, "owner/slow"); result <- err }()
	ctx := <-entered
	latest, err := w.OpenSession(0, "owner/fast")
	if err != nil || latest.Events[0].Text != "fast" {
		t.Fatalf("latest %+v %v", latest, err)
	}
	canceled(t, ctx)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("open stalled")
	}
	if got := w.PollSession(latest.Token); got == nil || len(got) != 0 {
		t.Fatal(got)
	}
}

func TestChatFormattingAndLazyDetails(t *testing.T) {
	var pctx *plugin.Context
	k := kernel.New()
	if err := k.Start(plugin.New("views", func(ctx *plugin.Context, _ struct{}) error {
		pctx = ctx
		plugin.Add(ctx, plugin.ToolView{Tool: "read", Show: func(json.RawMessage) string { return "reading file" }, Detail: func(s plugin.Step) string {
			if s.Remark != "" || (s.Err != nil && s.Err.Error() != "failed") {
				t.Fatal("raw result/error reached detail")
			}
			return "✓"
		}})
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	details := 0
	s := &testSessions{watch: func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		return []plugin.SessionEvent{
			{Kind: plugin.SessionStep, Tool: "read", CallID: "call", Args: json.RawMessage(`{"secret":"arguments"}`), Done: true, Err: "raw error", Remark: "full result", Text: "full result"},
			{Kind: plugin.SessionStep, Tool: "run", CallID: "run-call", Done: true},
		}, nil, nil
	}, details: func(_ context.Context, id, call string) (plugin.SessionToolResult, error) {
		details++
		if id != "a" && id != "peer-key/conv" {
			t.Fatalf("details ID: %q", id)
		}
		if call != "call" {
			t.Fatal(call)
		}
		return plugin.SessionToolResult{Arguments: "original args", Content: "full result", Found: true}, nil
	}}
	w := sessionWindow(t, s)
	w.profiles[0].ctx = pctx
	snap, err := w.OpenSession(0, "owner/a")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Events[0].Text != "reading file ✓" || !snap.Events[0].Failed || snap.Events[1].Text != "run" || snap.Events[1].CallID != "run-call" {
		t.Fatalf("format: %+v", snap)
	}
	b, _ := json.Marshal(snap)
	for _, secret := range []string{`"args"`, `"result"`, `"err"`, "secret", "raw error", "full result"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("leaked %q: %s", secret, b)
		}
	}
	_, _ = w.Sessions(0)
	_ = w.PollSession(snap.Token)
	if details != 0 {
		t.Fatal("details fetched eagerly")
	}
	for _, id := range []string{"owner/a", "peer-key/conv"} {
		result, err := w.ToolResult(0, id, "call")
		if err != nil || !result.Found || result.Content != "full result" {
			t.Fatalf("details %+v %v", result, err)
		}
	}
	if details != 2 {
		t.Fatal(details)
	}
	for _, id := range []string{"a", "owner/", "owner/../a", "owner/..", "peer-/a", "other/a", "owner/a/b"} {
		if _, err := w.OpenSession(0, id); err == nil {
			t.Fatalf("accepted open %q", id)
		}
		if _, err := w.ToolResult(0, id, "call"); err == nil {
			t.Fatalf("accepted details %q", id)
		}
	}
	if details != 2 {
		t.Fatal("invalid ID reached service")
	}
}

func TestSessionBridgeErrors(t *testing.T) {
	unavailable := sessionWindow(t, nil)
	if _, err := unavailable.Sessions(0); err == nil {
		t.Fatal("missing service list")
	}
	if _, err := unavailable.OpenSession(0, "owner/a"); err == nil {
		t.Fatal("missing service watch")
	}
	if _, err := unavailable.ToolResult(0, "owner/a", "call"); err == nil {
		t.Fatal("missing service details")
	}
	for _, i := range []int{-1, 1} {
		if _, err := unavailable.OpenSession(i, "owner/a"); err == nil {
			t.Fatal("invalid profile")
		}
	}
	sentinel := errors.New("unavailable")
	w := sessionWindow(t, &testSessions{watch: func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		return nil, nil, sentinel
	}})
	if _, err := w.OpenSession(0, "owner/a"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}
