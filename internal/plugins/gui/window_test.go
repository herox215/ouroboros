package gui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/kernel"
	"ouroboros/plugin"
)

func TestInitialState(t *testing.T) {
	w := newWindow(&profile{}, "Luna")
	t.Cleanup(w.Close)
	state := w.InitialState()
	if state.Profile != "Luna" || state.Thread != "owner/"+plugin.ThreadID {
		t.Fatalf("state: %+v", state)
	}
}

// The window shows the very conversation the terminal UI shows first, with
// the same watch.
func TestOpenWatchesTheThread(t *testing.T) {
	s := &fakeSessions{past: []plugin.SessionEvent{
		{Kind: plugin.SessionMessage, Text: "hello"},
		{Kind: plugin.SessionThought, Text: "hmm"}, // not shown in the window
		{Kind: plugin.SessionAnswer, Text: "done"},
	}}
	w := testWindow(t, s)
	snapshot, err := w.Open()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.watchIDs) != 1 || s.watchIDs[0] != plugin.ThreadID {
		t.Fatalf("watched %v, want the thread", s.watchIDs)
	}
	if len(snapshot.Events) != 2 ||
		snapshot.Events[0].Kind != plugin.SessionMessage || snapshot.Events[0].Text != "hello" ||
		snapshot.Events[1].Kind != plugin.SessionAnswer || snapshot.Events[1].Text != "done" {
		t.Fatalf("events: %+v", snapshot.Events)
	}
}

// Opening again replaces the watch: the stream of the old one ends.
func TestOpenReplacesTheWatch(t *testing.T) {
	s := &fakeSessions{}
	w := testWindow(t, s)
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
	if len(s.watches) != 2 {
		t.Fatalf("watches: %d", len(s.watches))
	}
	canceled(t, s.watches[0])
	if s.watches[1].Err() != nil {
		t.Fatal("the current watch was canceled")
	}
}

// What the thread tells while the window watches is passed on, in the order
// it came, without the thoughts the window has no place for.
func TestOpenStreamsWhatTheThreadTells(t *testing.T) {
	live := make(chan plugin.SessionEvent, 8)
	s := &fakeSessions{live: live}
	w := testWindow(t, s)
	batches := make(chan []Event, 8)
	w.onEmit(func(events []Event) { batches <- events })
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
	live <- plugin.SessionEvent{Kind: plugin.SessionThought, Text: "hmm"}
	live <- plugin.SessionEvent{Kind: plugin.SessionSay, Text: "looking"}
	live <- plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "done"}
	got := collect(t, batches, 2)
	if got[0].Kind != plugin.SessionSay || got[0].Text != "looking" || got[1].Kind != plugin.SessionAnswer {
		t.Fatalf("events: %+v", got)
	}
}

// A flood tells in bounded passes.
func TestStreamBatches(t *testing.T) {
	live := make(chan plugin.SessionEvent, maxBatch*2)
	s := &fakeSessions{live: live}
	w := testWindow(t, s)
	batches := make(chan []Event, 8)
	w.onEmit(func(events []Event) { batches <- events })
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
	for range maxBatch {
		live <- plugin.SessionEvent{Kind: plugin.SessionAnswering, Text: "x"}
	}
	got := collect(t, batches, maxBatch)
	if len(got) != maxBatch {
		t.Fatalf("events: %d", len(got))
	}
}

// The end of the live stream is told once: the page opens the thread again.
func TestStreamEndTellsOnce(t *testing.T) {
	live := make(chan plugin.SessionEvent)
	s := &fakeSessions{live: live}
	w := testWindow(t, s)
	ended := make(chan struct{}, 2)
	w.onEnded(func() { ended <- struct{}{} })
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
	close(live)
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the end of the stream was not told")
	}
	select {
	case <-ended:
		t.Fatal("the end was told twice")
	case <-time.After(20 * time.Millisecond):
	}
}

