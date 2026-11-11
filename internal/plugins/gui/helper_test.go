package gui

import (
	"context"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

// fakeSessions implements as much of the sessions service as the window uses;
// the rest is left to the nil embedded interface, as no test asks for it.
type fakeSessions struct {
	plugin.Sessions

	mu         sync.Mutex
	opens      int
	releases   int
	started    bool
	openErr    error
	releaseErr error
	watches    []context.Context
	watchIDs   []string
	past       []plugin.SessionEvent
	live       chan plugin.SessionEvent
	watchErr   error
	asks       []sent
	steers     []sent
	canceled   []string
	askErr     error
	steerErr   error
	cancelErr  error
}

type sent struct{ id, text, source string }

func (s *fakeSessions) Open(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens++
	return s.started, s.openErr
}

func (s *fakeSessions) Release(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases++
	return s.releaseErr
}

func (s *fakeSessions) Watch(ctx context.Context, id string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watches = append(s.watches, ctx)
	s.watchIDs = append(s.watchIDs, id)
	if s.watchErr != nil {
		return nil, nil, s.watchErr
	}
	return s.past, s.live, nil
}

func (s *fakeSessions) Ask(ctx context.Context, id, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asks = append(s.asks, sent{id, text, openai.MessageSourceFrom(ctx).Interface})
	return s.askErr
}

func (s *fakeSessions) Steer(ctx context.Context, id, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steers = append(s.steers, sent{id, text, openai.MessageSourceFrom(ctx).Interface})
	return s.steerErr
}

func (s *fakeSessions) Cancel(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.canceled = append(s.canceled, id)
	return s.cancelErr
}

// tellingSessions is a sessions service that tells the image its node runs,
// as the local service does.
type tellingSessions struct {
	*fakeSessions
	instance func(context.Context) (plugin.NodeInstance, error)
}

func (s *tellingSessions) Instance(ctx context.Context) (plugin.NodeInstance, error) {
	return s.instance(ctx)
}

// sessionProvider hands s to a plugin that needs the sessions service.
func sessionProvider(s plugin.Sessions) plugin.Plugin {
	return plugin.NewService("sessions", func(*plugin.Context, struct{}) (struct{ Sessions plugin.Sessions }, error) {
		return struct{ Sessions plugin.Sessions }{s}, nil
	})
}

// testWindow is a window on s that passes what it tells on quickly, and is
// closed when the test ends.
func testWindow(t *testing.T, s plugin.Sessions) *Window {
	t.Helper()
	w := newWindow(&profile{sessions: s}, "Luna")
	w.flush = time.Millisecond
	t.Cleanup(w.Close)
	return w
}

// collect reads batches until want events came or two seconds passed.
func collect(t *testing.T, batches <-chan []Event, want int) []Event {
	t.Helper()
	var out []Event
	deadline := time.After(2 * time.Second)
	for len(out) < want {
		select {
		case batch := <-batches:
			out = append(out, batch...)
		case <-deadline:
			t.Fatalf("only %d of %d events came: %+v", len(out), want, out)
		}
	}
	return out
}

// canceled fails unless ctx was canceled within a second.
func canceled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the watch was not canceled")
	}
}
