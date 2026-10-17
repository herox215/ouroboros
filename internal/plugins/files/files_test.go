package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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
	failing   error  // what reading the configuration fails with
	effort    string // of the answers look has the agent give
	remark    string // what read said for the owner when look had it run
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
	fails(t, got, err, "~ is a directory, which is read with list")
	got, err = w.run(owner, "read", map[string]any{"path": "/dev/null"})
	fails(t, got, err, "/dev/null is not a file but a device, a pipe or a socket, which read does not read")
	got, err = w.run(owner, Read, `{"path": 1}`)
	fails(t, got, err, "read: invalid arguments")
	got, err = w.run(owner, Read, readArgs{Path: "~user/notes.md"})
	fails(t, got, err, `the path "~user/notes.md" has to be absolute or start with ~`)

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

// Pages stay below the script output limit and say where to go on.
func TestReadInPieces(t *testing.T) {
	w := newWorld(t)
	line := strings.Repeat("x", 1023) + "\n"
	fits := strings.Repeat(line, maxReadPage/len(line))
	w.file("~/fits", fits)
	if got, err := w.run(owner, Read, readArgs{Path: "~/fits"}); err != nil || got != fits {
		t.Errorf("a file of exactly the limit: %d bytes, %v", len(got), err)
	}
	w.file("~/more", fits+"last\nvery last\n")
	note := "[these were the lines 1 to 24 of 26: continue with offset 25]"
	if got, err := w.run(owner, Read, readArgs{Path: "~/more"}); err != nil || got != fits+note {
		t.Errorf("a file of more than the limit: %d bytes, %v", len(got), err)
	}
	if got, err := w.run(owner, Read, readArgs{Path: "~/more", Offset: 25}); err != nil || got != "last\nvery last\n[these were the lines 25 to 26 of 26, the end of the file]" {
		t.Errorf("the rest: %q, %v", got, err)
	}

	// A line that fills a call alone is too long to read, and the lines
	// after it are read past it.
	w.file("~/wide", "short\n"+strings.Repeat("ä", maxReadBytes)+"\nshort\n")
	got, err := w.run(owner, Read, readArgs{Path: "~/wide", Offset: 2})
	fails(t, got, err, "line 2 of ~/wide has 256 KiB or more: too long to read; the lines after it are read with offset 3")
	if got, err := w.run(owner, Read, readArgs{Path: "~/wide", Offset: 3}); err != nil || got != "short\n[these were the lines 3 to 3 of 3, the end of the file]" {
		t.Errorf("past a long line: %q, %v", got, err)
	}
	w.file("~/wideend", "short\n"+strings.Repeat("x", maxReadBytes+1))
	if got, err := w.run(owner, Read, readArgs{Path: "~/wideend", Offset: 3}); err == nil || err.Error() != "~/wideend has only 2 lines" {
		t.Errorf("a long last line: %q, %v", got, err)
	}
	w.file("~/widenul", "short\n"+strings.Repeat("x", maxReadBytes)+"\x00\nshort\n")
	w.file("~/widenul2", "short\n\x00"+strings.Repeat("x", 2*maxReadBytes)+"\nshort\n")
	for _, path := range []string{"~/widenul", "~/widenul2"} {
		got, err = w.run(owner, Read, readArgs{Path: path, Offset: 3})
		fails(t, got, err, path+" is not a text file")
	}
	// A file that cannot be read on while a long line is passed over.
	func() {
		defer func(o func(string) (io.ReadCloser, error)) { open = o }(open)
		open = func(string) (io.ReadCloser, error) {
			return io.NopCloser(io.MultiReader(strings.NewReader("short\n"+strings.Repeat("x", 2*maxReadBytes)), iotest.ErrReader(errTest))), nil
		}
		got, err := w.run(owner, Read, readArgs{Path: "~/wide", Offset: 3})
		if !errors.Is(err, errTest) || got != "" {
			t.Errorf("an error on the way: %q, %v", got, err)
		}
	}()
	// After the lines returned a long line is only counted.
	w.file("~/widelater", "short\n"+strings.Repeat("x", 2*maxReadBytes)+"\nshort")
	if got, err := w.run(owner, Read, readArgs{Path: "~/widelater", Limit: 1}); err != nil || got != "short\n[these were the lines 1 to 1 of 3: continue with offset 2]" {
		t.Errorf("a long line after those returned: %q, %v", got, err)
	}
	w.file("~/justfits", "short\n"+strings.Repeat("x", maxReadBytes-1)+"\nshort\n")
	if got, err := w.run(owner, Read, readArgs{Path: "~/justfits", Offset: 2, Limit: 1}); err != nil || !strings.HasPrefix(got, strings.Repeat("x", maxReadPage)) || len(got) >= 32<<10 {
		t.Errorf("a line that just fits: %d bytes, %v; start %q, end %q", len(got), err, got[:min(len(got), 50)], got[max(0, len(got)-150):])
	}
}

