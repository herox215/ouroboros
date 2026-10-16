package matrix

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fakeConnection struct {
	started chan struct{}
	stopped chan struct{}
	sends   atomic.Int32
}

func (f *fakeConnection) Run(ctx context.Context, _ func(context.Context, incoming) error) error {
	close(f.started)
	<-ctx.Done()
	close(f.stopped)
	return ctx.Err()
}
func (f *fakeConnection) Send(context.Context, string, string) error { f.sends.Add(1); return nil }
func (f *fakeConnection) SendTo(ctx context.Context, room, peer, text string) error {
	return f.Send(ctx, room, text)
}
func (f *fakeConnection) TypingTo(context.Context, string, string, bool) error { return nil }
func (f *fakeConnection) Close() error                                         { return nil }

func TestControllerStartsOnlyWhileServingAndStops(t *testing.T) {
	s := store{t.TempDir()}
	if err := s.save(configuration{Enabled: true, Token: "token", Revision: "1"}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeConnection{started: make(chan struct{}), stopped: make(chan struct{})}
	c := controller{store: s, connect: func(context.Context, configuration) (connection, error) { return fake, nil }}
	select {
	case <-fake.started:
		t.Fatal("started before serving")
	default:
	}
	c.start()
	c.start() // Idempotent: no second sync loop.
	select {
	case <-fake.started:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	c.stop()
	select {
	case <-fake.stopped:
	default:
		t.Fatal("did not stop sync")
	}
	c.stop()
}

func TestGuardRejectsConfigurationChangesBeforeSending(t *testing.T) {
	s := store{t.TempDir()}
	cfg := configuration{Enabled: true, Revision: "1"}
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	fake := &fakeConnection{}
	g := guardedConnection{connection: fake, store: s, revision: "1"}
	if err := g.SendTo(context.Background(), "!room:example.org", "@owner:example.org", "hello"); err != nil {
		t.Fatal(err)
	}
	cfg.Revision = "2"
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.SendTo(context.Background(), "!room:example.org", "@owner:example.org", "must not leak"); err == nil {
		t.Fatal("sent after policy change")
	}
	if fake.sends.Load() != 1 {
		t.Fatal("unexpected message count")
	}
}
