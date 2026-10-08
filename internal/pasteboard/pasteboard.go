// Package pasteboard reads a picture from the clipboard of the system, for
// the chat to paste: the terminal hands a program the text of the clipboard
// only. It asks the tools of the desktop: wl-paste on Wayland, xclip on X11
// and osascript on macOS. Without one of them, or on another system, the
// clipboard has no picture to read.
package pasteboard

import (
	"context"
	"encoding/hex"
	"errors"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// ErrNone means that the clipboard holds no picture, or none that can be
// read here.
var ErrNone = errors.New("no picture on the clipboard")

// Picture is a picture of the clipboard: the file it is, if a file was
// copied, as in a file manager, or else its data, as of a screenshot.
type Picture struct {
	Path string // of a file that was copied; "" for data
	Data []byte
	Ext  string // of Data, like "png", without the dot
}

// kinds are the pictures the clipboard is asked for, by their media type and
// the extension of their file, the first that is there.
var kinds = []struct{ media, ext string }{
	{"image/png", "png"}, {"image/jpeg", "jpg"}, {"image/webp", "webp"}, {"image/gif", "gif"}, {"image/bmp", "bmp"},
}

// extensions are those of the files that are pictures.
var extensions = []string{".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp", ".tif", ".tiff", ".heic", ".avif", ".svg"}

// IsPicture reports whether a file named path is a picture, by its extension.
func IsPicture(path string) bool {
	return slices.Contains(extensions, strings.ToLower(filepath.Ext(path)))
}

// Reader reads the clipboard with the tools of a desktop.
type Reader struct {
	GOOS   string
	Getenv func(string) string
	// Run runs a tool and returns what it wrote.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// System is the Reader of the system ouro runs on.
func System(getenv func(string) string) Reader {
	return Reader{GOOS: runtime.GOOS, Getenv: getenv, Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, name, args...).Output()
	}}
}

// Picture returns the picture on the clipboard: ErrNone if it holds none.
func (r Reader) Picture(ctx context.Context) (Picture, error) {
	switch {
	case r.GOOS == "darwin":
		return r.mac(ctx)
	case r.Getenv("WAYLAND_DISPLAY") != "":
		return r.typed(ctx, []string{"wl-paste", "--list-types"}, func(media string) []string {
			return []string{"wl-paste", "--no-newline", "--type", media}
		})
	case r.Getenv("DISPLAY") != "":
		return r.typed(ctx, []string{"xclip", "-selection", "clipboard", "-t", "TARGETS", "-o"}, func(media string) []string {
			return []string{"xclip", "-selection", "clipboard", "-t", media, "-o"}
		})
	}
	return Picture{}, ErrNone
}

// typed reads the clipboard of a desktop that lists what it holds by media
// types: list is the command that lists them, and read the one that reads
// one. A file that was copied comes first, as the list of its URI.
func (r Reader) typed(ctx context.Context, list []string, read func(media string) []string) (Picture, error) {
	out, err := r.Run(ctx, list[0], list[1:]...)
	if err != nil {
		return Picture{}, ErrNone // no such tool, or nothing on the clipboard
	}
	offered := strings.Fields(string(out))
	if slices.Contains(offered, "text/uri-list") {
		cmd := read("text/uri-list")
		if uris, err := r.Run(ctx, cmd[0], cmd[1:]...); err == nil {
			if path := Local(strings.TrimSpace(firstLine(string(uris)))); IsPicture(path) {
				return Picture{Path: path}, nil
			}
		}
	}
	for _, k := range kinds {
		if !slices.Contains(offered, k.media) {
			continue
		}
		cmd := read(k.media)
		data, err := r.Run(ctx, cmd[0], cmd[1:]...)
		if err != nil || len(data) == 0 {
			return Picture{}, ErrNone
		}
		return Picture{Data: data, Ext: k.ext}, nil
	}
	return Picture{}, ErrNone
}

// mac reads the clipboard of macOS: a file that was copied in the Finder, or
// else a picture, which osascript writes as «data PNGf…» in hex.
func (r Reader) mac(ctx context.Context) (Picture, error) {
	if out, err := r.Run(ctx, "osascript", "-e", "POSIX path of (the clipboard as «class furl»)"); err == nil {
		if path := strings.TrimSpace(string(out)); IsPicture(path) {
			return Picture{Path: path}, nil
		}
	}
	out, err := r.Run(ctx, "osascript", "-e", "the clipboard as «class PNGf»")
	if err != nil {
		return Picture{}, ErrNone
	}
	_, written, _ := strings.Cut(string(out), "PNGf")
	data, err := hex.DecodeString(strings.TrimRight(strings.TrimSpace(written), "»"))
	if err != nil || len(data) == 0 {
		return Picture{}, ErrNone
	}
	return Picture{Data: data, Ext: "png"}, nil
}

// firstLine is the first line of text that is no comment of a list of URIs.
func firstLine(text string) string {
	for line := range strings.Lines(text) {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

// Local is the path of the file a URI of the scheme file names; what is no
// such URI is the path as it is.
func Local(name string) string {
	if !strings.HasPrefix(name, "file://") {
		return name
	}
	u, err := url.Parse(name)
	if err != nil || u.Path == "" {
		return name
	}
	return u.Path
}
