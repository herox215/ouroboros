package plugin_test

import (
	"context"
	"errors"
	"testing"

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
