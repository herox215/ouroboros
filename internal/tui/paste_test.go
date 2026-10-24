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
	"github.com/charmbracelet/x/ansi"

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
// are typed as their labels, however the terminal wrote them, and the
// conversation keeps their paths: the same file has the same label.
// Anything else is pasted as it is, and nothing is sent.
func TestPastedPicturesAreTypedAsLabels(t *testing.T) {
	dir := files(t, "cat.png", "my cat.JPG", `the "odd" one.gif`, `back\slash.webp`, "notes.txt")
	in := func(name string) string { return filepath.Join(dir, name) }
	type typed struct {
		text  string
		paths []string // of the labels, by their number
	}
	one := func(name string) typed { return typed{"[Image_1] ", []string{in(name)}} }
	as := func(text string) typed { return typed{text, nil} }
	for pasted, want := range map[string]typed{
		in("cat.png"):                                                one("cat.png"),
		"  " + in("cat.png") + "\n":                                  one("cat.png"),
		in("my cat.JPG"):                                             one("my cat.JPG"), // as it is, spaces and all
		"'" + in("my cat.JPG") + "'":                                 one("my cat.JPG"),
		`"` + in("my cat.JPG") + `"`:                                 one("my cat.JPG"),
		strings.ReplaceAll(in("my cat.JPG"), " ", `\ `):              one("my cat.JPG"),
		"file://" + strings.ReplaceAll(in("my cat.JPG"), " ", "%20"): one("my cat.JPG"),
		in("cat.png") + " '" + in("my cat.JPG") + "'\t" + in("cat.png") + "\n": {"[Image_1] [Image_2] [Image_1] ", []string{in("cat.png"), in("my cat.JPG")}},
		"'" + in(`the "odd" one.gif`) + "'":                                    one(`the "odd" one.gif`),
		`"` + strings.ReplaceAll(in(`the "odd" one.gif`), `"`, `\"`) + `"`:     one(`the "odd" one.gif`),
		"'" + in(`back\slash.webp`) + "'":                                      one(`back\slash.webp`),

		"look at " + in("cat.png"):            as("look at " + in("cat.png")),
		in("cat.png") + " " + in("notes.txt"): {"[Image_1] [File_1] ", []string{in("cat.png")}},
		in("notes.txt"):                       {"[File_1] ", nil},
		in("gone.png"):                        as(in("gone.png")),
		in("dir.png"):                         as(in("dir.png")),
		"cat.png":                             as("cat.png"),
		"'" + in("cat.png"):                   as("'" + in("cat.png")),
		in("cat.png") + `\`:                   as(in("cat.png") + `\`),
		"''":                                  as("''"),
		"hello":                               as("hello"),
	} {
		m, b := newModel(t)
		m.input.SetValue("see ")
		if _, cmd := m.Update(tea.PasteMsg{Content: pasted}); cmd != nil {
			t.Errorf("%q: pasting runs nothing", pasted)
		}
		if got := m.input.Value(); got != "see "+want.text || !slices.Equal(m.chat.images, want.paths) {
			t.Errorf("%q typed %q with %q, want %q with %q", pasted, got, m.chat.images, "see "+want.text, want.paths)
		}
		if len(b.messages()) != 0 || m.chat.pending {
			t.Errorf("%q: nothing is sent by pasting", pasted)
		}
	}
	if got := quote(in(`the "odd" one.gif`)) + quote(in(`back\slash.webp`)); got != `"`+dir+`/the \"odd\" one.gif""`+dir+`/back\\slash.webp"` {
		t.Fatalf("quoted %s", got)
	}
}

// What is sent has the paths of the pictures in place of their labels; a
// label without a picture stays. The chat shows the labels again, colored
// as in the input, and so does a message that comes back into the input.
func TestLabelsAreSentAsPaths(t *testing.T) {
	m, b := newModel(t)
	dir := files(t, "cat.png", `my "dog".png`)
	m.Update(tea.PasteMsg{Content: filepath.Join(dir, "cat.png")})
	m.input.InsertString("and ")
	m.Update(tea.PasteMsg{Content: "'" + filepath.Join(dir, `my "dog".png`) + "'"})
	m.input.InsertString("but not [Image_3] [Image_0] [Image_99999999999999999999]")
	if !strings.Contains(m.chatScreen(), imageStyle.Render("[Image_2]")) {
		t.Error("the input should color the labels")
	}
	answered(t, m, press(m, "enter"))
	cat, dog := quote(filepath.Join(dir, "cat.png")), quote(filepath.Join(dir, `my "dog".png`))
	want := cat + " and " + dog + " but not [Image_3] [Image_0] [Image_99999999999999999999]"
	if got := b.messages(); len(got) != 1 || got[0].text != want {
		t.Fatalf("sent %q, want %q", got, want)
	}
	m.chat.queued = []string{"queued " + dog}
	m.renderHistory(true)
	shown := m.history.View()
	if !strings.Contains(shown, imageStyle.Render("[Image_1]")) || !strings.Contains(shown, "queued "+imageStyle.Render("[Image_2]")) ||
		strings.Contains(shown, "cat.png") || !strings.Contains(shown, "but not [Image_3] [Image_0]") {
		t.Fatalf("the chat shows %q", shown)
	}
	if got := m.chat.labelled(want); got != "[Image_1] and [Image_2] but not [Image_3] [Image_0] [Image_99999999999999999999]" {
		t.Fatalf("labelled %q", got)
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
	if m.input.Value() != "see [Image_1] " || !slices.Equal(m.chat.images, []string{"/work/pasted/1.png"}) || !slices.Equal(b.pictures, []string{"PNG"}) || m.notice != "" {
		t.Fatalf("input %q, kept %q, notice %q", m.input.Value(), b.pictures, m.notice)
	}

	// A file that was copied is there already.
	ctrlV(t, m, plugin.Picture{Path: `/home/me/my "cat".png`}, nil, "")
	if m.input.Value() != "see [Image_1] [Image_2] " || m.chat.images[1] != `/home/me/my "cat".png` || len(b.pictures) != 1 {
		t.Fatalf("input %q, kept %q", m.input.Value(), b.pictures)
	}

	// No picture: the text, a file of a picture in it as its path.
	dir := files(t, "cat.png")
	m.input.Reset()
	ctrlV(t, m, plugin.Picture{}, plugin.ErrNoPicture, "hello ")
	ctrlV(t, m, plugin.Picture{}, plugin.ErrNoPicture, " \n")
	ctrlV(t, m, plugin.Picture{}, plugin.ErrNoPicture, filepath.Join(dir, "cat.png"))
	if m.input.Value() != "hello [Image_3] " || m.chat.images[2] != filepath.Join(dir, "cat.png") || len(b.pictures) != 1 {
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
	if m.input.Value() != "[Image_1] " || !slices.Equal(m.chat.images, []string{"/work/pasted/1.jpg"}) || !slices.Equal(b.pictures, []string{"JPEG"}) {
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

// A message the node did not take comes back into the input with the labels
// of its pictures, as it was typed.
func TestRefusedMessageKeepsItsLabels(t *testing.T) {
	m, b := newModel(t)
	b.fail = map[string]error{"ask": errors.New("the node is busy")}
	dir := files(t, "cat.png")
	m.Update(tea.PasteMsg{Content: filepath.Join(dir, "cat.png")})
	m.input.InsertString("look")
	drive(t, m, press(m, "enter"), func() bool { return m.noticeErr })
	if got := m.input.Value(); got != "[Image_1] look" {
		t.Fatalf("input %q", got)
	}
}

// The line below an answer says how large the pictures the agent read were
// shown, each once: a pasted one by its label, in its color, any other by
// the name of its file.
func TestSummaryShowsThePictures(t *testing.T) {
	m, _ := newModel(t)
	m.chat.images = []string{`/tmp/my "cat".png`}
	cat := `reading /tmp/my "cat".png ✓ 48ms · picture "/tmp/my \"cat\".png" 768×384 of 2400×1200 (effort low)`
	m.chat.add(entry{mine: true, text: "look", at: m.now()})
	m.chat.add(entry{at: m.now(), calls: []call{
		{text: cat, done: true},
		{text: "reading d.go ✓ 1s", done: true},
		{text: cat, done: true},
		{text: `reading /tmp/a\b.png ✓ 9ms · picture "/tmp/a\\b.png" 10×10`, done: true},
		{text: `reading /tmp/[Image_9].png ✓ 9ms · picture "/tmp/[Image_9].png" 5×5`, done: true},
	}})
	m.chat.add(entry{text: "a cat", at: m.now()})
	m.renderHistory(true)
	want := "↳ Used 5 tools · [Image_1] 768×384 of 2400×1200 (effort low) · a\\b.png 10×10 · [Image_9].png"
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 30}) // a line wide enough for all of it
	shown := m.history.View()
	if !strings.Contains(ansi.Strip(shown), want) || !strings.Contains(shown, imageStyle.Render("[Image_1]")) || strings.Contains(shown, imageStyle.Render("[Image_9]")) {
		t.Fatalf("the chat shows %q, want %q", ansi.Strip(shown), want)
	}
}

// Every label of a picture there is takes its color in a muted text; one of
// a picture there is not stays muted, and so does what follows it.
func TestMutedColorsEveryLabel(t *testing.T) {
	c := &conversation{images: []string{"a.png", "b.png"}}
	got := c.muted("see [Image_9] and [Image_1] or [Image_2].")
	want := mutedStyle.Render("see [Image_9] and ") + imageStyle.Render("[Image_1]") +
		mutedStyle.Render(" or ") + imageStyle.Render("[Image_2]") + mutedStyle.Render(".")
	if got != want {
		t.Fatalf("muted %q, want %q", got, want)
	}
}