// A tool that asks ouro to start anew leaves the window once, with the build
// it names, and no further restart event replaces it.
func TestRestartLeavesOnce(t *testing.T) {
	live := make(chan plugin.SessionEvent, 4)
	s := &fakeSessions{live: live}
	w := testWindow(t, s)
	left := make(chan struct{}, 2)
	w.onRestart(func() { left <- struct{}{} })
	batches := make(chan []Event, 4)
	w.onEmit(func(events []Event) { batches <- events })
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.pendingRestart(); ok {
		t.Fatal("restarted before a tool asked for it")
	}
	live <- plugin.SessionEvent{Kind: plugin.SessionRestart, Text: "/new/ouro"}
	select {
	case <-left:
	case <-time.After(2 * time.Second):
		t.Fatal("the window did not leave")
	}
	if exe, ok := w.pendingRestart(); !ok || exe != "/new/ouro" {
		t.Fatalf("restart: %q %v", exe, ok)
	}
	// The event is not part of the transcript; the answer after it is, so
	// that the stream clearly passed the second restart event too.
	live <- plugin.SessionEvent{Kind: plugin.SessionRestart, Text: "/other/ouro"}
	live <- plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "done"}
	collect(t, batches, 1)
	select {
	case <-left:
		t.Fatal("left twice")
	default:
	}
	if exe, _ := w.pendingRestart(); exe != "/new/ouro" {
		t.Fatalf("second restart replaced the build: %q", exe)
	}
}

// Without a driver that closes the window there is nowhere to go on, and the
// window stays rather than ending with a restart nobody handles.
func TestNobodyClosesTheWindow(t *testing.T) {
	live := make(chan plugin.SessionEvent, 1)
	s := &fakeSessions{live: live}
	w := testWindow(t, s)
	batches := make(chan []Event, 2)
	w.onEmit(func(events []Event) { batches <- events })
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
	live <- plugin.SessionEvent{Kind: plugin.SessionRestart, Text: "/new/ouro"}
	live <- plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "done"}
	collect(t, batches, 1)
	if _, ok := w.pendingRestart(); ok {
		t.Fatal("restarted without a window to close")
	}
}

// A node that started anew from another build, as a rebuild in another
// session makes it do, is taken by the window even if no restart event of the
// thread says so.
func TestOpenNoticesANewNodeImage(t *testing.T) {
	tells := &tellingSessions{fakeSessions: &fakeSessions{}}
	tells.instance = func(context.Context) (plugin.NodeInstance, error) {
		return plugin.NodeInstance{ID: "aaa"}, nil
	}
	w := testWindow(t, tells)
	left := make(chan struct{}, 2)
	w.onRestart(func() { left <- struct{}{} })
	if _, err := w.Open(); err != nil { // the first image is only remembered
		t.Fatal(err)
	}
	if _, err := w.Open(); err != nil { // and the same one changes nothing
		t.Fatal(err)
	}
	select {
	case <-left:
		t.Fatal("left for the same build")
	default:
	}
	tells.instance = func(context.Context) (plugin.NodeInstance, error) {
		return plugin.NodeInstance{ID: "bbb", Executable: "/new/ouro"}, nil
	}
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-left:
	case <-time.After(2 * time.Second):
		t.Fatal("the window did not leave for the new build")
	}
	if exe, ok := w.pendingRestart(); !ok || exe != "/new/ouro" {
		t.Fatalf("restart: %q %v", exe, ok)
	}
}

