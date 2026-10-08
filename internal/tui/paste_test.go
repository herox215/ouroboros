package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// files makes the files named in a new directory, and a directory "dir.png",
// and returns the directory.
func files(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.png"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The files of pictures a terminal pastes, as when they are dropped on it,
// are typed as their paths in double quotes, however the terminal wrote
// them. Anything else is pasted as it is, and nothing is sent.
func TestPastedPicturesAreTypedAsPaths(t *testing.T) {
	dir := files(t, "cat.png", "my cat.JPG", `the "odd" one.gif`, `back\slash.webp`, "notes.txt")
	in := func(name string) string { return filepath.Join(dir, name) }
	q := func(names ...string) string {
		var paths []string
		for _, name := range names {
			paths = append(paths, in(name))
		}
		return quoted(paths)
	}
	for pasted, want := range map[string]string{
		in("cat.png"):                                                q("cat.png"),
		"  " + in("cat.png") + "\n":                                  q("cat.png"),
		in("my cat.JPG"):                                             q("my cat.JPG"), // as it is, spaces and all
		"'" + in("my cat.JPG") + "'":                                 q("my cat.JPG"),
		`"` + in("my cat.JPG") + `"`:                                 q("my cat.JPG"),
		strings.ReplaceAll(in("my cat.JPG"), " ", `\ `):              q("my cat.JPG"),
		"file://" + strings.ReplaceAll(in("my cat.JPG"), " ", "%20"): q("my cat.JPG"),
		in("cat.png") + " '" + in("my cat.JPG") + "'\t" + in("cat.png") + "\n": q("cat.png", "my cat.JPG", "cat.png"),
		"'" + in(`the "odd" one.gif`) + "'":                                    q(`the "odd" one.gif`),
		`"` + strings.ReplaceAll(in(`the "odd" one.gif`), `"`, `\"`) + `"`:     q(`the "odd" one.gif`),
		"'" + in(`back\slash.webp`) + "'":                                      q(`back\slash.webp`),

		"look at " + in("cat.png"):            "look at " + in("cat.png"),
		in("cat.png") + " " + in("notes.txt"): in("cat.png") + " " + in("notes.txt"),
		in("notes.txt"):                       in("notes.txt"),
		in("gone.png"):                        in("gone.png"),
		in("dir.png"):                         in("dir.png"),
		"cat.png":                             "cat.png",
		"'" + in("cat.png"):                   "'" + in("cat.png"),
		in("cat.png") + `\`:                   in("cat.png") + `\`,
		"''":                                  "''",
		"hello":                               "hello",
	} {
		m, b := newModel(t)
		m.input.SetValue("see ")
		if _, cmd := m.Update(tea.PasteMsg{Content: pasted}); cmd != nil {
			t.Errorf("%q: pasting runs nothing", pasted)
		}
		if got := m.input.Value(); got != "see "+want {
			t.Errorf("%q typed %q, want %q", pasted, got, "see "+want)
		}
		if len(b.messages()) != 0 || m.chat.pending {
			t.Errorf("%q: nothing is sent by pasting", pasted)
		}
	}
	if got := q("cat.png", `the "odd" one.gif`, `back\slash.webp`); got != `"`+in("cat.png")+`" "`+dir+`/the \"odd\" one.gif" "`+dir+`/back\\slash.webp" ` {
		t.Fatalf("quoted %s", got)
	}
}

// ctrlV presses ctrl+v with a clipboard that holds the picture p, or none
// for err, and the text, and hands the UI what was read.
func ctrlV(t *testing.T, m *Model, p plugin.Picture, err error, text string) {
	t.Helper()
	wasPicture, wasText := readPicture, readClipboard
	t.Cleanup(func() { readPicture, readClipboard = wasPicture, wasText })
	readPicture = func(context.Context) (plugin.Picture, error) { return p, err }
	readClipboard = func() (string, error) { return text, nil }
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+v reads the clipboard")
	}
	m.Update(cmd())
}

// ctrl+v pastes the picture on the clipboard of the system: one that is no
// file is kept as one by the node, and its path is typed. Without a picture
// the text of the clipboard is pasted, as the input did before.
func TestCtrlVPastesAPicture(t *testing.T) {
	m, b := newModel(t)
	m.input.SetValue("see ")
	ctrlV(t, m, plugin.Picture{Data: []byte("PNG"), Ext: "png"}, nil, "not this")
	if m.input.Value() != `see "/work/pasted/1.png" ` || !slices.Equal(b.pictures, []string{"PNG"}) || m.notice != "" {
		t.Fatalf("input %q, kept %q, notice %q", m.input.Value(), b.pictures, m.notice)
	}

	// A file that was copied is there already.
	ctrlV(t, m, plugin.Picture{Path: `/home/me/my "cat".png`}, nil, "")
	if m.input.Value() != `see "/work/pasted/1.png" "/home/me/my \"cat\".png" ` || len(b.pictures) != 1 {
		t.Fatalf("input %q, kept %q", m.input.Value(), b.pictures)
	}

	// No picture: the text, a file of a picture in it as its path.
	dir := files(t, "cat.png")
	m.input.Reset()
	ctrlV(t, m, plugin.Picture{}, plugin.ErrNoPicture, "hello ")
	ctrlV(t, m, plugin.Picture{}, plugin.ErrNoPicture, " \n")
	ctrlV(t, m, plugin.Picture{}, plugin.ErrNoPicture, filepath.Join(dir, "cat.png"))
	if m.input.Value() != `hello "`+filepath.Join(dir, "cat.png")+`" ` || len(b.pictures) != 1 {
		t.Fatalf("input %q", m.input.Value())
	}

	// A picture that cannot be read or kept is said, and nothing is typed.
	m.input.Reset()
	ctrlV(t, m, plugin.Picture{}, errors.New("the clipboard is locked"), "not this")
	if m.input.Value() != "" || m.notice != "the picture was not pasted: the clipboard is locked" || !m.noticeErr {
		t.Fatalf("input %q, notice %q", m.input.Value(), m.notice)
	}
	b.fail = map[string]error{"picture": errors.New("the disk is full")}
	ctrlV(t, m, plugin.Picture{Data: []byte("PNG"), Ext: "png"}, nil, "")
	if m.input.Value() != "" || m.notice != "the picture was not pasted: the disk is full" {
		t.Fatalf("input %q, notice %q", m.input.Value(), m.notice)
	}
	if len(b.messages()) != 0 {
		t.Fatal("nothing is sent by pasting")
	}
}

// A terminal pastes a picture that is no file as nothing: then the
// clipboard is asked for it.
func TestEmptyPasteAsksTheClipboard(t *testing.T) {
	m, b := newModel(t)
	wasPicture := readPicture
	defer func() { readPicture = wasPicture }()
	readPicture = func(context.Context) (plugin.Picture, error) {
		return plugin.Picture{Data: []byte("JPEG"), Ext: "jpg"}, nil
	}
	_, cmd := m.Update(tea.PasteMsg{Content: " \n"})
	if cmd == nil {
		t.Fatal("an empty paste asks the clipboard")
	}
	m.Update(cmd())
	if m.input.Value() != `"/work/pasted/1.jpg" ` || !slices.Equal(b.pictures, []string{"JPEG"}) {
		t.Fatalf("input %q, kept %q", m.input.Value(), b.pictures)
	}
}

// The clipboard of the system is read with the tools of its desktop, which
// has none here.
func TestTheClipboardOfTheSystem(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("PATH", t.TempDir())
	if p, err := readPicture(context.Background()); !errors.Is(err, plugin.ErrNoPicture) || p.Path != "" {
		t.Fatalf("picture %+v, %v", p, err)
	}
}

// A line is split as a shell splits it.
func TestWords(t *testing.T) {
	for text, want := range map[string][]string{
		`a b`:          {"a", "b"},
		" a\t\nb\r ":   {"a", "b"},
		`'a b' "c d"`:  {"a b", "c d"},
		`a\ b c\\d`:    {"a b", `c\d`},
		`'a\b' "a\"b"`: {`a\b`, `a"b`},
		`a'b c'd`:      {"ab cd"},
		`'' x`:         {"", "x"},
		`"it's"`:       {"it's"},
		``:             nil,
	} {
		if got, ok := words(text); !ok || !slices.Equal(got, want) {
			t.Errorf("words(%q) = %q, %v", text, got, ok)
		}
	}
	for _, text := range []string{`'a`, `"a`, `a\`, `"a\"`} {
		if _, ok := words(text); ok {
			t.Errorf("words(%q) should not end", text)
		}
	}
}