// Even a long UTF-8 line can be retrieved in full without splitting a character.
func TestReadLongLinePages(t *testing.T) {
	w := newWorld(t)
	source := "x" + strings.Repeat("€", 20000)
	w.file("~/page.html", source)
	var joined strings.Builder
	for column := 0; column < len(source); {
		got, err := w.run(owner, Read, readArgs{Path: "~/page.html", Column: column})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) >= 32<<10 {
			t.Fatalf("page exceeds script output limit: %d bytes", len(got))
		}
		chunk, note, ok := strings.Cut(got, "\n[")
		if !ok || chunk == "" {
			t.Fatalf("missing content or continuation: %q", got)
		}
		joined.WriteString(chunk)
		column += len(chunk)
		if column < len(source) {
			want := fmt.Sprintf("continue with offset 1 and column %d]", column)
			if !strings.HasSuffix(note, want) {
				t.Fatalf("continuation: %q, want %q", note, want)
			}
		} else if !strings.Contains(note, "the end of the file") {
			t.Fatalf("missing end: %q", note)
		}
	}
	if joined.String() != source {
		t.Fatal("pages lost or duplicated text")
	}
	for _, column := range []int{-1, 2, len(source)} {
		if _, err := w.run(owner, Read, readArgs{Path: "~/page.html", Column: column}); err == nil {
			t.Errorf("invalid column %d accepted", column)
		}
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
	// And when it fails after the page of a long line.
	open = func(string) (io.ReadCloser, error) {
		return io.NopCloser(io.MultiReader(strings.NewReader(strings.Repeat("a", maxReadPage+1)+"\n"), iotest.ErrReader(errTest))), nil
	}
	if got, err := w.run(owner, Read, readArgs{Path: "~/utf8"}); !errors.Is(err, errTest) || got != "" {
		t.Errorf("a file failing after the page of a long line: %q, %v", got, err)
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
	fails(t, got, err, "~/new is a directory: write writes a file")
	w.file("~/locked/x", "")
	w.mode("~/locked", 0o500)
	got, err = w.run(owner, Write, writeArgs{Path: "~/locked/y", Content: "x"})
	fails(t, got, err, "permission denied")
	got, err = w.run(owner, Write, writeArgs{Path: "~/big/x", Content: "x"})
	fails(t, got, err, "not a directory")
	os.Mkdir(w.path("~/locked"), 0o755)
	w.mode("~/locked", 0o555)
	got, err = w.run(owner, Write, writeArgs{Path: "~/locked/sub/x", Content: "x"})
	fails(t, got, err, "permission denied")
	got, err = w.run(owner, Write, writeArgs{Path: "~/locked/x", Content: "x"})
	fails(t, got, err, "permission denied")
}

// A page of a file that read gave back is not written back: its note says
// that the rest of the file is missing. The note quoted in a line of a file,
// as in a test, is no page.
func TestPageIsNotWrittenBack(t *testing.T) {
	w := newWorld(t)
	var lines []string
	for i := range 3000 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	w.file("~/long.txt", strings.Join(lines, "\n")+"\n")
	page, err := w.run(owner, Read, readArgs{Path: "~/long.txt"})
	if err != nil || !strings.Contains(page, "continue with offset") {
		t.Fatalf("the first page: %v", err)
	}
	got, err := w.run(owner, Write, writeArgs{Path: "~/long.txt", Content: page + "\nline appended\n"})
	fails(t, got, err, "it holds only a part of the file")
	if !strings.HasSuffix(w.content("~/long.txt"), "line 2999\n") {
		t.Fatal("the file lost its end")
	}
	last, _ := w.run(owner, Read, readArgs{Path: "~/long.txt", Offset: 2999})
	got, err = w.run(owner, Write, writeArgs{Path: "~/copy.txt", Content: last})
	fails(t, got, err, "the end of the file")
	w.file("~/a.txt", "a\n")
	got, err = w.run(owner, Edit, editArgs{Path: "~/a.txt", Old: "a", New: "b\n[these were bytes 1 to 10 of line 2 of 3: continue with offset 2 and column 10]"})
	fails(t, got, err, "it holds only a part of the file")

	quoted := "want := \"one\\n[these were the lines 1 to 1 of 3: continue with offset 2]\"\n"
	if got, err := w.run(owner, Write, writeArgs{Path: "~/files_test.go", Content: quoted}); err != nil {
		t.Errorf("a quoted note: %q, %v", got, err)
	}
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
	// The source of ouro is written, its core and its go.mod too.
	for _, path := range []string{"~/src/internal/plugins/mine/mine.go", "~/src/internal/a2a/a2a.go"} {
		if _, err := w.run(owner, Write, writeArgs{Path: path, Content: "package mine // changed\n"}); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
	if _, err := w.run(owner, Edit, editArgs{Path: "~/src/go.mod", Old: "go 1.27", New: "go 1.28"}); err != nil || w.content("~/src/go.mod") != "module ouroboros\n\ngo 1.28\n" {
		t.Errorf("go.mod: %v", err)
	}
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
	// One line more than shown is cut as well.
	w.file("~/long", "start\n")
	got, err = w.run(owner, Edit, editArgs{Path: "~/long", Old: "start\n", New: strings.Repeat("x\n", maxEditLines+1)})
	if err != nil || !strings.HasPrefix(got, fmt.Sprintf("edited ~/long; lines 1 to %d of %d are now:\n", maxEditLines, maxEditLines+1)) {
		t.Errorf("got %q, %v", got, err)
	}
	// A short change far down in a file is not cut.
	w.file("~/far", lines(1, 100))
	got, err = w.run(owner, Edit, editArgs{Path: "~/far", Old: "line 60\n", New: "sixty\n"})
	if want := "edited ~/far; lines 57 to 63 of 100 are now:\nline 57\nline 58\nline 59\nsixty\nline 61\nline 62\nline 63"; err != nil || got != want {
		t.Errorf("got %q, %v, want %q", got, err, want)
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

	// The node is below what is deleted.
	got, err = w.run(owner, Delete, deleteArgs{Path: "~/.config", Recursive: true})
	refused(t, got, err, "the directory of this node")
	if w.content("~/.config/ouroboros/contacts.json") != "{}" {
		t.Fatal("the state of the node was deleted")
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
		if _, err := tool.Run(a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"}), argsOf(tool.Name)); err == nil || !strings.Contains(err.Error(), `has no "allow" for peers`) {
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
		if got, err := tool.Run(context.Background(), argsOf(tool.Name)); !errors.Is(err, errTest) || got != "" {
			t.Errorf("%s: %q, %v", tool.Name, got, err)
		}
	}
	if w.content("~/x") != "x" {
		t.Error("a file was changed without the rules")
	}
}

// What the LLM is told about the tools. What a path is the prompt of the
// agent says, once for all of them.
func TestSchemas(t *testing.T) {
	all := tools(&guard.Guard{})
	var names []string
	for _, tool := range all {
		names = append(names, tool.Name)
		if !json.Valid([]byte(tool.Schema)) || !strings.Contains(tool.Schema, `"path":{"type":"string","description":"the `) ||
			strings.Contains(tool.Schema, "absolute") {
			t.Errorf("%s: %s / %s", tool.Name, tool.Schema, tool.Description)
		}
	}
	if strings.Join(names, ",") != "read,write,edit,list,delete,search" {
		t.Errorf("the tools: %v", names)
	}
	want := `{"type":"object","properties":{"column":{"type":"integer","description":"bytes to skip in the first line, from a continuation note; default 0"},` +
		`"limit":{"type":"integer","description":"how many lines at most"},` +
		`"offset":{"type":"integer","description":"the first line, counted from 1"},` +
		`"path":{"type":"string","description":"the file"},` +
		`"region":{"type":"string","description":"of a picture: only the part \"x,y,width,height\", in its own pixels"},` +
		`"size":{"type":"integer","description":"of a picture: the longer side in pixels it is shown with at most; leave out for what the effort allows"}},"required":["path"]}`
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
	if !strings.Contains(all[0].Description, "at most 2000 lines or 24 KiB per call") || !strings.Contains(all[1].Description, "At most 1 MiB.") ||
		!strings.Contains(all[3].Description, "At most 1000 entries") {
		t.Errorf("the limits are not what the tools say: %s / %s / %s", all[0].Description, all[1].Description, all[3].Description)
	}
}

// Research guidance must agree with the owner prompt: bounded sections may
// share a script, but separate page limits do not protect its combined output.
func TestResearchGuidance(t *testing.T) {
	all := tools(&guard.Guard{})
	for _, tt := range []struct {
		at   int
		want string
	}{
		{0, "Batch relevant bounded sections in one script only when their combined output, including notes and other results, stays safely below the script output limit."},
		{0, "Use offset and limit for relevant code sections located by search; leave out limit when the whole file is needed."},
		{5, "For extensive code analysis, search first for function and declaration overviews, then read relevant sections with offset and limit, not whole files by default;"},
	} {
		if !strings.Contains(all[tt.at].Description, tt.want) {
			t.Errorf("%s: missing research guidance %q", all[tt.at].Name, tt.want)
		}
	}
	for _, tool := range all {
		for _, stale := range []string{"Print one page per script", "whole unless it is very long"} {
			if strings.Contains(tool.Description, stale) {
				t.Errorf("%s: conflicting research guidance %q", tool.Name, stale)
			}
		}
	}
}

// Search examples must survive JavaScript string parsing before RE2 sees them.
func TestSearchJavaScriptGuidance(t *testing.T) {
	description := tools(&guard.Guard{})[5].Description
	for _, want := range []string{
		"use [(] and [)] for literal parentheses",
		`pattern: "func main[(][)]"`,
		"to avoid losing backslashes in JS strings",
		"pattern: String.raw`\\bmain\\s*\\(`",
	} {
		if !strings.Contains(description, want) {
			t.Errorf("search: missing JavaScript guidance %q", want)
		}
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
	w.file("~/src/lead.bin", "\x00model\n")
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
		{map[string]any{"path": "~/src/view.go", "pattern": "brain", "glob": "*.txt"}, `(no file of ~/src/view.go is searched with the glob "*.txt")`},
		// A glob that starts with ! leaves out what it matches.
		{map[string]any{"path": "~/src", "pattern": "(?i)model", "glob": "!*.go"}, "~/src/.env:1: model=secret\n~/src/notes.txt:1: model"},
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
		`{"path": "~/src"}`:                               `search needs "pattern"`,
		`{"path": "~/src", "pattern": "("}`:               "the pattern is no regular expression",
		`{"path": "~/src", "pattern": "x", "glob": "["}`:  `the glob "[" is no pattern of file names`,
		`{"path": "~/src", "pattern": "x", "glob": "!["}`: `the glob "![" is no pattern of file names`,
		`{"path": "~/none", "pattern": "x"}`:              "no such file or directory",
		`{"path": "~src", "pattern": "x"}`:                "absolute",
		// A file asked for is told why it has no lines to match.
		`{"path": "~/src/lead.bin", "pattern": "model"}`: "~/src/lead.bin is not a text file",
		`{"path": "/dev/null", "pattern": "x"}`:          "/dev/null is no file: search reads files",
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
	// A big file named itself is searched, up to maxSearchFile; in a
	// directory it is left out.
	if got, err := w.run(owner, "search", map[string]any{"path": "~/big.txt", "pattern": "^x"}); err != nil || !strings.HasPrefix(got, "~/big.txt:1: xxx") {
		t.Errorf("big: %.40q, %v", got, err)
	}
	w.file("~/bigdir/big.txt", strings.Repeat("x", maxWriteBytes+1))
	if got, err := w.run(owner, "search", map[string]any{"path": "~/bigdir", "pattern": "x"}); err != nil || got != "(no line matches)" {
		t.Errorf("big in a directory: %.40q, %v", got, err)
	}
	defer func(n int64) { maxSearchFile = n }(maxSearchFile)
	maxSearchFile = maxWriteBytes
	got, err = w.run(owner, "search", map[string]any{"path": "~/big.txt", "pattern": "x"})
	fails(t, got, err, "~/big.txt has 1048577 bytes, and search reads files up to 1048576: read it in parts with read")
	// Up to the limits, nothing is cut.
	w.file("~/enough.txt", strings.Repeat("x\n", maxMatches))
	if got, err := w.run(owner, "search", map[string]any{"path": "~/enough.txt", "pattern": "x"}); err != nil ||
		strings.Count(got, "\n") != maxMatches-1 || strings.Contains(got, "[") {
		t.Errorf("enough: %q, %v", got, err)
	}
	w.file("~/wide.txt", strings.Repeat("a", maxMatchBytes)+"\n")
	if got, err := w.run(owner, "search", map[string]any{"path": "~/wide.txt", "pattern": "a"}); err != nil ||
		got != "~/wide.txt:1: "+strings.Repeat("a", maxMatchBytes) {
		t.Errorf("wide: %q, %v", got, err)
	}
	w.file("~/limit.txt", "y"+strings.Repeat("x", maxWriteBytes-1))
	if got, err := w.run(owner, "search", map[string]any{"path": "~/limit.txt", "pattern": "^y"}); err != nil || !strings.HasPrefix(got, "~/limit.txt:1: yx") {
		t.Errorf("limit: %q, %v", got, err)
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

// An edit whose "old" is not in the file says where it parts from the
// file, so that the LLM can fix it without reading the file again.
func TestEditSaysWhereOldParts(t *testing.T) {
	w := newWorld(t)
	w.file("~/a.go", "func f() {\n\tif x {\n\t\tt.Fatalf(\"no:\\n%s\", x)\n\t}\n}\n")
	for old, want := range map[string]string{
		// A line break where the file has a backslash and an n.
		"\tif x {\n\t\tt.Fatalf(\"no:\n%s\", x)": `; 1 of its lines match from line 2 of the file on, then line 3 of the file is "\t\tt.Fatalf(\"no:\\n%s\", x)", ` +
			`where "old" has "\t\tt.Fatalf(\"no:"; in a string of JavaScript a backslash starts an escape`,
		// Spaces where the file has tabs.
		"  if x {\n    t.Fatalf(\"no:\\n%s\", x)": "; its lines are lines 2 to 3 of the file, but not with the same spaces and tabs",
		// The end of the file.
		"}\n}\nmore":      `; 2 of its lines match from line 4 of the file on, then line 6 of the file is "", where "old" has "more"`,
		"nothing like it": "; not even its first line is in the file",
	} {
		got, err := w.run(owner, Edit, editArgs{Path: "~/a.go", Old: old, New: "x"})
		if err == nil || got != "" || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v\nwant %q", old, err, want)
		}
	}
}

// With "all", edit replaces every time "old" occurs, and says in which
// lines; once, it is as without it.
func TestEditAll(t *testing.T) {
	w := newWorld(t)
	w.file("~/a.go", "x := \"old\"\n// \"old\"\n\ny := \"old\"\n")
	got, err := w.run(owner, Edit, map[string]any{"path": "~/a.go", "old": `"old"`, "new": "\"new\"\n", "all": true})
	if err != nil || got != "edited ~/a.go: replaced 3 times, in the lines 1, 3, 6" || w.content("~/a.go") != "x := \"new\"\n\n// \"new\"\n\n\ny := \"new\"\n\n" {
		t.Fatalf("got %q, %v: %q", got, err, w.content("~/a.go"))
	}
	if got, err := w.run(owner, Edit, map[string]any{"path": "~/a.go", "old": "x :=", "new": "z :=", "all": true}); err != nil || !strings.HasPrefix(got, "edited ~/a.go; lines 1 to") {
		t.Fatalf("once: %q, %v", got, err)
	}
	got, err = w.run(owner, Edit, map[string]any{"path": "~/a.go", "old": `"new"`, "new": "x"})
	fails(t, got, err, `"old" occurs 3 times in ~/a.go: it has to match exactly once, so take more of the text around it, or replace it every time with "all"`)
}

// With context, search shows the lines around every match too, as grep -C
// does: places that touch are one, places apart are split by --.
func TestSearchWithContext(t *testing.T) {
	w := newWorld(t)
	w.file("~/c.txt", "a\nhit 1\nb\nc\nd\ne\nhit 2\nf\nhit 3\ng\n")
	got, err := w.run(owner, "search", map[string]any{"path": "~/c.txt", "pattern": "hit", "context": 1})
	want := "~/c.txt-1- a\n~/c.txt:2: hit 1\n~/c.txt-3- b\n--\n~/c.txt-6- e\n~/c.txt:7: hit 2\n~/c.txt-8- f\n~/c.txt:9: hit 3\n~/c.txt-10- g"
	if err != nil || got != want {
		t.Fatalf("got\n%s\n%v\nwant\n%s", got, err, want)
	}
	// One place that holds two matches.
	got, err = w.run(owner, "search", map[string]any{"path": "~/c.txt", "pattern": "hit [23]", "context": 3})
	if err != nil || strings.Count(got, "--") != 0 || !strings.HasPrefix(got, "~/c.txt-4- c\n") || !strings.HasSuffix(got, "~/c.txt-10- g") {
		t.Fatalf("one place: %q, %v", got, err)
	}
	// At most maxPlaces places.
	w.file("~/many.txt", strings.Repeat("hit\n\n\n\n", maxPlaces+1))
	got, err = w.run(owner, "search", map[string]any{"path": "~/many.txt", "pattern": "hit", "context": 1})
	if err != nil || !strings.HasSuffix(got, fmt.Sprintf("[these are the first %d places, and there are more: narrow the path, the pattern or the glob]", maxPlaces)) {
		t.Fatalf("places: %q, %v", got, err)
	}
	// The limit counts the places of every file.
	w.file("~/two/a.txt", strings.Repeat("hit\n\n\n\n", maxPlaces-1))
	w.file("~/two/b.txt", strings.Repeat("hit\n\n\n\n", 3))
	got, err = w.run(owner, "search", map[string]any{"path": "~/two", "pattern": "hit", "context": 1})
	if err != nil || strings.Count(got, "\n--\n") != maxPlaces || !strings.Contains(got, "~/two/b.txt:1: hit") || strings.Contains(got, "~/two/b.txt:5: hit") ||
		!strings.HasSuffix(got, "places, and there are more: narrow the path, the pattern or the glob]") {
		t.Fatalf("places of two files: %q, %v", got, err)
	}
	// Beyond maxAround bytes, a place is only its matching lines.
	filler := strings.Repeat(strings.Repeat("x", 99)+"\n", 45)
	w.file("~/big.txt", strings.Repeat(filler+"hit\n", 5)+filler)
	got, err = w.run(owner, "search", map[string]any{"path": "~/big.txt", "pattern": "hit", "context": 20})
	places := strings.Split(got, "\n--\n")
	if err != nil || !strings.HasPrefix(places[0], "~/big.txt-26- ") || len(got) > maxAround+500 ||
		!regexp.MustCompile(`^\[[1-4] more matching lines, beyond what one search shows, in ~/big.txt \([1-4]\): search there with a narrower pattern\]$`).MatchString(places[len(places)-1]) {
		t.Fatalf("beyond the bytes: %d places, %d bytes, %v\n%s", len(places), len(got), err, places[len(places)-1])
	}
	// Matches that go on and on are cut into places of maxPlaceLines lines,
	// so that one place does not hold all of a file.
	w.file("~/on.txt", strings.Repeat("m\n", 20000))
	got, err = w.run(owner, "search", map[string]any{"path": "~/on.txt", "pattern": "m", "context": 1})
	if places := strings.Split(got, "\n--\n"); err != nil || len(got) > maxAround+maxPlaceLines*20 || !strings.HasPrefix(places[0], "~/on.txt:1: m\n") ||
		strings.Count(places[0], "\n") != maxPlaceLines-1 || !strings.Contains(got, fmt.Sprintf("~/on.txt:%d: m", maxPlaceLines+1)) {
		t.Fatalf("on and on: %d bytes, %v\n%.500s", len(got), err, got)
	}
	// A line that ends in \r\n matches as it is shown.
	w.file("~/crlf.txt", "end here\r\nnot\r\n")
	got, err = w.run(owner, "search", map[string]any{"path": "~/crlf.txt", "pattern": "here$", "context": 1})
	if err != nil || got != "~/crlf.txt:1: end here\n~/crlf.txt-2- not" {
		t.Fatalf("crlf: %q, %v", got, err)
	}
	for _, context := range []int{-1, maxContext + 1} {
		got, err = w.run(owner, "search", map[string]any{"path": "~/c.txt", "pattern": "hit", "context": context})
		fails(t, got, err, "context is a number of lines from 0 to 30")
	}
}

// Beyond maxAround bytes, search says how many more matches are in which
// file, instead of lines that would not come back from a script.
func TestSearchFitsItsBytes(t *testing.T) {
	w := newWorld(t)
	w.file("~/fit/a.txt", strings.Repeat("hit "+strings.Repeat("x", 200)+"\n", 100))
	w.file("~/fit/b.txt", "hit\n")
	got, err := w.run(owner, "search", map[string]any{"path": "~/fit", "pattern": "hit"})
	lines := strings.Split(got, "\n")
	last := lines[len(lines)-1]
	if err != nil || len(got) > maxAround+300 || !strings.HasPrefix(lines[0], "~/fit/a.txt:1: hit") ||
		!regexp.MustCompile(`^\[\d+ more matching lines, beyond what one search shows, in ~/fit/a.txt \(\d+\), ~/fit/b.txt \(1\): search there with a narrower pattern\]$`).MatchString(last) {
		t.Fatalf("%d bytes, %v, the end: %s", len(got), err, last)
	}
}

// A search that stopped at its limit and is cut to its bytes says one thing
// of what is not shown: how many more matching lines there are at least.
func TestSearchCutAtTheLimitAndItsBytes(t *testing.T) {
	w := newWorld(t)
	w.file("~/many.txt", strings.Repeat("match "+strings.Repeat("x", 120)+"\n", 300))
	got, err := w.run(owner, "search", map[string]any{"path": "~/many.txt", "pattern": "match"})
	lines := strings.Split(got, "\n")
	if err != nil || strings.Contains(got, "these are the first") ||
		!regexp.MustCompile(`^\[at least \d+ more matching lines, beyond what one search shows, in ~/many.txt \(\d+\): search there with a narrower pattern\]$`).MatchString(lines[len(lines)-1]) {
		t.Fatalf("%v, the end: %s", err, lines[len(lines)-1])
	}
}

// With from and to, edit replaces those lines, whatever their text: no old
// has to be written as the file has it.
func TestEditLines(t *testing.T) {
	w := newWorld(t)
	w.file("~/l.go", "a\nb \"\\n\"\nb \"\\n\"\nd\n")
	got, err := w.run(owner, Edit, map[string]any{"path": "~/l.go", "from": 3, "to": 3, "new": "c"})
	if err != nil || w.content("~/l.go") != "a\nb \"\\n\"\nc\nd\n" || !strings.HasPrefix(got, "edited ~/l.go; lines 1 to 4 of 4 are now:") {
		t.Fatalf("one line: %q, %v: %q", got, err, w.content("~/l.go"))
	}
	if _, err := w.run(owner, Edit, map[string]any{"path": "~/l.go", "from": 1, "to": 2, "new": "x\ny\nz\n"}); err != nil || w.content("~/l.go") != "x\ny\nz\nc\nd\n" {
		t.Fatalf("more lines: %v: %q", err, w.content("~/l.go"))
	}
	if _, err := w.run(owner, Edit, map[string]any{"path": "~/l.go", "from": 4, "to": 5, "new": ""}); err != nil || w.content("~/l.go") != "x\ny\nz\n" {
		t.Fatalf("taken out: %v: %q", err, w.content("~/l.go"))
	}
	w.file("~/end", "a\nb")
	if _, err := w.run(owner, Edit, map[string]any{"path": "~/end", "from": 2, "to": 2, "new": "c"}); err != nil || w.content("~/end") != "a\nc" {
		t.Fatalf("the last line, without a break: %v: %q", err, w.content("~/end"))
	}
	for args, want := range map[string]string{
		`{"path": "~/l.go", "from": 0, "to": 1, "new": "x"}`:             "~/l.go: the lines 0 to 1 are not lines of the file, which has 3",
		`{"path": "~/l.go", "from": 2, "to": 1, "new": "x"}`:             "the lines 2 to 1 are not lines",
		`{"path": "~/l.go", "from": 3, "to": 4, "new": "x"}`:             "the lines 3 to 4 are not lines",
		`{"path": "~/l.go", "from": 1, "to": 1, "old": "x", "new": "y"}`: `edit takes "old" or "from" and "to", not both`,
		`{"path": "~/l.go", "to": 1, "all": true, "new": "y"}`:           `edit takes "old" or "from" and "to", not both`,
		`{"path": "~/l.go", "new": "y"}`:                                 `edit needs "old", the text to replace, or "from" and "to", the lines to replace`,
	} {
		got, err := w.run(owner, Edit, args)
		fails(t, got, err, want)
	}
}

// In a file whose lines end in \r\n, "old" with \n matches, as read shows
// the lines, and what is put in keeps their ends.
func TestEditCRLF(t *testing.T) {
	w := newWorld(t)
	w.file("~/crlf.txt", "alpha\r\nbeta\r\ngamma\r\n")
	if _, err := w.run(owner, Edit, editArgs{Path: "~/crlf.txt", Old: "alpha\nbeta", New: "A\nB\r\nC"}); err != nil || w.content("~/crlf.txt") != "A\r\nB\r\nC\r\ngamma\r\n" {
		t.Fatalf("%v: %q", err, w.content("~/crlf.txt"))
	}
	got, err := w.run(owner, Edit, editArgs{Path: "~/crlf.txt", Old: "C\ndelta", New: "x"})
	fails(t, got, err, `"old" does not occur`)
	// So do the lines replaced by their numbers.
	if _, err := w.run(owner, Edit, map[string]any{"path": "~/crlf.txt", "from": 1, "to": 2, "new": "one\ntwo"}); err != nil || w.content("~/crlf.txt") != "one\r\ntwo\r\nC\r\ngamma\r\n" {
		t.Fatalf("lines: %v: %q", err, w.content("~/crlf.txt"))
	}
}

// argsOf are arguments of a call of the tool name that do what it does with
// ~/x.
func argsOf(name string) json.RawMessage {
	return json.RawMessage(map[string]string{
		Read: `{"path": "~/x"}`, Write: `{"path": "~/x", "content": "y"}`, Edit: `{"path": "~/x", "old": "x", "new": "y"}`,
		List: `{"path": "~/x"}`, Delete: `{"path": "~/x"}`, Search: `{"path": "~/x", "pattern": "x"}`,
	}[name])
}

// An argument a tool does not have is named, with those it has.
func TestUnknownArguments(t *testing.T) {
	w := newWorld(t)
	got, err := w.run(owner, Read, `{"file": "~/x"}`)
	fails(t, got, err, `read: invalid arguments: it has no "file"; it takes path, offset, column, limit, size, region`)
}

// A page of a long line ends before maxReadPage bytes, at the start of a
// character, and says which bytes of which line of how many it held; a
// line of exactly maxReadPage bytes is no page; a column cuts the first
// line alone.
func TestReadPageBounds(t *testing.T) {
	w := newWorld(t)
	one := strings.Repeat("x", maxReadPage-1) + "\n"
	w.file("~/one", one)
	if got, err := w.run(owner, Read, readArgs{Path: "~/one"}); err != nil || got != one {
		t.Errorf("a line of exactly a page: %d bytes, %v", len(got), err)
	}
	source := "x" + strings.Repeat("€", 20000)
	w.file("~/long", source+"\nshort\nshort\n")
	for _, column := range []int{0, 24574} {
		got, err := w.run(owner, Read, readArgs{Path: "~/long", Column: column})
		chunk, note, _ := strings.Cut(got, "\n[")
		end := column + len(chunk)
		want := fmt.Sprintf("these were bytes %d to %d of line 1 of 3: continue with offset 1 and column %d]", column+1, end, end)
		if err != nil || len(chunk) > maxReadPage || chunk != source[column:end] || note != want {
			t.Errorf("column %d: %d bytes, %q, %v", column, len(chunk), note, err)
		}
	}
	w.file("~/columns", "abcdef\nxyz123\n")
	if got, err := w.run(owner, Read, readArgs{Path: "~/columns", Column: 2}); err != nil || got != "cdef\nxyz123\n[these were the lines 1 to 2 of 2, the end of the file]" {
		t.Errorf("a column: %q, %v", got, err)
	}
}

// What edit says and does at the edges: a file with both ends of lines,
// "old" that goes past the end of the file or parts from it on its first
// line, and lines that "all" adds before the next place.
func TestEditEdges(t *testing.T) {
	w := newWorld(t)
	w.file("~/mixed.txt", "x\ny\r\nx\r\ny\r\n")
	if _, err := w.run(owner, Edit, editArgs{Path: "~/mixed.txt", Old: "x\ny", New: "z"}); err != nil || w.content("~/mixed.txt") != "z\r\nx\r\ny\r\n" {
		t.Errorf("old as it is written: %v, %q", err, w.content("~/mixed.txt"))
	}
	w.file("~/crlf.txt", "a\r\nb\r\n")
	w.file("~/end.txt", "a\nb\n")
	for path, old := range map[string]string{
		"~/crlf.txt": "a\nq\nc",
		"~/end.txt":  "b\n\nx",
	} {
		want := map[string]string{
			"~/crlf.txt": `; 1 of its lines match from line 1 of the file on, then line 2 of the file is "b\r", where "old" has "q"`,
			"~/end.txt":  `; 2 of its lines match from line 2 of the file on, then line 4 of the file is (the end of the file), where "old" has "x"`,
		}[path]
		got, err := w.run(owner, Edit, editArgs{Path: path, Old: old, New: "z"})
		if err == nil || got != "" || !strings.HasSuffix(err.Error(), want) {
			t.Errorf("%s: %v\nwant %q", path, err, want)
		}
	}
	w.file("~/all.txt", "a\nb\na\nb\n")
	got, err := w.run(owner, Edit, map[string]any{"path": "~/all.txt", "old": "a\n", "new": "x\ny\n", "all": true})
	if err != nil || got != "edited ~/all.txt: replaced 2 times, in the lines 1, 4" || w.content("~/all.txt") != "x\ny\nb\nx\ny\nb\n" {
		t.Errorf("all: %q, %v, %q", got, err, w.content("~/all.txt"))
	}
}

// What search shows at its bounds: as much as fills maxAround bytes, the
// first place even when it alone is more, and context up to maxContext; a
// path that starts with [ is cut as well.
func TestSearchBounds(t *testing.T) {
	w := newWorld(t)
	var b strings.Builder
	var shown []string
	size := 0
	for k := 1; ; k++ {
		prefix := fmt.Sprintf("~/fit.txt:%d: ", k)
		if rest := maxAround - size - len(prefix); rest <= maxMatchBytes {
			text := "m" + strings.Repeat("x", rest-1)
			b.WriteString(text + "\nm\n")
			shown = append(shown, prefix+text)
			break
		}
		text := "m" + strings.Repeat("x", 199)
		b.WriteString(text + "\n")
		shown = append(shown, prefix+text)
		size += len(prefix) + len(text)
	}
	w.file("~/fit.txt", b.String())
	got, err := w.run(owner, "search", map[string]any{"path": "~/fit.txt", "pattern": "^m"})
	want := strings.Join(shown, "\n") + "\n[1 more matching lines, beyond what one search shows, in ~/fit.txt (1): search there with a narrower pattern]"
	if err != nil || got != want {
		t.Errorf("exactly maxAround: %v, the end: %q", err, got[max(0, len(got)-200):])
	}

	// A place holds a match with maxContext lines on either side.
	w.file("~/wide.txt", strings.Repeat("m"+strings.Repeat("x", 290)+"\n", maxPlaceLines))
	got, err = w.run(owner, "search", map[string]any{"path": "~/wide.txt", "pattern": "^m", "context": maxContext})
	if first := strings.Split(got, "\n--\n")[0]; err != nil || !strings.HasPrefix(first, "~/wide.txt:1: m") || strings.Count(first, "\n") != 2*maxContext || len(first) <= maxAround {
		t.Errorf("a first place of more than maxAround: %d bytes, %v", len(got), err)
	}

	w.guard.Workspace = w.home
	w.file("~/[x]/a.txt", strings.Repeat("hit "+strings.Repeat("x", 200)+"\n", 100))
	got, err = w.run(owner, "search", map[string]any{"path": "[x]", "pattern": "hit"})
	if lines := strings.Split(got, "\n"); err != nil || len(got) > maxAround+300 || !strings.HasPrefix(lines[0], "[x]/a.txt:1: hit") ||
		!strings.HasPrefix(lines[len(lines)-1], "[") || !strings.Contains(lines[len(lines)-1], "more matching lines, beyond what one search shows, in [x]/a.txt") {
		t.Errorf("a path that starts with [: %d bytes, %v", len(got), err)
	}
}