// A node whose image cannot be read, that tells none, or that names no
// binary changes nothing; nor does a service that cannot tell.
func TestNoticeRestartKeepsTheWindow(t *testing.T) {
	for name, instance := range map[string]func(context.Context) (plugin.NodeInstance, error){
		"error": func(context.Context) (plugin.NodeInstance, error) {
			return plugin.NodeInstance{}, errors.New("no answer")
		},
		"no id": func(context.Context) (plugin.NodeInstance, error) {
			return plugin.NodeInstance{Executable: "/new/ouro"}, nil
		},
		"no binary": func(context.Context) (plugin.NodeInstance, error) { return plugin.NodeInstance{ID: "bbb"}, nil },
	} {
		t.Run(name, func(t *testing.T) {
			tells := &tellingSessions{fakeSessions: &fakeSessions{}}
			tells.instance = func(context.Context) (plugin.NodeInstance, error) {
				return plugin.NodeInstance{ID: "aaa"}, nil
			}
			w := testWindow(t, tells)
			left := 0
			w.onRestart(func() { left++ })
			if _, err := w.Open(); err != nil {
				t.Fatal(err)
			}
			tells.instance = instance
			if _, err := w.Open(); err != nil {
				t.Fatal(err)
			}
			if _, ok := w.pendingRestart(); ok || left != 0 {
				t.Fatalf("restarted: %v, leaves %d", ok, left)
			}
		})
	}
	// A service that cannot tell at all.
	w := testWindow(t, &fakeSessions{})
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
}

// Send starts the answer, and the message says the window sent it. While an
// answer is on its way, the text goes to it instead.
func TestSendAsksThenSteers(t *testing.T) {
	s := &fakeSessions{}
	w := testWindow(t, s)
	if err := w.Send("  hello  "); err != nil {
		t.Fatal(err)
	}
	if len(s.asks) != 1 || s.asks[0] != (sent{plugin.ThreadID, "hello", "gui"}) {
		t.Fatalf("asks: %+v", s.asks)
	}
	if s.opens != 1 {
		t.Fatalf("the node was opened %d times", s.opens)
	}
	s.askErr = plugin.ErrBusy
	if err := w.Send("next"); err != nil {
		t.Fatal(err)
	}
	if len(s.steers) != 1 || s.steers[0] != (sent{plugin.ThreadID, "next", "gui"}) {
		t.Fatalf("steers: %+v", s.steers)
	}
	s.askErr = errors.New("provider gone")
	if err := w.Send("third"); err == nil || len(s.steers) != 1 {
		t.Fatalf("a failed ask steered: %v, %+v", err, s.steers)
	}
	if err := w.Send(" \n\t "); err == nil || len(s.asks) != 3 {
		t.Fatalf("an empty message was sent: %v", err)
	}
	if s.opens != 1 {
		t.Fatalf("the node was opened again: %d", s.opens)
	}
}

func TestSendFailsWithoutANode(t *testing.T) {
	s := &fakeSessions{openErr: errors.New("not set up")}
	w := testWindow(t, s)
	if err := w.Send("hello"); err == nil || len(s.asks) != 0 {
		t.Fatalf("send: %v, asks %+v", err, s.asks)
	}
	w2 := testWindow(t, s)
	w2.cancel()
	if err := w2.Send("hello"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a closed window sent: %v", err)
	}
}

// Stop cancels the thread itself: the node also stops the answers the thread
// handed to other sessions.
func TestStopCancelsTheThread(t *testing.T) {
	s := &fakeSessions{}
	w := testWindow(t, s)
	if err := w.Stop(); err != nil {
		t.Fatal(err)
	}
	if len(s.canceled) != 1 || s.canceled[0] != plugin.ThreadID {
		t.Fatalf("canceled: %v", s.canceled)
	}
	s.cancelErr = errors.New("nothing to stop")
	if err := w.Stop(); err == nil {
		t.Fatal("a failed cancel was hidden")
	}
}

