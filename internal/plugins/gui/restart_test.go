package gui

import (
	"context"
	"testing"

	"ouroboros/plugin"
)

// A tool that asks for a new build leaves the window once the live stream of
// the session it watches says so, and the driver starts that build in its
// place. What the same stream said before is history: it leaves nothing.
func TestWindowLeavesForANewBuild(t *testing.T) {
	live := make(chan plugin.SessionEvent, 2)
	w := sessionWindow(t, &testSessions{watch: func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		return []plugin.SessionEvent{{Kind: plugin.SessionRestart, Text: "/old/ouro"}}, live, nil
	}})
	leaves := 0
	w.OnRestart(func() { leaves++ })
	if _, _, ok := w.Restart(); ok {
		t.Fatal("restarted without a tool asking for it")
	}
	snapshot, err := w.OpenSession(0, "owner/s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := w.Restart(); ok {
		t.Fatal("a restart that happened before made the window leave")
	}
	live <- plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "done"}
	if events := w.PollSession(snapshot.Token); len(events) != 1 {
		t.Fatalf("events: %+v", events)
	}
	if _, _, ok := w.Restart(); ok || leaves != 0 {
		t.Fatal("an answer alone left the window")
	}
	live <- plugin.SessionEvent{Kind: plugin.SessionRestart, Text: "/new/ouro"}
	if events := w.PollSession(snapshot.Token); len(events) != 1 {
		t.Fatalf("restart event: %+v", events)
	}
	exe, going, ok := w.Restart()
	if !ok || exe != "/new/ouro" || going.Session != "owner/s1" || leaves != 1 {
		t.Fatalf("restart: %q %+v %v, leaves %d", exe, going, ok, leaves)
	}
	// Another build of another answer does not leave a second time.
	live <- plugin.SessionEvent{Kind: plugin.SessionRestart, Text: "/other/ouro"}
	w.PollSession(snapshot.Token)
	if exe, _, ok := w.Restart(); !ok || exe != "/new/ouro" || leaves != 1 {
		t.Fatalf("second restart: %q %v, leaves %d", exe, ok, leaves)
	}
}

// Without a driver that closes the window there is nowhere to go on, and the
// window stays as it is rather than ending with a restart nobody handles.
func TestNobodyClosesTheWindow(t *testing.T) {
	live := make(chan plugin.SessionEvent, 1)
	w := sessionWindow(t, &testSessions{watch: func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		return nil, live, nil
	}})
	snapshot, err := w.OpenSession(0, "owner/s1")
	if err != nil {
		t.Fatal(err)
	}
	live <- plugin.SessionEvent{Kind: plugin.SessionRestart, Text: "/new/ouro"}
	w.PollSession(snapshot.Token)
	if _, _, ok := w.Restart(); ok {
		t.Fatal("restarted without a window to close")
	}
}
