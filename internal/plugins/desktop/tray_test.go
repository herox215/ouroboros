package desktop

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	model "ouroboros/internal/desktop"
	"ouroboros/plugin"
)

func TestTrayAggregate(t *testing.T) {
	a := &Profile{source: &testSessions{activity: plugin.Activity{Running: 2}}}
	b := &Profile{source: &testSessions{activity: plugin.Activity{Running: 1, Waiting: 1}}}
	Together([]*Profile{a, b})
	if a.controller != b.controller {
		t.Fatal("profiles do not share tray")
	}
	got := a.controller.sample(context.Background())
	if got.State != "waiting" || got.Running != 3 || got.Waiting != 1 {
		t.Fatal(got)
	}
	b.source = &testSessions{err: errors.New("unavailable")}
	if got := a.controller.sample(context.Background()); got.State != "error" {
		t.Fatal(got)
	}
	b.source = nil
	if got := a.controller.sample(context.Background()); got.State != "error" {
		t.Fatal(got)
	}
}

func TestTrayLifetime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux node adapter")
	}
	p := &Profile{log: func(err error) { t.Errorf("tray error: %v", err) }}
	q := &Profile{}
	Together([]*Profile{p, q})
	c := p.controller
	started := make(chan struct{})
	var runs atomic.Int32
	c.run = func(ctx context.Context, _ func(context.Context) model.Status, _ func()) error {
		runs.Add(1)
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	p.start()
	q.start()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("not started")
	}
	p.stop()
	q.stop()
	if runs.Load() != 1 {
		t.Fatal(runs.Load())
	}
}

func TestTrayOpenCoalesces(t *testing.T) {
	p := &Profile{log: func(err error) { t.Errorf("open: %v", err) }}
	Together([]*Profile{p})
	c := p.controller
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c.launch = func() error { calls.Add(1); close(entered); <-release; return nil }
	c.open()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not opened")
	}
	c.open()
	if calls.Load() != 1 {
		t.Fatal("opened duplicate GUI")
	}
	close(release)
}
