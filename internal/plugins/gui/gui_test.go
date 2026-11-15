package gui

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/guard"
	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	"ouroboros/plugin"
)

func fakeProfile(name string) *profile {
	return &profile{status: func() (plugin.NodeStatus, error) {
		return plugin.NodeStatus{Name: name, ID: strings.ToLower(name)}, nil
	}}
}

// The plugin contributes the command and no front end: ouro without a command
// still opens the terminal UI.
func TestCommand(t *testing.T) {
	var ctx *plugin.Context
	k := kernel.New()
	if err := k.Start(sessionProvider(&fakeSessions{}), Plugin(nil, home.Dir(t.TempDir()), &guard.Guard{Home: t.TempDir(), Workspace: t.TempDir()}), plugin.New("spy", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	commands := plugin.All[plugin.Command](ctx)
	if len(commands) != 1 || commands[0].Name != "gui" || commands[0].Help == "" {
		t.Fatalf("command: %+v", commands)
	}
	if len(plugin.All[plugin.FrontEnd](ctx)) != 0 {
		t.Fatal("the GUI replaced the default front end")
	}
	// The window shows pictures through the neutral tool of the gateways,
	// not through one of its own.
	outputs := plugin.All[plugin.GatewayOutput](ctx)
	if len(outputs) != 1 || outputs[0].Name != "gui" || outputs[0].SendImage == nil {
		t.Fatalf("the window as a channel of pictures: %+v", outputs)
	}
}

// The window shows the thread of the profile ouro runs for, and no other: the
// name comes from the profile, which must be readable before the window opens.
func TestRun(t *testing.T) {
	old := show
	t.Cleanup(func() { show = old })
	shown := 0
	var got *profile
	var name string
	show = func(_ context.Context, p *profile, n string) error {
		shown++
		got, name = p, n
		if n == "error" {
			return errors.New("window failed")
		}
		return nil
	}

	p := fakeProfile("Luna")
	if err := p.run(context.Background(), nil); err != nil || shown != 1 || got != p || name != "Luna" {
		t.Fatalf("run: %v, shown %d as %q", err, shown, name)
	}
	if err := p.run(context.Background(), []string{"extra"}); err == nil || shown != 1 {
		t.Fatal("accepted an argument")
	}
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.run(canceledCtx, nil); !errors.Is(err, context.Canceled) || shown != 1 {
		t.Fatalf("canceled: %v", err)
	}
	unnamed := &profile{status: func() (plugin.NodeStatus, error) { return plugin.NodeStatus{}, nil }}
	if err := unnamed.run(context.Background(), nil); err != nil || name != "Not set up" {
		t.Fatalf("unnamed: %v, %q", err, name)
	}
	broken := &profile{status: func() (plugin.NodeStatus, error) {
		return plugin.NodeStatus{}, errors.New("bad config")
	}}
	if err := broken.run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "bad config") || shown != 2 {
		t.Fatalf("unreadable profile: %v, shown %d", err, shown)
	}
	failing := fakeProfile("error")
	if err := failing.run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "window failed") {
		t.Fatalf("window failure: %v", err)
	}
}

// A restart starts the new binary with the environment of this process.
func TestHandOver(t *testing.T) {
	r, ok := errors.AsType[*plugin.Restarting](handOver("/new/ouro"))
	if !ok || r.Exe != "/new/ouro" || !slices.Equal(r.Env, os.Environ()) {
		t.Fatalf("handover: %+v, %v", r, ok)
	}
}
