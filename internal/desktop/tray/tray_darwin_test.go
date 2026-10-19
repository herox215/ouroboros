//go:build darwin && cgo

package tray

import (
	"context"
	"testing"

	"ouroboros/internal/desktop"
)

func TestNativeModel(t *testing.T) {
	s := desktop.Status{State: "waiting", Running: 2, Waiting: 1, Sessions: []string{"A session"}}
	got := nativeModel(s)
	if got.Label != s.Label() || !got.Attention || got.Running != 2 || len(got.Sessions) != 1 || got.Sessions[0] != "A session" {
		t.Fatal(got)
	}
	if nativeModel(desktop.Status{State: "idle"}).Attention {
		t.Fatal("idle needs no attention")
	}
}

func TestRunCallbacksAndCancellation(t *testing.T) {
	if Run(context.Background(), nil, nil) == nil {
		t.Fatal("accepted nil callbacks")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, func(context.Context) desktop.Status { t.Fatal("sampled canceled context"); return desktop.Status{} }, func() { t.Fatal("opened UI") })
	if err != nil {
		t.Fatal(err)
	}
}
