package pasteboard

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
)

// desktop is a Reader of a desktop whose tools answer as tools says, by
// their command line: one that is not in it fails.
func desktop(goos string, env map[string]string, tools map[string]string) Reader {
	return Reader{GOOS: goos, Getenv: func(name string) string { return env[name] },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			out, ok := tools[strings.Join(append([]string{name}, args...), " ")]
			if !ok {
				return nil, errors.New("exit status 1")
			}
			return []byte(out), nil
		}}
}

var (
	wayland = map[string]string{"WAYLAND_DISPLAY": "wayland-1", "DISPLAY": ":0"}
	x11     = map[string]string{"DISPLAY": ":0"}
)

// The clipboard gives the picture it holds: the file that was copied before
// the data of a picture, and of the data the first kind it is asked for.
func TestPicture(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		reader Reader
		want   Picture
	}{
		"a screenshot on wayland": {desktop("linux", wayland, map[string]string{
			"wl-paste --list-types":                  "text/plain\nimage/jpeg\nimage/png\n",
			"wl-paste --no-newline --type image/png": "PNG",
		}), Picture{Data: []byte("PNG"), Ext: "png"}},
		"a jpeg on wayland": {desktop("linux", wayland, map[string]string{
			"wl-paste --list-types":                   "image/jpeg\n",
			"wl-paste --no-newline --type image/jpeg": "JPEG",
		}), Picture{Data: []byte("JPEG"), Ext: "jpg"}},
		"a file copied on wayland": {desktop("linux", wayland, map[string]string{
			"wl-paste --list-types":                      "text/uri-list\nimage/png\n",
			"wl-paste --no-newline --type text/uri-list": "# copied\r\n\r\nfile:///home/me/my%20cat.PNG\r\nfile:///home/me/other.png\r\n",
		}), Picture{Path: "/home/me/my cat.PNG"}},
		"a file that is no picture, with a preview": {desktop("linux", wayland, map[string]string{
			"wl-paste --list-types":                      "text/uri-list\nimage/png\n",
			"wl-paste --no-newline --type text/uri-list": "file:///home/me/notes.txt\n",
			"wl-paste --no-newline --type image/png":     "PNG",
		}), Picture{Data: []byte("PNG"), Ext: "png"}},
		"a list of files that cannot be read": {desktop("linux", wayland, map[string]string{
			"wl-paste --list-types":                  "text/uri-list\nimage/png\n",
			"wl-paste --no-newline --type image/png": "PNG",
		}), Picture{Data: []byte("PNG"), Ext: "png"}},
		"a screenshot on x11": {desktop("linux", x11, map[string]string{
			"xclip -selection clipboard -t TARGETS -o":    "TARGETS\nimage/webp\n",
			"xclip -selection clipboard -t image/webp -o": "WEBP",
		}), Picture{Data: []byte("WEBP"), Ext: "webp"}},
		"a screenshot on macOS": {desktop("darwin", wayland, map[string]string{
			"osascript -e the clipboard as «class PNGf»": "«data PNGf89504E47»\n",
		}), Picture{Data: []byte("\x89PNG"), Ext: "png"}},
		"a file copied on macOS": {desktop("darwin", nil, map[string]string{
			"osascript -e POSIX path of (the clipboard as «class furl»)": "/Users/me/cat.jpeg\n",
		}), Picture{Path: "/Users/me/cat.jpeg"}},
		"a file that is no picture on macOS": {desktop("darwin", nil, map[string]string{
			"osascript -e POSIX path of (the clipboard as «class furl»)": "/Users/me/notes.txt\n",
			"osascript -e the clipboard as «class PNGf»":                 "«data PNGf00»",
		}), Picture{Data: []byte{0}, Ext: "png"}},
	} {
		got, err := tc.reader.Picture(ctx)
		if err != nil || got.Path != tc.want.Path || string(got.Data) != string(tc.want.Data) || got.Ext != tc.want.Ext {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
}

// A clipboard without a picture, or one no tool reads, has none.
func TestNoPicture(t *testing.T) {
	ctx := context.Background()
	for name, r := range map[string]Reader{
		"no desktop":           desktop("linux", nil, map[string]string{"wl-paste --list-types": "image/png"}),
		"no tool":              desktop("linux", wayland, nil),
		"text alone":           desktop("linux", wayland, map[string]string{"wl-paste --list-types": "text/plain\nimage/svg+xml\n"}),
		"a picture not read":   desktop("linux", x11, map[string]string{"xclip -selection clipboard -t TARGETS -o": "image/png"}),
		"a picture of nothing": desktop("linux", wayland, map[string]string{"wl-paste --list-types": "image/png", "wl-paste --no-newline --type image/png": ""}),
		"text on macOS":        desktop("darwin", nil, nil),
		"no hex on macOS":      desktop("darwin", nil, map[string]string{"osascript -e the clipboard as «class PNGf»": "«data PNGfxyz»"}),
		"nothing on macOS":     desktop("darwin", nil, map[string]string{"osascript -e the clipboard as «class PNGf»": "«data PNGf»"}),
		"x11 beside macOS":     desktop("darwin", x11, map[string]string{"xclip -selection clipboard -t TARGETS -o": "image/png", "xclip -selection clipboard -t image/png -o": "PNG"}),
	} {
		if got, err := r.Picture(ctx); !errors.Is(err, ErrNone) || got.Path != "" || got.Data != nil {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
}

// The Reader of the system runs the tools of this system.
func TestSystem(t *testing.T) {
	r := System(func(name string) string { return "of " + name })
	if r.GOOS != runtime.GOOS || r.Getenv("X") != "of X" {
		t.Fatalf("reader %+v", r)
	}
	out, err := r.Run(context.Background(), "sh", "-c", "printf '%s' \"$1\"", "sh", "a b")
	if err != nil || string(out) != "a b" {
		t.Fatalf("ran %q, %v", out, err)
	}
	if _, err := r.Run(context.Background(), "ouro-no-such-tool"); err == nil {
		t.Fatal("a tool that is not there")
	}
}

// A picture is told by the extension of its file, and a URI of a file is its
// path.
func TestNames(t *testing.T) {
	for name, want := range map[string]bool{"a.png": true, "/x/B.JPEG": true, "c.svg": true, "notes.txt": false, "png": false, "": false} {
		if IsPicture(name) != want {
			t.Errorf("IsPicture(%q) = %v", name, !want)
		}
	}
	for uri, want := range map[string]string{
		"file:///home/me/my%20cat.png": "/home/me/my cat.png",
		"/home/me/cat.png":             "/home/me/cat.png",
		"file://%zz":                   "file://%zz",
		"file://host":                  "file://host",
		"https://example.org/cat.png":  "https://example.org/cat.png",
	} {
		if got := Local(uri); got != want {
			t.Errorf("Local(%q) = %q", uri, got)
		}
	}
	if firstLine("# only\n\n") != "" {
		t.Error("a list of comments has no line")
	}
}
