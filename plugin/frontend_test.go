package plugin_test

import (
	"context"
	"errors"
	"testing"

	"ouroboros/internal/session"
	"ouroboros/plugin"
)

// The error of a front end that is to be started anew names what starts.
func TestRestartingSaysFromWhat(t *testing.T) {
	if got := (&plugin.Restarting{Exe: "/bin/ouro"}).Error(); got != "ouro is to be started anew from /bin/ouro" {
		t.Fatalf("got %q", got)
	}
}

// A front end reads the picture on the clipboard with the tools of the
// desktop, which has none here, and tells the files of pictures by name.
func TestClipboardPicture(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("PATH", t.TempDir())
	if p, err := plugin.ClipboardPicture(context.Background()); !errors.Is(err, plugin.ErrNoPicture) || p.Path != "" || p.Data != nil {
		t.Fatalf("picture %+v, %v", p, err)
	}
	if !plugin.IsPicture("/a/cat.png") || plugin.IsPicture("/a/notes.txt") {
		t.Fatal("a picture is told by its extension")
	}
	if got := plugin.LocalPath("file:///a/my%20cat.png"); got != "/a/my cat.png" {
		t.Fatalf("local path %q", got)
	}
}

// A tool asks the node for a new build, to go on there, and tells the jobs
// of its answer through the context the node hands it; outside of one nobody
// is asked or told.
func TestToolAsksTheNode(t *testing.T) {
	ctx := context.Background()
	if plugin.Restart(ctx, "/new/ouro") || plugin.GoOn(ctx) || plugin.TellJobs(ctx, nil, nil) {
		t.Fatal("asked or told outside of a node")
	}
	var exe string
	wentOn := 0
	ctx = plugin.WithGoOn(plugin.WithRestart(ctx, func(e string) { exe = e }), func() { wentOn++ })
	if !plugin.Restart(ctx, "/new/ouro") || exe != "/new/ouro" {
		t.Fatalf("restart with %q", exe)
	}
	if !plugin.GoOn(ctx) || wentOn != 1 {
		t.Fatalf("went on %d times", wentOn)
	}
	var told []plugin.Job
	var stop func(int) bool
	ctx = session.WithJobs(ctx, func(running []plugin.Job, s func(int) bool) { told, stop = running, s })
	running := []plugin.Job{{N: 1, Command: "make test"}}
	if !plugin.TellJobs(ctx, running, func(n int) bool { return n == 1 }) || len(told) != 1 || told[0].Command != "make test" || !stop(1) || stop(2) {
		t.Fatalf("told %+v", told)
	}
}
