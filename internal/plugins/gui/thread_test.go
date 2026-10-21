package gui

import (
	"context"
	"errors"
	"testing"

	"ouroboros/plugin"
)

func TestThreadStartsInGUIDirectoryPerProfile(t *testing.T) {
	var paths []string
	service := func() *testSessions {
		return &testSessions{
			directory: func(_ context.Context, id, path string) error {
				if id != plugin.ThreadID {
					t.Fatalf("directory ID: %q", id)
				}
				paths = append(paths, path)
				return nil
			},
			watch: func(_ context.Context, id string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
				if id != plugin.ThreadID {
					t.Fatalf("watch ID: %q", id)
				}
				return []plugin.SessionEvent{{Kind: plugin.SessionWorking, N: 3, Own: true}}, nil, nil
			},
		}
	}
	w := sessionWindow(t, service(), service())
	w.directory = t.TempDir()
	id := w.InitialState().Thread
	if id != "owner/"+plugin.ThreadID {
		t.Fatal(id)
	}
	for _, profile := range []int{0, 0, 1, 0} {
		snapshot, err := w.OpenSession(profile, id)
		if err != nil || len(snapshot.Events) != 1 || snapshot.Events[0].N != 3 || !snapshot.Events[0].Own {
			t.Fatalf("snapshot: %+v, %v", snapshot, err)
		}
	}
	if len(paths) != 2 || paths[0] != w.directory || paths[1] != w.directory {
		t.Fatal(paths)
	}
}

func TestBusyThreadKeepsProjectAndRemainsUsable(t *testing.T) {
	var asks, steers int
	s := &testSessions{
		directory: func(context.Context, string, string) error { return plugin.ErrBusy },
		watch: func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
			return nil, nil, nil
		},
		ask: func(_ context.Context, id, text string) error {
			if id != plugin.ThreadID || text != "keep going" {
				t.Fatal(id, text)
			}
			asks++
			return plugin.ErrBusy
		},
		steer: func(_ context.Context, id, text string) error {
			if id != plugin.ThreadID || text != "keep going" {
				t.Fatal(id, text)
			}
			steers++
			return nil
		},
	}
	w := sessionWindow(t, s)
	w.directory = t.TempDir()
	id := w.InitialState().Thread
	snapshot, err := w.OpenSession(0, id)
	if err != nil || snapshot.Notice == "" {
		t.Fatalf("snapshot: %+v, %v", snapshot, err)
	}
	if err := w.SendMessage(0, id, "keep going"); err != nil {
		t.Fatal(err)
	}
	if asks != 1 || steers != 1 {
		t.Fatal(asks, steers)
	}
}

func TestStopUsesOwnerSessionAndRejectsPeers(t *testing.T) {
	var stopped []string
	sentinel := errors.New("cannot cancel")
	w := sessionWindow(t, &testSessions{cancel: func(_ context.Context, id string) error {
		stopped = append(stopped, id)
		if id == "fails" {
			return sentinel
		}
		return nil
	}})
	for _, id := range []string{w.InitialState().Thread, "owner/topic"} {
		if err := w.StopSession(0, id); err != nil {
			t.Fatal(err)
		}
	}
	if len(stopped) != 2 || stopped[0] != plugin.ThreadID || stopped[1] != "topic" {
		t.Fatal(stopped)
	}
	for _, id := range []string{"peer-key/topic", "thread", "owner/../topic"} {
		if err := w.StopSession(0, id); err == nil {
			t.Fatal("accepted", id)
		}
	}
	if len(stopped) != 2 {
		t.Fatal(stopped)
	}
	if err := w.StopSession(0, "owner/fails"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if err := w.StopSession(1, "owner/topic"); err == nil {
		t.Fatal("invalid profile")
	}
}
