package tui

import (
	"context"
	"errors"
	"testing"

	"ouroboros/plugin"
)

type directorySessions struct {
	plugin.Sessions
	ctx      context.Context
	id, path string
	err      error
}

func (s *directorySessions) SetDirectory(ctx context.Context, id, path string) error {
	s.ctx, s.id, s.path = ctx, id, path
	return s.err
}

func TestSetSessionDirectoryForwarding(t *testing.T) {
	ctx := context.Background()
	s := &directorySessions{}
	u := &ui{sessions: s}
	for _, path := range []string{"/tmp/my project", ""} {
		for _, want := range []error{nil, plugin.ErrBusy} {
			s.err = want
			err := u.SetSessionDirectory(ctx, "session", path)
			if !errors.Is(err, want) || s.ctx != ctx || s.id != "session" || s.path != path {
				t.Fatalf("forwarded %+v, error %v", s, err)
			}
		}
	}
}

func TestDirectoryEventForwarding(t *testing.T) {
	u := &ui{}
	for _, path := range []string{"/tmp/my project", ""} {
		e := u.event(plugin.SessionEvent{Kind: "directory", Text: path})
		if e.Kind != "directory" || e.Text != path {
			t.Fatalf("event %+v", e)
		}
	}
}

// A memory the review kept comes with what takes it back.
func TestRememberedEventForwarding(t *testing.T) {
	u := &ui{}
	e := u.event(plugin.SessionEvent{Kind: plugin.SessionRemembered, Text: "build: make build", Args: []byte(`{"memory":{}}`)})
	if e.Text != "build: make build" || string(e.Remembered) != `{"memory":{}}` {
		t.Fatalf("event %+v", e)
	}
}
