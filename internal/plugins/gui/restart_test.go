package gui

import (
	"context"
	"errors"
	"os"
	"strings"
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

// Restart names the profile the window shows, and none when it shows none.
func TestRestartNamesTheProfile(t *testing.T) {
	two := []choice{{"Luna", "aaa"}, {"Other", "bbb"}}
	for _, tc := range []struct {
		choices  []choice
		selected int
		want     string
	}{{two, 0, "aaa"}, {two, 1, "bbb"}, {two, 2, ""}, {two, -1, ""}, {nil, 0, ""}} {
		w := newWindow(page{choices: tc.choices, selected: tc.selected})
		w.OnRestart(func() {})
		w.restart("/new/ouro")
		if exe, going, ok := w.Restart(); !ok || exe != "/new/ouro" || going.Profile != tc.want {
			t.Errorf("%d of %d: %q %+v %v", tc.selected, len(tc.choices), exe, going, ok)
		}
		w.Close()
	}
}

// nodeSessions are sessions whose node tells the image of ouro it runs.
type nodeSessions struct {
	*testSessions
	instance func() (plugin.NodeInstance, error)
}

func (s nodeSessions) Instance(context.Context) (plugin.NodeInstance, error) { return s.instance() }

// A node that started anew without a restart event of the session the window
// watches, as a rebuild in another session, ouro update or a peer make it
// do, leaves the window for the build it runs once the window opens a
// session again: the thread as an individual session.
func TestWindowNoticesARestartElsewhere(t *testing.T) {
	for _, id := range []string{"owner/" + plugin.ThreadID, "owner/s1"} {
		now := plugin.NodeInstance{ID: "a", Executable: "/old/ouro"}
		var failing error
		sessions := nodeSessions{&testSessions{watch: func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
			return nil, make(chan plugin.SessionEvent), nil
		}}, func() (plugin.NodeInstance, error) { return now, failing }}
		w := newWindow(page{choices: []choice{{"Luna", "aaa"}}, profiles: []*Profile{{sessions: sessions}}})
		leaves := 0
		w.OnRestart(func() { leaves++ })
		open := func() {
			t.Helper()
			if _, err := w.OpenSession(0, id); err != nil {
				t.Fatal(err)
			}
		}
		open()
		open()
		// A node that does not answer, or tells nothing, is no new one.
		failing = plugin.ErrNotRunning
		open()
		failing = nil
		now = plugin.NodeInstance{}
		open()
		if _, _, ok := w.Restart(); ok || leaves != 0 {
			t.Fatalf("%s: left the same node", id)
		}
		now = plugin.NodeInstance{ID: "b", Executable: "/new/ouro"}
		open()
		exe, going, ok := w.Restart()
		if !ok || exe != "/new/ouro" || going != (resume{Profile: "aaa", Session: id}) || leaves != 1 {
			t.Fatalf("%s: %q %+v %v, leaves %d", id, exe, going, ok, leaves)
		}
		// What the window hands over is what the next one goes on with.
		r, _ := errors.AsType[*plugin.Restarting](handOver(exe, going))
		for _, v := range r.Env {
			if value, ok := strings.CutPrefix(v, resumeEnv+"="); ok {
				t.Setenv(resumeEnv, value)
			}
		}
		if back, err := resumed(); err != nil || *back != going {
			t.Fatalf("%s: resumed %+v %v", id, back, err)
		}
		if _, ok := os.LookupEnv(resumeEnv); ok {
			t.Fatal("the node would see the handover")
		}
		w.Close()
	}
}

// Where nothing closes the window, or the node does not say which binary it
// runs, the window watches the session of the new node instead: the front end
// replays it in full.
func TestWindowWatchesTheNewNodeWhereItCannotLeave(t *testing.T) {
	for _, closes := range []bool{false, true} {
		now := plugin.NodeInstance{ID: "a", Executable: "/old/ouro"}
		watches := 0
		sessions := nodeSessions{&testSessions{watch: func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
			watches++
			return []plugin.SessionEvent{{Kind: plugin.SessionMessage, Text: "rebuild"}}, make(chan plugin.SessionEvent), nil
		}}, func() (plugin.NodeInstance, error) { return now, nil }}
		w := sessionWindow(t, sessions)
		if closes {
			w.OnRestart(func() {})
		}
		if _, err := w.OpenSession(0, "owner/thread"); err != nil {
			t.Fatal(err)
		}
		now = plugin.NodeInstance{ID: "b"}
		if closes {
			now.Executable = ""
		} else {
			now.Executable = "/new/ouro"
		}
		snapshot, err := w.OpenSession(0, "owner/thread")
		if err != nil || len(snapshot.Events) != 1 || snapshot.Token == 0 || watches != 2 {
			t.Fatalf("closes %v: %+v %v, watches %d", closes, snapshot, err, watches)
		}
		if _, _, ok := w.Restart(); ok {
			t.Fatalf("closes %v: left without a build to go to", closes)
		}
	}
}