// The window releases the node it started, once, and leaves a node it found
// running alone.
func TestCloseReleasesTheNodeItStarted(t *testing.T) {
	s := &fakeSessions{started: true, live: make(chan plugin.SessionEvent)}
	w := testWindow(t, s)
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
	watch := s.watches[0]
	w.Close()
	canceled(t, watch)
	if s.releases != 1 {
		t.Fatalf("released %d times", s.releases)
	}
	w.Close()
	if s.releases != 1 {
		t.Fatalf("released again: %d", s.releases)
	}

	other := &fakeSessions{}
	found := testWindow(t, other)
	if _, err := found.Open(); err != nil {
		t.Fatal(err)
	}
	found.Close()
	if other.releases != 0 {
		t.Fatalf("released a node it did not start: %d", other.releases)
	}
}

func TestOpenFails(t *testing.T) {
	s := &fakeSessions{openErr: errors.New("not set up")}
	if _, err := testWindow(t, s).Open(); err == nil {
		t.Fatal("opened without a node")
	}
	s = &fakeSessions{watchErr: errors.New("gone")}
	if _, err := testWindow(t, s).Open(); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("watch: %v", err)
	}
}

// A call of a tool shows what the plugin of the tool wants shown, and how it
// ended; a question shows what to choose from until it is answered.
func TestEventText(t *testing.T) {
	w := newWindow(&profile{}, "")
	if got := w.event(plugin.SessionEvent{Kind: plugin.SessionStep, Tool: "bash", Args: json.RawMessage(`"go test"`)}); got.Text != "bash" {
		t.Fatalf("a call without a view: %+v", got)
	}
	if got := w.event(plugin.SessionEvent{Kind: plugin.SessionStep, Tool: "bash", Done: true, Err: "exit 1"}); !got.Failed || got.Text != "bash" {
		t.Fatalf("a failed call: %+v", got)
	}

	k := kernel.New()
	if err := k.Start(plugin.New("view", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.ToolView{
			Tool:   "bash",
			Show:   func(args json.RawMessage) string { return "bash · " + string(args) },
			Detail: func(plugin.Step) string { return "· ok" },
		})
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	shown := newWindow(&profile{ctx: k.Context()}, "")
	call := plugin.SessionEvent{Kind: plugin.SessionStep, Tool: "bash", Args: json.RawMessage(`"go test"`)}
	if got := shown.event(call); got.Text != `bash · "go test"` {
		t.Fatalf("a call with a view: %+v", got)
	}
	call.Done = true
	if got := shown.event(call); got.Text != `bash · "go test" · ok` {
		t.Fatalf("an ended call with a view: %+v", got)
	}

	asked := w.event(plugin.SessionEvent{Kind: plugin.SessionAsked, Args: json.RawMessage(`{"question":"Which?","options":["a","b"],"recommended":"b"}`)})
	if asked.Text != "Which?" || len(asked.Options) != 2 || asked.Recommended != "b" {
		t.Fatalf("question: %+v", asked)
	}
	answered := w.event(plugin.SessionEvent{Kind: plugin.SessionAsked, Done: true, Text: "a"})
	if answered.Text != "a" || answered.Options != nil {
		t.Fatalf("answered question: %+v", answered)
	}
	other := w.event(plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "done", At: time.Unix(1, 0)})
	if other.Kind != plugin.SessionAnswer || other.Text != "done" {
		t.Fatalf("answer: %+v", other)
	}
}

// A watch that another open replaced passes nothing on and tells no end: the
// page shows the new one.
func TestStaleWatchTellsNothing(t *testing.T) {
	live := make(chan plugin.SessionEvent, 1)
	s := &fakeSessions{live: live}
	w := testWindow(t, s)
	emitted := 0
	ended := 0
	w.onEmit(func([]Event) { emitted++ })
	w.onEnded(func() { ended++ })
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
	// As if another open had replaced the watch before its batch went out.
	w.mu.Lock()
	w.gen++
	w.mu.Unlock()
	live <- plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "gone"}
	time.Sleep(20 * time.Millisecond)
	w.end(0) // for a watch that is not the current one
	if emitted != 0 || ended != 0 {
		t.Fatalf("told %d batches and %d ends of a stale watch", emitted, ended)
	}
}
