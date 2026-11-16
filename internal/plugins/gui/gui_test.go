package gui

import (
	"context"
	"errors"
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
	// through one of its own. It is contributed whether it is open or not, and
	// says with Available whether a window or the page of the thread shows the
	// thread now: a profile that shows neither is no channel of pictures.
	outputs := plugin.All[plugin.GatewayOutput](ctx)
	if len(outputs) != 1 || outputs[0].Name != "gui" || outputs[0].SendImage == nil {
		t.Fatalf("the window as a channel of pictures: %+v", outputs)
	}
	if outputs[0].Available == nil || outputs[0].Available(context.Background()) {
		t.Fatal("a profile without a window can show pictures")
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

// fakeWatchers is a sessions service that tells which front ends watch the
// thread, as the node's does.
type fakeWatchers struct {
	windows int
	others  bool // a conversation other than the thread works for it
}

func (f fakeWatchers) ShowsThread(id string) bool {
	return id == plugin.ThreadID || f.others
}

func (f fakeWatchers) Watching(id, source string) int {
	if id == plugin.ThreadID && source == plugin.SourceWindow {
		return f.windows
	}
	return 0
}

// The window is a provider of gateway_send_image, and tells with Available
// whether a picture shown now would be seen: a window of ouro that watches the
// thread in the node, or the page of the thread served there.
func TestTheWindowIsAProvider(t *testing.T) {
	ctx := testContext(t)
	out := pictureOutput(&fakeSessions{}, nil, pictures{dir: t.TempDir()}, func(context.Context) bool { return false })
	item := plugin.Add(ctx, out)
	t.Cleanup(item.Remove)
	outputs := plugin.All[plugin.GatewayOutput](ctx)
	if len(outputs) != 1 || outputs[0].Name != "gui" {
		t.Fatalf("the window as a channel of pictures: %+v", outputs)
	}
	if outputs[0].Available == nil || outputs[0].Available(context.Background()) {
		t.Fatal("a window that is not open can show a picture")
	}
	if _, err := outputs[0].SendImage(context.Background(), plugin.GatewayImageRequest{Attachment: 1}); err == nil || !strings.Contains(err.Error(), "no window of ouro") {
		t.Fatalf("a picture shown without a window: %v", err)
	}
	// A provider with no Available at all is always there, as the gateways are.
	item.Remove()
	plugin.Add(ctx, plugin.GatewayOutput{Name: "always", SendImage: func(context.Context, plugin.GatewayImageRequest) (string, error) { return "", nil }})
	if got := plugin.All[plugin.GatewayOutput](ctx); len(got) != 1 || got[0].Available != nil {
		t.Fatalf("a provider that is always there: %+v", got)
	}
}

// windowShows is what a node tells from the front end that watches: the window
// runs in a process of its own, and says so through the socket.
func TestShownByAWindowOrThePage(t *testing.T) {
	ctx := context.Background() // a call of no conversation the thread shows
	page := false
	shown := shownBy(fakeWatchers{windows: 1, others: true}, func() bool { return page })
	if !shown(ctx) {
		t.Fatal("a window that watches the thread shows no picture")
	}
	shown = shownBy(fakeWatchers{others: true}, func() bool { return page })
	if shown(ctx) {
		t.Fatal("a page that is off shows a picture")
	}
	page = true
	if !shown(ctx) {
		t.Fatal("a served page shows no picture")
	}
	if shownBy(fakeWatchers{others: true}, nil)(ctx) {
		t.Fatal("a process that shows nothing shows a picture")
	}
	// The window and the page show the thread: a conversation that does not
	// work for it has its picture seen by neither.
	if shownBy(fakeWatchers{windows: 1}, func() bool { return true })(ctx) {
		t.Fatal("a picture of a conversation the thread does not show is shown")
	}
}

func TestWindowShowsIsWhatTheNodeTells(t *testing.T) {
	if windowShows(nil) {
		t.Fatal("a process without sessions has an open window")
	}
	if windowShows(fakeWatchers{}) {
		t.Fatal("a window that does not watch is open")
	}
	if !windowShows(fakeWatchers{windows: 1}) {
		t.Fatal("a window that watches the thread is not open")
	}
}
