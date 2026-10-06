package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"

	"ouroboros/internal/a2a"
	"ouroboros/internal/guard"
)

var errTest = errors.New("test error")

// world is a machine with a user, a node and the tools for its files.
type world struct {
	t         *testing.T
	home      string // of the user
	node      string // the directory of the node
	guard     *guard.Guard
	protected []string
	failing   error // what reading the configuration fails with
}

func newWorld(t *testing.T) *world {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, home: filepath.Join(root, "home"), protected: []string{"~/.ssh"}}
	w.node = filepath.Join(w.home, ".config", "ouroboros")
	t.Setenv("HOME", w.home)
	w.file("~/.config/ouroboros/config.json", "{}")
	w.file("~/.config/ouroboros/contacts.json", "{}")
	w.file("~/.ssh/id_ed25519", "key")
	w.guard = &guard.Guard{
		Home:      w.node,
		Secrets:   []string{w.node + "/config.json", w.node + "/config.json.lock"},
		Protected: func() ([]string, error) { return w.protected, w.failing },
	}
	return w
}

// path is where a path below "~" is.
func (w *world) path(path string) string { return w.home + strings.TrimPrefix(path, "~") }

// file writes a file with its directories.
func (w *world) file(path, content string) {
	w.t.Helper()
	if err := os.MkdirAll(filepath.Dir(w.path(path)), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(w.path(path), []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

// content is what the file at path holds, "<none>" if there is none.
func (w *world) content(path string) string {
	b, err := os.ReadFile(w.path(path))
	if err != nil {
		return "<none>"
	}
	return string(b)
}

// mode sets the mode of a file or directory until the test ends.
func (w *world) mode(path string, mode os.FileMode) {
	w.t.Helper()
	if err := os.Chmod(w.path(path), mode); err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { os.Chmod(w.path(path), 0o755) })
}

// who runs a tool: the owner or a peer, with the settings of the tool.
type who struct {
	settings string
	peer     bool
}

var owner = who{settings: `{}`}

func peer(settings string) who { return who{settings, true} }

// run runs the tool called name for by with the arguments args.
func (w *world) run(by who, name string, args any) (string, error) {
	w.t.Helper()
	for _, tool := range tools(w.guard) {
		if tool.Name != name {
			continue
		}
		tool, err := tool.Configure(json.RawMessage(by.settings))
		if err != nil {
			w.t.Fatalf("settings %s: %v", by.settings, err)
		}
		raw, ok := args.(string)
		if !ok {
			b, _ := json.Marshal(args)
			raw = string(b)
		}
		ctx := context.Background()
		if by.peer {
			ctx = a2a.WithOrigin(ctx, a2a.Origin{PublicKey: "peer"})
		}
		return tool.Run(ctx, json.RawMessage(raw))
	}
	w.t.Fatalf("no tool %s", name)
	return "", nil
}

// refused fails the test unless err is the refusal of rule.
func refused(t *testing.T, got string, err error, rule string) {
	t.Helper()
	var refusal *guard.Refusal
	if !errors.As(err, &refusal) || got != "" || !strings.Contains(refusal.Rule, rule) {
		t.Errorf("got %q, %v; want a refusal by %q", got, err, rule)
	}
}

// fails fails the test unless err says want.
func fails(t *testing.T, got string, err error, want string) {
	t.Helper()
	if err == nil || got != "" || !strings.Contains(err.Error(), want) {
		t.Errorf("got %q, %v; want the error %q", got, err, want)
	}
}

func lines(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func TestRead(t *testing.T) {
	w := newWorld(t)
	w.file("~/notes.md", "one\ntwo\nthree\n")
	w.file("~/open.txt", "one\ntwo")
	w.file("~/empty", "")
	w.file("~/long", lines(1, maxReadLines+1))

	for _, tt := range []struct {
		args readArgs
		want string
	}{
		{readArgs{Path: "~/notes.md"}, "one\ntwo\nthree\n"},
		{readArgs{Path: w.home + "/notes.md"}, "one\ntwo\nthree\n"},
		{readArgs{Path: "~/open.txt"}, "one\ntwo"},
		{readArgs{Path: "~/empty"}, "(the file is empty)"},
		{readArgs{Path: "~/empty", Offset: 7}, "(the file is empty)"},
		{readArgs{Path: "~/notes.md", Offset: 1}, "one\ntwo\nthree\n"},
		{readArgs{Path: "~/notes.md", Offset: -3}, "one\ntwo\nthree\n"},
		{readArgs{Path: "~/notes.md", Offset: 2}, "two\nthree\n[these were the lines 2 to 3 of 3, the end of the file]"},
		{readArgs{Path: "~/notes.md", Offset: 3}, "three\n[these were the lines 3 to 3 of 3, the end of the file]"},
		{readArgs{Path: "~/notes.md", Limit: 3}, "one\ntwo\nthree\n"},
		{readArgs{Path: "~/notes.md", Limit: -1}, "one\ntwo\nthree\n"},
		{readArgs{Path: "~/notes.md", Limit: 2}, "one\ntwo\n[these were the lines 1 to 2 of 3: continue with offset 3]"},
		{readArgs{Path: "~/notes.md", Limit: 1}, "one\n[these were the lines 1 to 1 of 3: continue with offset 2]"},
		{readArgs{Path: "~/notes.md", Offset: 2, Limit: 1}, "two\n[these were the lines 2 to 2 of 3: continue with offset 3]"},
		{readArgs{Path: "~/open.txt", Offset: 2, Limit: 1}, "two\n[these were the lines 2 to 2 of 2, the end of the file]"},
		{readArgs{Path: "~/open.txt", Limit: 1}, "one\n[these were the lines 1 to 1 of 2: continue with offset 2]"},
		{readArgs{Path: "~/long"}, lines(1, maxReadLines) + "[these were the lines 1 to 2000 of 2001: continue with offset 2001]"},
		{readArgs{Path: "~/long", Limit: maxReadLines + 5}, lines(1, maxReadLines) + "[these were the lines 1 to 2000 of 2001: continue with offset 2001]"},
		{readArgs{Path: "~/long", Offset: 2, Limit: maxReadLines}, lines(2, maxReadLines+1) + "[these were the lines 2 to 2001 of 2001, the end of the file]"},
		{readArgs{Path: "~/long", Offset: 2001}, "line 2001\n[these were the lines 2001 to 2001 of 2001, the end of the file]"},
		{readArgs{Path: "~/long", Limit: 1}, "line 1\n[these were the lines 1 to 1 of 2001: continue with offset 2]"},
	} {
		if got, err := w.run(owner, Read, tt.args); err != nil || got != tt.want {
			t.Errorf("read(%+v) = %q, %v; want %q", tt.args, got, err, tt.want)
		}
	}

	got, err := w.run(owner, Read, readArgs{Path: "~/notes.md", Offset: 4})
	fails(t, got, err, "~/notes.md has only 3 lines")
	got, err = w.run(owner, Read, readArgs{Path: "~/missing"})
	fails(t, got, err, "no such file")
	got, err = w.run(owner, Read, readArgs{Path: "~"})
	fails(t, got, err, "~ is not a file; a directory is read with list")
	got, err = w.run(owner, Read, `{"path": 1}`)
	fails(t, got, err, "read: invalid arguments")
	got, err = w.run(owner, Read, readArgs{Path: "notes.md"})
	fails(t, got, err, `the path "notes.md" has to be absolute or start with ~`)

	// What never ends is not read.
	if err := syscall.Mkfifo(w.path("~/pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = w.run(owner, Read, readArgs{Path: "~/pipe"})
	fails(t, got, err, "~/pipe is not a file")

	w.file("~/secret", "x")
	w.mode("~/secret", 0)
	got, err = w.run(owner, Read, readArgs{Path: "~/secret"})
	fails(t, got, err, "permission denied")
}

// One call returns at most 256 KiB, in whole lines, and says where to go on.
func TestReadInPieces(t *testing.T) {
	w := newWorld(t)
	line := strings.Repeat("x", 1023) + "\n"
	fits := strings.Repeat(line, maxReadBytes/len(line))
	w.file("~/fits", fits)
	if got, err := w.run(owner, Read, readArgs{Path: "~/fits"}); err != nil || got != fits {
		t.Errorf("a file of exactly the limit: %d bytes, %v", len(got), err)
	}
	w.file("~/more", fits+"last\nvery last\n")
	note := "[these were the lines 1 to 256 of 258: continue with offset 257]"
	if got, err := w.run(owner, Read, readArgs{Path: "~/more"}); err != nil || got != fits+note {
		t.Errorf("a file of more than the limit: %d bytes, %v", len(got), err)
	}
	if got, err := w.run(owner, Read, readArgs{Path: "~/more", Offset: 257}); err != nil || got != "last\nvery last\n[these were the lines 257 to 258 of 258, the end of the file]" {
		t.Errorf("the rest: %q, %v", got, err)
	}

	// A line that fills a call alone is too long, wherever it is.
	w.file("~/wide", "short\n"+strings.Repeat("x", maxReadBytes)+"\nshort\n")
	got, err := w.run(owner, Read, readArgs{Path: "~/wide", Offset: 3})
	fails(t, got, err, "line 2 of ~/wide has 256 KiB or more: too long to read")
	// After the lines returned a long line is only counted.
	w.file("~/widelater", "short\n"+strings.Repeat("x", 2*maxReadBytes)+"\nshort")
	if got, err := w.run(owner, Read, readArgs{Path: "~/widelater", Limit: 1}); err != nil || got != "short\n[these were the lines 1 to 1 of 3: continue with offset 2]" {
		t.Errorf("a long line after those returned: %q, %v", got, err)
	}
	w.file("~/justfits", "short\n"+strings.Repeat("x", maxReadBytes-1)+"\nshort\n")
	if got, err := w.run(owner, Read, readArgs{Path: "~/justfits", Offset: 2, Limit: 1}); err != nil || len(got) < maxReadBytes {
		t.Errorf("a line that just fits: %d bytes, %v", len(got), err)
	}
}

// What is not text is not handed to the LLM.
func TestReadWhatIsNoText(t *testing.T) {
	w := newWorld(t)
	w.file("~/binary", "ELF\x00\x01\n")
	w.file("~/latin1", "gr\xfc\xdfe\n")
	w.file("~/late", "fine\nfine\n\x00\n")
	w.file("~/utf8", "grüße\n")
	for _, path := range []string{"~/binary", "~/latin1", "~/late"} {
		got, err := w.run(owner, Read, readArgs{Path: path})
		fails(t, got, err, path+" is not a text file")
	}
	if got, err := w.run(owner, Read, readArgs{Path: "~/utf8"}); err != nil || got != "grüße\n" {
		t.Errorf("got %q, %v", got, err)
	}

	// A file that cannot be read to its end is an error, not a short file.
	open = func(string) (io.ReadCloser, error) {
		return io.NopCloser(io.MultiReader(strings.NewReader("one\ntwo"), iotest.ErrReader(errTest))), nil
	}
	defer func() { open = func(path string) (io.ReadCloser, error) { return os.Open(path) } }()
	if got, err := w.run(owner, Read, readArgs{Path: "~/utf8"}); !errors.Is(err, errTest) || got != "" {
		t.Errorf("a failing file: %q, %v", got, err)
	}
	// Also when it fails only after the lines returned, which are counted for the note.
	open = func(string) (io.ReadCloser, error) {
		return io.NopCloser(io.MultiReader(strings.NewReader("one\ntwo\n"), iotest.ErrReader(errTest))), nil
	}
	if got, err := w.run(owner, Read, readArgs{Path: "~/utf8", Limit: 1}); !errors.Is(err, errTest) || got != "" {
		t.Errorf("a file failing after the lines returned: %q, %v", got, err)
	}
}

func TestWrite(t *testing.T) {
	w := newWorld(t)
	got, err := w.run(owner, Write, writeArgs{Path: "~/new/dir/notes.md", Content: "hello"})
	if err != nil || got != "created ~/new/dir/notes.md (5 bytes)" || w.content("~/new/dir/notes.md") != "hello" {
		t.Fatalf("a new file: %q, %v", got, err)
	}
	if info, _ := os.Stat(w.path("~/new/dir/notes.md")); info.Mode().Perm() != 0o644 {
		t.Errorf("a new file has the mode %o", info.Mode().Perm())
	}
	if info, _ := os.Stat(w.path("~/new/dir")); info.Mode().Perm() != 0o755 {
		t.Errorf("a new directory has the mode %o", info.Mode().Perm())
	}
	// A file that is replaced keeps its mode, and is replaced in one step.
	w.mode("~/new/dir/notes.md", 0o600)
	before, _ := os.Stat(w.path("~/new/dir/notes.md"))
	got, err = w.run(owner, Write, writeArgs{Path: "~/new/dir/notes.md", Content: ""})
	if err != nil || got != "replaced ~/new/dir/notes.md (0 bytes)" || w.content("~/new/dir/notes.md") != "" {
		t.Fatalf("replacing: %q, %v", got, err)
	}
	after, _ := os.Stat(w.path("~/new/dir/notes.md"))
	if after.Mode().Perm() != 0o600 || os.SameFile(before, after) {
		t.Errorf("replaced in place, or with the mode %o", after.Mode().Perm())
	}
	if entries, _ := os.ReadDir(w.path("~/new/dir")); len(entries) != 1 {
		t.Errorf("writing left %d files behind", len(entries)-1)
	}

	// The limit.
	if got, err := w.run(owner, Write, writeArgs{Path: "~/big", Content: strings.Repeat("x", maxWriteBytes)}); err != nil || !strings.HasPrefix(got, "created") {
		t.Errorf("a file of exactly the limit: %q, %v", got, err)
	}
	got, err = w.run(owner, Write, writeArgs{Path: "~/bigger", Content: strings.Repeat("x", maxWriteBytes+1)})
	fails(t, got, err, "the content has 1048577 bytes: a file is written with at most 1048576")
	if w.content("~/bigger") != "<none>" {
		t.Error("a file over the limit was written")
	}

	// What cannot be written.
	got, err = w.run(owner, Write, `{"path": "~/x", "content": 1}`)
	fails(t, got, err, "write: invalid arguments")
	got, err = w.run(owner, Write, writeArgs{Path: "~/new", Content: "x"})
	fails(t, got, err, "rename")
	got, err = w.run(owner, Write, writeArgs{Path: "~/big/x", Content: "x"})
	fails(t, got, err, "not a directory")
	os.Mkdir(w.path("~/locked"), 0o755)
	w.mode("~/locked", 0o555)
	got, err = w.run(owner, Write, writeArgs{Path: "~/locked/sub/x", Content: "x"})
	fails(t, got, err, "permission denied")
	got, err = w.run(owner, Write, writeArgs{Path: "~/locked/x", Content: "x"})
	fails(t, got, err, "permission denied")
}

// Writing is refused by the same rules for every way to write.
func TestWriteAndEditAreRefused(t *testing.T) {
	w := newWorld(t)
	w.file("~/src/go.mod", "module ouroboros\n\ngo 1.27\n")
	w.file("~/src/internal/a2a/a2a.go", "package a2a\n")
	w.file("~/src/internal/plugins/mine/mine.go", "package mine\n")
	w.file("~/share/inbox/keep/x", "kept")
	w.file("~/share/inbox/x", "old")

	for _, tt := range []struct {
		by   who
		path string
		rule string
	}{
		{owner, "~/.ssh/id_ed25519", `~/.ssh is in "protected"`},
		{owner, "~/.config/ouroboros/config.json", "the secrets of this node"},
		{owner, "~/.config/ouroboros/contacts.json", "the directory of this node"},
		{owner, "~/src/internal/a2a/a2a.go", "source of the core"},
		{owner, "~/src/go.mod", "it decides how ouroboros is built"},
		{who{settings: `{"deny": ["~/share"]}`}, "~/share/inbox/x", `~/share is in "deny" of the tool`},
		{peer(`{}`), "~/share/inbox/x", `no "allow" for peers`},
		{peer(`{"allow": ["~/share/inbox"], "deny": ["~/share/inbox/keep"]}`), "~/share/inbox/keep/x", `~/share/inbox/keep is in "deny"`},
		{peer(`{"allow": ["~/share/inbox"]}`), "~/share/x", `it is not in "allow"`},
	} {
		before := w.content(tt.path)
		got, err := w.run(tt.by, Write, writeArgs{Path: tt.path, Content: "module mine\n"})
		refused(t, got, err, tt.rule)
		got, err = w.run(tt.by, Edit, editArgs{Path: tt.path, Old: "ouroboros", New: "mine"})
		refused(t, got, err, tt.rule)
		if w.content(tt.path) != before {
			t.Errorf("%s was changed", tt.path)
		}
	}

	// What is allowed is done, for a peer too.
	in := peer(`{"allow": ["~/share/inbox"], "deny": ["~/share/inbox/keep"]}`)
	if got, err := w.run(in, Write, writeArgs{Path: "~/share/inbox/x", Content: "new"}); err != nil || w.content("~/share/inbox/x") != "new" {
		t.Errorf("a peer writes where it may: %q, %v", got, err)
	}
	if got, err := w.run(in, Edit, editArgs{Path: "~/share/inbox/x", Old: "new", New: "newer"}); err != nil || w.content("~/share/inbox/x") != "newer" {
		t.Errorf("a peer edits where it may: %q, %v", got, err)
	}
	// A plugin of the checkout is written, its go.mod not even as it is.
	if _, err := w.run(owner, Write, writeArgs{Path: "~/src/internal/plugins/mine/mine.go", Content: "package mine // changed\n"}); err != nil {
		t.Errorf("a plugin: %v", err)
	}
	got, err := w.run(owner, Edit, editArgs{Path: "~/src/go.mod", Old: "go 1.27", New: "go 1.28"})
	refused(t, got, err, "it decides how ouroboros is built")
}

func TestEdit(t *testing.T) {
	w := newWorld(t)
	w.file("~/notes.md", "one two\nthree two\n")
	w.mode("~/notes.md", 0o600)
	before, _ := os.Stat(w.path("~/notes.md"))

	got, err := w.run(owner, Edit, editArgs{Path: "~/notes.md", Old: "three two", New: "3 2"})
	if err != nil || got != "edited ~/notes.md; lines 1 to 2 of 2 are now:\none two\n3 2" || w.content("~/notes.md") != "one two\n3 2\n" {
		t.Fatalf("got %q, %v: %q", got, err, w.content("~/notes.md"))
	}
	after, _ := os.Stat(w.path("~/notes.md"))
	if after.Mode().Perm() != 0o600 || os.SameFile(before, after) {
		t.Errorf("edited in place, or with the mode %o", after.Mode().Perm())
	}
	// Text can be taken out.
	if _, err := w.run(owner, Edit, editArgs{Path: "~/notes.md", Old: "one "}); err != nil || w.content("~/notes.md") != "two\n3 2\n" {
		t.Fatalf("got %v: %q", err, w.content("~/notes.md"))
	}

	got, err = w.run(owner, Edit, editArgs{Path: "~/notes.md", Old: "four", New: "4"})
	fails(t, got, err, `"old" does not occur in ~/notes.md: it has to match the text of the file exactly`)
	got, err = w.run(owner, Edit, editArgs{Path: "~/notes.md", Old: "\n", New: " "})
	fails(t, got, err, `"old" occurs 2 times in ~/notes.md: it has to match exactly once, so take more of the text around it`)
	got, err = w.run(owner, Edit, editArgs{Path: "~/notes.md", New: "x"})
	fails(t, got, err, `edit needs "old", the text to replace`)
	got, err = w.run(owner, Edit, `{"path": "~/notes.md", "old": 1}`)
	fails(t, got, err, "edit: invalid arguments")
	got, err = w.run(owner, Edit, editArgs{Path: "~/missing", Old: "a", New: "b"})
	fails(t, got, err, "no such file")
	got, err = w.run(owner, Edit, editArgs{Path: "~", Old: "a", New: "b"})
	fails(t, got, err, "~ is not a file")
	if w.content("~/notes.md") != "two\n3 2\n" {
		t.Errorf("a failed edit changed the file: %q", w.content("~/notes.md"))
	}

	// What an edit left behind is shown with the lines around it, and no more.
	w.file("~/lines", "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	got, err = w.run(owner, Edit, editArgs{Path: "~/lines", Old: "5\n6\n", New: "five\nsix\nseven\n"})
	if want := "edited ~/lines; lines 2 to 10 of 11 are now:\n2\n3\n4\nfive\nsix\nseven\n7\n8\n9"; err != nil || got != want {
		t.Errorf("got %q, %v, want %q", got, err, want)
	}
	// What was taken out leaves the line it was in, and the end of a file is the end.
	got, err = w.run(owner, Edit, editArgs{Path: "~/lines", Old: "9\n10\n"})
	if want := "edited ~/lines; lines 6 to 9 of 9 are now:\nsix\nseven\n7\n8"; err != nil || got != want {
		t.Errorf("got %q, %v, want %q", got, err, want)
	}
	got, err = w.run(owner, Edit, editArgs{Path: "~/lines", Old: "1\n2\n3\n4\nfive\nsix\nseven\n7\n8\n"})
	if err != nil || got != "edited ~/lines, which is empty now" {
		t.Errorf("got %q, %v", got, err)
	}
	// A long change is cut, with where to go on reading.
	w.file("~/long", "start\n")
	got, err = w.run(owner, Edit, editArgs{Path: "~/long", Old: "start\n", New: strings.Repeat("x\n", maxEditLines+10)})
	if err != nil || !strings.HasPrefix(got, fmt.Sprintf("edited ~/long; lines 1 to %d of %d are now:\n", maxEditLines, maxEditLines+10)) ||
		!strings.HasSuffix(got, fmt.Sprintf("x\n[and more: continue with read at offset %d]", maxEditLines+1)) {
		t.Errorf("got %q, %v", got, err)
	}

	// The limit holds for what is edited and for what comes of it.
	w.file("~/big", "a"+strings.Repeat("x", maxWriteBytes-1))
	if _, err := w.run(owner, Edit, editArgs{Path: "~/big", Old: "a", New: "b"}); err != nil {
		t.Errorf("a file of exactly the limit: %v", err)
	}
	got, err = w.run(owner, Edit, editArgs{Path: "~/big", Old: "b", New: "bb"})
	fails(t, got, err, "~/big would have 1048577 bytes: a file is written with at most 1048576")
	w.file("~/bigger", "a"+strings.Repeat("x", maxWriteBytes))
	got, err = w.run(owner, Edit, editArgs{Path: "~/bigger", Old: "a", New: ""})
	fails(t, got, err, "~/bigger has 1048577 bytes: a file is edited with at most 1048576")

	// What cannot be read or written back.
	w.file("~/locked/x", "a")
	w.mode("~/locked", 0o555)
	got, err = w.run(owner, Edit, editArgs{Path: "~/locked/x", Old: "a", New: "b"})
	fails(t, got, err, "permission denied")
	w.mode("~/notes.md", 0)
	got, err = w.run(owner, Edit, editArgs{Path: "~/notes.md", Old: "two", New: "2"})
	fails(t, got, err, "permission denied")
}

func TestList(t *testing.T) {
	w := newWorld(t)
	w.file("~/notes.md", "hello")
	w.file("~/empty.txt", "")
	w.file("~/share/x", "x")
	os.Symlink("notes.md", w.path("~/link"))
	os.Symlink(w.path("~/.ssh/id_ed25519"), w.path("~/key"))
	os.Symlink("nowhere", w.path("~/broken"))
	os.Symlink("loop", w.path("~/loop"))
	os.Mkdir(w.path("~/none"), 0o755)

	// What is protected is not there for the tool, under any name.
	got, err := w.run(owner, List, listArgs{Path: "~"})
	if want := ".config/\nbroken\nempty.txt (0 bytes)\nlink\nnone/\nnotes.md (5 bytes)\nshare/"; err != nil || got != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
	// Neither are the secrets of the node; the rest of its state is.
	got, err = w.run(owner, List, listArgs{Path: "~/.config/ouroboros"})
	if err != nil || got != "contacts.json (2 bytes)" {
		t.Errorf("the directory of the node: %q, %v", got, err)
	}
	if got, err := w.run(owner, List, listArgs{Path: "~/none"}); err != nil || got != "(nothing to list)" {
		t.Errorf("an empty directory: %q, %v", got, err)
	}
	// A peer sees what it may read.
	got, err = w.run(peer(`{"allow": ["~/share", "~/notes.md"]}`), List, listArgs{Path: "~"})
	refused(t, got, err, `it is not in "allow" of the tool list`)
	if got, err := w.run(peer(`{"allow": ["~"], "deny": ["~/share", "~/none", "~/.config"]}`), List, listArgs{Path: "~"}); err != nil || got != "broken\nempty.txt (0 bytes)\nlink\nnotes.md (5 bytes)" {
		t.Errorf("for a peer: %q, %v", got, err)
	}
	if got, err := w.run(peer(`{"allow": ["~/share"]}`), List, listArgs{Path: "~/share"}); err != nil || got != "x (1 bytes)" {
		t.Errorf("for a peer: %q, %v", got, err)
	}

	got, err = w.run(owner, List, listArgs{Path: "~/.ssh"})
	refused(t, got, err, `~/.ssh is in "protected"`)
	got, err = w.run(owner, List, listArgs{Path: "~/notes.md"})
	fails(t, got, err, "not a directory")
	got, err = w.run(owner, List, listArgs{Path: "~/missing"})
	fails(t, got, err, "no such file")
	got, err = w.run(owner, List, `{"path": []}`)
	fails(t, got, err, "list: invalid arguments")
}

// A directory with many entries is cut, and the tool says so.
func TestListIsCut(t *testing.T) {
	w := newWorld(t)
	for i := range maxListed {
		w.file(fmt.Sprintf("~/many/%04d", i), "")
	}
	w.file("~/many/.hidden/x", "")
	hide := who{settings: `{"deny": ["~/many/.hidden"]}`}
	got, err := w.run(hide, List, listArgs{Path: "~/many"})
	if listed := strings.Split(got, "\n"); err != nil || len(listed) != maxListed || listed[0] != "0000 (0 bytes)" || listed[maxListed-1] != "0999 (0 bytes)" {
		t.Fatalf("exactly as many as fit: %d entries, %v", len(listed), err)
	}
	w.file("~/many/zzzy", "")
	w.file("~/many/zzzz", "")
	got, err = w.run(hide, List, listArgs{Path: "~/many"})
	listed := strings.Split(got, "\n")
	if err != nil || len(listed) != maxListed+1 || listed[maxListed-1] != "0999 (0 bytes)" || listed[maxListed] != "[these are the first 1000 entries, and there are more]" {
		t.Fatalf("one more: %d entries, %v, ends with %q", len(listed), err, listed[len(listed)-1])
	}
}

func TestDelete(t *testing.T) {
	w := newWorld(t)
	w.file("~/notes.md", "hello")
	w.file("~/tree/a/b/c", "c")
	w.file("~/tree/d", "d")
	w.file("~/target/x", "x")
	os.Mkdir(w.path("~/none"), 0o755)
	os.Symlink(w.path("~/target"), w.path("~/link"))

	got, err := w.run(owner, Delete, deleteArgs{Path: "~/notes.md"})
	if err != nil || got != "deleted ~/notes.md" || w.content("~/notes.md") != "<none>" {
		t.Errorf("a file: %q, %v", got, err)
	}
	if got, err := w.run(owner, Delete, deleteArgs{Path: "~/none"}); err != nil || got != "deleted ~/none" {
		t.Errorf("an empty directory: %q, %v", got, err)
	}
	if _, err := os.Stat(w.path("~/none")); err == nil {
		t.Error("the empty directory is still there")
	}
	// A link goes, not what it leads to.
	if _, err := w.run(owner, Delete, deleteArgs{Path: "~/link", Recursive: true}); err != nil || w.content("~/target/x") != "x" {
		t.Errorf("a link: %v", err)
	}
	if _, err := os.Lstat(w.path("~/link")); err == nil {
		t.Error("the link is still there")
	}

	// A directory with content needs the word for it.
	got, err = w.run(owner, Delete, deleteArgs{Path: "~/tree"})
	fails(t, got, err, "~/tree is a directory with content: it is only deleted, with everything in it, when recursive is true")
	got, err = w.run(owner, Delete, `{"path": "~/tree", "recursive": "yes"}`)
	fails(t, got, err, "delete: invalid arguments")
	if w.content("~/tree/a/b/c") != "c" || w.content("~/tree/d") != "d" {
		t.Fatal("a directory was deleted without recursive")
	}
	if got, err := w.run(owner, Delete, deleteArgs{Path: "~/tree", Recursive: true}); err != nil || got != "deleted ~/tree" {
		t.Errorf("recursive: %q, %v", got, err)
	}
	if _, err := os.Stat(w.path("~/tree")); err == nil {
		t.Error("the directory is still there")
	}

	got, err = w.run(owner, Delete, deleteArgs{Path: "~/missing"})
	fails(t, got, err, "no such file")
	got, err = w.run(owner, Delete, deleteArgs{Path: "~", Recursive: true})
	refused(t, got, err, "the root of the file system and the home directory are never deleted")
	// Not asked for with everything below it: a test is no place to find out what a mistake here deletes.
	got, err = w.run(owner, Delete, deleteArgs{Path: "/"})
	refused(t, got, err, "the root of the file system and the home directory are never deleted")
	if w.content("~/target/x") != "x" {
		t.Fatal("the home directory was deleted")
	}

	// What cannot be looked into or taken away.
	w.file("~/closed/x", "x")
	w.mode("~/closed", 0)
	got, err = w.run(owner, Delete, deleteArgs{Path: "~/closed", Recursive: true})
	fails(t, got, err, "permission denied")
	w.file("~/fixed/sub/x", "x")
	w.mode("~/fixed", 0o555)
	got, err = w.run(owner, Delete, deleteArgs{Path: "~/fixed/sub", Recursive: true})
	fails(t, got, err, "permission denied")
}

// Deleting with everything below stops before anything it may not delete.
func TestDeleteRecursiveKeepsWhatItMayNot(t *testing.T) {
	w := newWorld(t)
	tree := func() {
		w.file("~/tree/a/x", "x")
		w.file("~/tree/b/keep/y", "y")
		w.file("~/tree/z", "z")
	}
	intact := func(what string) {
		t.Helper()
		if w.content("~/tree/a/x") != "x" || w.content("~/tree/b/keep/y") != "y" || w.content("~/tree/z") != "z" {
			t.Fatalf("%s: something was deleted", what)
		}
	}
	tree()

	w.protected = []string{"~/tree/b/keep/y"}
	got, err := w.run(owner, Delete, deleteArgs{Path: "~/tree", Recursive: true})
	fails(t, got, err, "nothing was deleted: deleting "+w.path("~/tree/b/keep/y")+` is refused: ~/tree/b/keep/y is in "protected"`)
	refused(t, got, err, `"protected"`)
	intact("across a protected path")
	w.protected = nil

	got, err = w.run(who{settings: `{"deny": ["~/tree/b/keep"]}`}, Delete, deleteArgs{Path: "~/tree", Recursive: true})
	refused(t, got, err, `~/tree/b/keep is in "deny"`)
	intact("across a denied path")
	got, err = w.run(peer(`{"allow": ["~/tree/a"]}`), Delete, deleteArgs{Path: "~/tree", Recursive: true})
	refused(t, got, err, `it is not in "allow"`)
	intact("a peer outside of what it may")

	// A directory that cannot be looked into keeps the rest.
	w.mode("~/tree/b/keep", 0)
	got, err = w.run(owner, Delete, deleteArgs{Path: "~/tree", Recursive: true})
	fails(t, got, err, "nothing was deleted: ")
	w.mode("~/tree/b/keep", 0o755)
	intact("across a closed directory")

	// The node and the core of a checkout are below what is deleted.
	got, err = w.run(owner, Delete, deleteArgs{Path: "~/.config", Recursive: true})
	refused(t, got, err, "the directory of this node")
	if w.content("~/.config/ouroboros/contacts.json") != "{}" {
		t.Fatal("the state of the node was deleted")
	}
	w.file("~/tree/go.mod", "module ouroboros\n")
	w.file("~/tree/internal/kernel/kernel.go", "package kernel\n")
	got, err = w.run(owner, Delete, deleteArgs{Path: "~/tree/internal", Recursive: true})
	refused(t, got, err, "source of the core")
	got, err = w.run(owner, Delete, deleteArgs{Path: "~/tree", Recursive: true})
	refused(t, got, err, "it decides how ouroboros is built")
	intact("a checkout")
	if w.content("~/tree/internal/kernel/kernel.go") != "package kernel\n" {
		t.Fatal("the core was deleted")
	}

	// A peer deletes within what it may.
	if got, err := w.run(peer(`{"allow": ["~/tree/a"]}`), Delete, deleteArgs{Path: "~/tree/a", Recursive: true}); err != nil || got != "deleted ~/tree/a" || w.content("~/tree/a/x") != "<none>" {
		t.Errorf("a peer where it may: %q, %v", got, err)
	}
}

// The settings are paths, and nothing else is a setting.
func TestSettings(t *testing.T) {
	w := newWorld(t)
	for _, tool := range tools(w.guard) {
		for settings, want := range map[string]string{
			`{"allow": ["share"]}`:         `settings of the tool ` + tool.Name + `: the path "share" has to be absolute or start with ~`,
			`{"deny": ["~/x", "./share"]}`: `settings of the tool ` + tool.Name + `: the path "./share" has to be absolute or start with ~`,
			`{"allow": "~/share"}`:         `settings of the tool ` + tool.Name + `: json: cannot unmarshal string into Go struct field settings.allow of type []string`,
			`{"alow": ["~/share"]}`:        `settings of the tool ` + tool.Name + `: json: unknown field "alow"`,
		} {
			if _, err := tool.Configure(json.RawMessage(settings)); err == nil || err.Error() != want {
				t.Errorf("%s %s: %v", tool.Name, settings, err)
			}
		}
		if _, err := tool.Configure(json.RawMessage(`{"allow": [], "deny": []}`)); err != nil {
			t.Errorf("%s: %v", tool.Name, err)
		}
		// Listed by its name alone the tool goes everywhere for the owner and nowhere for a peer.
		if _, err := tool.Run(a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"}), json.RawMessage(`{"path": "~/x", "old": "x", "pattern": "x"}`)); err == nil || !strings.Contains(err.Error(), `has no "allow" for peers`) {
			t.Errorf("%s for a peer: %v", tool.Name, err)
		}
	}
	// "allow": null is a missing one.
	w.file("~/x", "x")
	if got, err := w.run(who{settings: `{"allow": null}`}, Read, readArgs{Path: "~/x"}); err != nil || got != "x" {
		t.Errorf("got %q, %v", got, err)
	}
	got, err := w.run(who{settings: `{"allow": []}`}, Read, readArgs{Path: "~/x"})
	refused(t, got, err, `it is not in "allow" of the tool read`)

	// Without the rules no tool does anything.
	w.failing = errTest
	for _, tool := range tools(w.guard) {
		if got, err := tool.Run(context.Background(), json.RawMessage(`{"path": "~/x", "old": "x", "content": "y"}`)); !errors.Is(err, errTest) || got != "" {
			t.Errorf("%s: %q, %v", tool.Name, got, err)
		}
	}
	if w.content("~/x") != "x" {
		t.Error("a file was changed without the rules")
	}
}

// What the LLM is told about the tools.
func TestSchemas(t *testing.T) {
	all := tools(&guard.Guard{})
	var names []string
	for _, tool := range all {
		names = append(names, tool.Name)
		if !json.Valid([]byte(tool.Schema)) || !strings.Contains(tool.Schema, "absolute or starting with") {
			t.Errorf("%s: %s / %s", tool.Name, tool.Schema, tool.Description)
		}
	}
	if strings.Join(names, ",") != "read,write,edit,list,delete,search" {
		t.Errorf("the tools: %v", names)
	}
	want := `{"type":"object","properties":{"limit":{"type":"integer","description":"how many lines at most"},` +
		`"offset":{"type":"integer","description":"the first line, counted from 1"},` +
		`"path":{"type":"string","description":"the file, absolute or starting with \"~\", the home directory"}},"required":["path"]}`
	if all[0].Schema != want {
		t.Errorf("read: %s", all[0].Schema)
	}
	if want := `"required":["path","old","new"]`; !strings.Contains(all[2].Schema, want) {
		t.Errorf("edit: %s", all[2].Schema)
	}
	if want := `"recursive":{"type":"boolean","description":"delete a directory with its content"}},"required":["path"]}`; !strings.HasSuffix(all[4].Schema, want) {
		t.Errorf("delete: %s", all[4].Schema)
	}
	if want := `"required":["path","pattern"]`; !strings.Contains(all[5].Schema, want) {
		t.Errorf("search: %s", all[5].Schema)
	}
	if !strings.Contains(all[5].Description, "at most 200 lines from at most 10000 files") ||
		!strings.Contains(all[5].Description, "files over 1 MiB are skipped") {
		t.Errorf("the limits of search: %s", all[5].Description)
	}
	if !strings.Contains(all[0].Description, "at most 2000 lines or 256 KiB per call") || !strings.Contains(all[1].Description, "At most 1 MiB.") ||
		!strings.Contains(all[3].Description, "At most 1000 entries") {
		t.Errorf("the limits are not what the tools say: %s / %s / %s", all[0].Description, all[1].Description, all[3].Description)
	}
}

func TestSearch(t *testing.T) {
	w := newWorld(t)
	w.file("~/src/main.go", "package main\n\nfunc main() {\n\tprintln(\"Model\")\n}\n")
	w.file("~/src/view.go", "package main\n\n// the model is shown\nvar model = \"brain\"\n")
	w.file("~/src/notes.txt", "model\n")
	w.file("~/src/.git/config", "model\n")
	w.file("~/src/.env", "model=secret\n")
	w.file("~/src/blob.bin", "model\x00\n")
	w.file("~/src/sub/deep.go", "package sub // model\r\n")
	w.file("~/empty", "")
	for _, c := range []struct {
		args any
		want string
	}{
		{map[string]any{"path": "~/src", "pattern": "(?i)model", "glob": "*.go"},
			"~/src/main.go:4: \tprintln(\"Model\")\n~/src/sub/deep.go:1: package sub // model\n" +
				"~/src/view.go:3: // the model is shown\n~/src/view.go:4: var model = \"brain\""},
		// Hidden directories are left out, hidden files are not, what is no text is.
		{map[string]any{"path": "~/src/", "pattern": "^model"}, "~/src/.env:1: model=secret\n~/src/notes.txt:1: model"},
		{map[string]any{"path": "~/src/view.go", "pattern": "brain"}, `~/src/view.go:4: var model = "brain"`},
		{map[string]any{"path": "~/src/view.go", "pattern": "brain", "glob": "*.txt"}, "(no line matches)"},
		{map[string]any{"path": "~/src", "pattern": "nothing"}, "(no line matches)"},
		{map[string]any{"path": "~/empty", "pattern": "^$"}, "(no line matches)"},
		{map[string]any{"path": "~/src/notes.txt", "pattern": "^$"}, "(no line matches)"},
		// A hidden directory that is searched itself is searched.
		{map[string]any{"path": "~/src/.git", "pattern": "model"}, "~/src/.git/config:1: model"},
	} {
		if got, err := w.run(owner, "search", c.args); err != nil || got != c.want {
			t.Errorf("%v: got %q, %v; want %q", c.args, got, err, c.want)
		}
	}
	for args, want := range map[string]string{
		`{"path": "~/src"}`:                              `search needs "pattern"`,
		`{"path": "~/src", "pattern": "("}`:              "the pattern is no regular expression",
		`{"path": "~/src", "pattern": "x", "glob": "["}`: `the glob "[" is no pattern of file names`,
		`{"path": "~/none", "pattern": "x"}`:             "no such file or directory",
		`{"path": "src", "pattern": "x"}`:                "absolute",
	} {
		got, err := w.run(owner, "search", args)
		fails(t, got, err, want)
	}
}

// What the tool may not read is not searched: the configuration, protected
// paths and what is denied; a peer searches only where it is allowed to.
func TestSearchKeepsToTheRules(t *testing.T) {
	w := newWorld(t)
	w.file("~/.config/ouroboros/contacts.json", `{"key": 1}`)
	w.file("~/.config/ouroboros/config.json", `{"key": 2}`)
	w.file("~/share/a.txt", "key\n")
	w.file("~/share/private/b.txt", "key\n")
	if got, err := w.run(owner, "search", map[string]any{"path": "~/.config/ouroboros", "pattern": "key"}); err != nil ||
		got != `~/.config/ouroboros/contacts.json:1: {"key": 1}` {
		t.Errorf("the node: %q, %v", got, err)
	}
	got, err := w.run(owner, "search", map[string]any{"path": "~/.config/ouroboros/config.json", "pattern": "key"})
	refused(t, got, err, "")
	got, err = w.run(owner, "search", map[string]any{"path": "~/.ssh", "pattern": "key"})
	refused(t, got, err, "protected")
	if got, err := w.run(owner, "search", map[string]any{"path": "~", "pattern": "key"}); err != nil || strings.Contains(got, "id_ed25519") {
		t.Errorf("the home directory: %q, %v", got, err)
	}
	denied := who{settings: `{"deny": ["~/share/private"]}`}
	if got, err := w.run(denied, "search", map[string]any{"path": "~/share", "pattern": "key"}); err != nil || got != "~/share/a.txt:1: key" {
		t.Errorf("denied: %q, %v", got, err)
	}
	got, err = w.run(peer(`{}`), "search", map[string]any{"path": "~/share", "pattern": "key"})
	refused(t, got, err, "")
	if got, err := w.run(peer(`{"allow": ["~/share"]}`), "search", map[string]any{"path": "~/share", "pattern": "key"}); err != nil ||
		got != "~/share/a.txt:1: key\n~/share/private/b.txt:1: key" {
		t.Errorf("a peer: %q, %v", got, err)
	}
}

func TestSearchIsCut(t *testing.T) {
	w := newWorld(t)
	w.file("~/many.txt", strings.Repeat("x\n", maxMatches+5))
	w.file("~/long.txt", strings.Repeat("ä", maxMatchBytes)+"\n")
	w.file("~/big.txt", strings.Repeat("x", maxWriteBytes+1))
	got, err := w.run(owner, "search", map[string]any{"path": "~/many.txt", "pattern": "x"})
	lines := strings.Split(got, "\n")
	if err != nil || len(lines) != maxMatches+1 || lines[maxMatches-1] != "~/many.txt:200: x" ||
		!strings.HasPrefix(lines[maxMatches], "[these are the first 200 matching lines, and there are more") {
		t.Errorf("many: %d lines, %v", len(lines), err)
	}
	got, err = w.run(owner, "search", map[string]any{"path": "~/long.txt", "pattern": "ä"})
	if want := "~/long.txt:1: " + strings.Repeat("ä", maxMatchBytes/2) + "…"; err != nil || got != want {
		t.Errorf("long: %q, %v", got, err)
	}
	if got, err := w.run(owner, "search", map[string]any{"path": "~/big.txt", "pattern": "x"}); err != nil || got != "(no line matches)" {
		t.Errorf("big: %q, %v", got, err)
	}
	defer func(n int) { maxSearched = n }(maxSearched)
	maxSearched = 3
	for i := range maxSearched + 1 {
		w.file(fmt.Sprintf("~/files/%05d.txt", i), "y\n")
	}
	got, err = w.run(owner, "search", map[string]any{"path": "~/files", "pattern": "z"})
	if err != nil || got != "[the search stopped after 3 files: narrow the path or the glob]" {
		t.Errorf("files: %q, %v", got, err)
	}
	got, err = w.run(owner, "search", map[string]any{"path": "~/files", "pattern": "y", "glob": "00001.txt"})
	if err != nil || got != "~/files/00001.txt:1: y" {
		t.Errorf("a glob: %q, %v", got, err)
	}
	// What the glob leaves out is looked at all the same, and counts.
	defer func(n int) { maxWalked = n }(maxWalked)
	maxWalked = 3
	got, err = w.run(owner, "search", map[string]any{"path": "~/files", "pattern": "y", "glob": "*.go"})
	if err != nil || got != "[the search stopped after looking at 3 files and directories: narrow the path]" {
		t.Errorf("walked: %q, %v", got, err)
	}
}

// A search ends with its call, which a script that ran too long ends.
func TestSearchEndsWithItsCall(t *testing.T) {
	w := newWorld(t)
	w.file("~/src/a.txt", "x\n")
	for _, tool := range tools(w.guard) {
		if tool.Name != Search {
			continue
		}
		tool, err := tool.Configure(json.RawMessage(owner.settings))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, err := tool.Run(ctx, json.RawMessage(`{"path":"~/src","pattern":"x"}`))
		if want := "the search was stopped after looking at 0 files and directories: context canceled"; got != "" || err == nil || err.Error() != want {
			t.Errorf("got %q, %v; want the error %q", got, err, want)
		}
	}
}

// What cannot be looked into is left out of a search, unless it is all there
// is to search.
func TestSearchLeavesOutWhatCannotBeRead(t *testing.T) {
	w := newWorld(t)
	w.file("~/src/a.txt", "x\n")
	w.file("~/src/b.txt", "x\n")
	w.file("~/src/c.txt", "x\n")
	w.file("~/src/locked/d.txt", "x\n")
	w.mode("~/src/locked", 0)
	os.Symlink("nowhere", w.path("~/src/broken.txt")) // a link to nothing has no lines
	defer func(o func(string) (io.ReadCloser, error)) { open = o }(open)
	open = func(path string) (io.ReadCloser, error) {
		switch filepath.Base(path) {
		case "b.txt":
			return nil, errTest
		case "c.txt":
			return io.NopCloser(iotest.ErrReader(errTest)), nil
		}
		return os.Open(path)
	}
	if got, err := w.run(owner, "search", map[string]any{"path": "~/src", "pattern": "x"}); err != nil || got != "~/src/a.txt:1: x" {
		t.Errorf("got %q, %v", got, err)
	}
	got, err := w.run(owner, "search", map[string]any{"path": "~/src/locked", "pattern": "x"})
	fails(t, got, err, "permission denied")
}
