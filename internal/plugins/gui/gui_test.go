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
	// The window shows pictures through the neutral tool of the gateways, not
	// through one of its own, and only while it or the page of the thread
	// shows them: a profile that shows neither is no channel of pictures.
	if outputs := plugin.All[plugin.GatewayOutput](ctx); len(outputs) != 0 {
		t.Fatalf("a profile without a window is a channel of pictures: %+v", outputs)
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

// testContext is the context of a kernel that runs the plugins of a test: what
// contributes a provider, and what reads them.
func testContext(t *testing.T) *plugin.Context {
	t.Helper()
	var ctx *plugin.Context
	k := kernel.New()
	if err := k.Start(plugin.New("spy", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	return ctx
}

// The window is a provider of gateway_send_image while it is open, and not
// before or after: a profile that shows no picture has no provider of its own.
func TestTheWindowIsAProviderWhileItIsOpen(t *testing.T) {
	ctx := testContext(t)
	p := fakeProfile("Luna")
	p.pictures = &pictureChannel{ctx: ctx, output: pictureOutput(&fakeSessions{}, nil, pictures{dir: t.TempDir()})}
	if got := plugin.All[plugin.GatewayOutput](ctx); len(got) != 0 {
		t.Fatalf("a profile without a window has %d providers: %+v", len(got), got)
	}
	old := show
	t.Cleanup(func() { show = old })
	show = func(context.Context, *profile, string) error {
		if got := plugin.All[plugin.GatewayOutput](ctx); len(got) != 1 || got[0].Name != "gui" {
			t.Fatalf("the open window is %d providers: %+v", len(got), got)
		}
		return nil
	}
	if err := p.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := plugin.All[plugin.GatewayOutput](ctx); len(got) != 0 {
		t.Fatalf("the window that closed left %d providers: %+v", len(got), got)
	}
}
