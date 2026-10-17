package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const sample = `---
name: demo
description: A harmless test.
---
Read the checklist.`

func TestWriteAndLoad(t *testing.T) {
	l := library{filepath.Join(t.TempDir(), "skills"), func(string, ...any) {}}
	loader := configured(t, l, `{}`)
	tool := l.writeTool()
	if !tool.OwnerOnly || !json.Valid([]byte(tool.Schema)) {
		t.Fatal("invalid owner-only tool")
	}
	save := func(a writeArgs) error {
		b, _ := json.Marshal(a)
		_, err := tool.Run(context.Background(), b)
		return err
	}
	if err := save(writeArgs{Name: "demo", Content: sample}); err != nil {
		t.Fatal(err)
	}
	got, err := loader.Run(context.Background(), json.RawMessage(`{"name":"demo"}`))
	if err != nil || !strings.Contains(got, "Read the checklist.") {
		t.Fatalf("load new skill: %q, %v", got, err)
	}
	if err := save(writeArgs{Name: "demo", File: "notes/check.md", Content: "check"}); err != nil {
		t.Fatal(err)
	}
	got, err = loader.Run(context.Background(), json.RawMessage(`{"name":"demo","file":"notes/check.md"}`))
	if err != nil || got != "check" {
		t.Fatalf("auxiliary: %q, %v", got, err)
	}
	if err := save(writeArgs{Name: "demo", Content: sample}); err == nil {
		t.Fatal("overwrote without permission")
	}
	updated := strings.ReplaceAll(sample, "Read the checklist.", "Updated instructions.")
	if err := save(writeArgs{Name: "demo", Content: updated, Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	got, err = loader.Run(context.Background(), json.RawMessage(`{"name":"demo"}`))
	if err != nil || !strings.Contains(got, "Updated instructions.") {
		t.Fatalf("updated: %q, %v", got, err)
	}
	if !strings.Contains(configured(t, l, `{}`).Description, "demo: A harmless test.") {
		t.Fatal("new description missing")
	}
	b, _ := json.Marshal(writeArgs{Name: "demo", Content: sample, Overwrite: true})
	if _, err := tool.Run(peer, b); err == nil {
		t.Fatal("peer wrote skill")
	}
}

// Every file written is logged, by the skill and the file the call named.
func TestWriteIsLogged(t *testing.T) {
	var logs []string
	l := library{t.TempDir(), func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }}
	b, _ := json.Marshal(writeArgs{Name: "demo", Content: sample})
	if _, err := l.writeTool().Run(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0] != "tool skill_write: demo " {
		t.Fatalf("log: %q", logs)
	}
}

func TestWriteRefusesInvalidFiles(t *testing.T) {
	l := library{t.TempDir(), nil}
	if err := l.write(writeArgs{Name: "valid", Content: strings.ReplaceAll(sample, "demo", "valid")}); err != nil {
		t.Fatal(err)
	}
	path := "file must be a clean relative path within the skill"
	content := "content must be UTF-8 text of at most 256 KiB without NUL bytes"
	cases := []struct {
		a    writeArgs
		want string // "" for any error
	}{
		{writeArgs{Name: "../escape", Content: sample}, "invalid skill name"},
		{writeArgs{Name: "valid", File: "../escape", Content: "x"}, path},
		{writeArgs{Name: "valid", File: "/tmp/escape", Content: "x"}, path},
		{writeArgs{Name: "valid", File: ".", Content: "x"}, path},
		{writeArgs{Name: "valid", File: "notes/../check.md", Content: "x"}, path},
		{writeArgs{Name: "demo", Content: "no frontmatter"}, ""},
		{writeArgs{Name: "other", Content: sample}, "frontmatter name must match the skill directory"},
		{writeArgs{Name: "demo", File: "notes.md", Content: "no skill yet"}, ""},
		{writeArgs{Name: "valid", File: "notes.md", Content: strings.Repeat("x", maxFile+1)}, content},
		{writeArgs{Name: "valid", File: "notes.md", Content: string([]byte{255})}, content},
		{writeArgs{Name: "valid", File: "notes.md", Content: "a\x00b"}, content},
		{writeArgs{Name: "demo", Content: "`"}, ""},
	}
	for _, c := range cases {
		if err := l.write(c.a); err == nil || c.want != "" && err.Error() != c.want {
			t.Errorf("%q / %q: %v, want %q", c.a.Name, c.a.File, err, c.want)
		}
	}
	// A file of the most there may be is written.
	if err := l.write(writeArgs{Name: "valid", File: "notes.md", Content: strings.Repeat("x", maxFile)}); err != nil {
		t.Errorf("a file of maxFile: %v", err)
	}
}

func TestWriteDoesNotFollowLinks(t *testing.T) {
	l := library{filepath.Join(t.TempDir(), "skills"), func(string, ...any) {}}
	if err := l.write(writeArgs{Name: "demo", Content: sample}); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{outside, filepath.Join(l.dir, "linked")},
		{outside, filepath.Join(l.dir, "demo", "notes")},
		{secret, filepath.Join(l.dir, "demo", "secret")},
	} {
		if err := os.Symlink(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range []writeArgs{
		{Name: "linked", Content: strings.ReplaceAll(sample, "demo", "linked")},
		{Name: "demo", File: "notes/secret", Content: "changed"},
		{Name: "demo", File: "secret", Content: "changed", Overwrite: true},
	} {
		if err := l.write(a); err == nil {
			t.Errorf("followed symlink: %+v", a)
		}
	}
	b, err := os.ReadFile(secret)
	if err != nil || string(b) != "unchanged" {
		t.Fatalf("outside changed: %q, %v", b, err)
	}
	// Nor a link that stays in the skills directory: a skill is a directory
	// of its own, and so is each directory of a file in it.
	if err := os.Mkdir(filepath.Join(l.dir, "demo", "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"demo", filepath.Join(l.dir, "alias")}, {"sub", filepath.Join(l.dir, "demo", "inner")}} {
		if err := os.Symlink(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	for a, want := range map[writeArgs]string{
		{Name: "alias", Content: strings.ReplaceAll(sample, "demo", "alias"), Overwrite: true}: "skill directory must not be a symbolic link",
		{Name: "demo", File: "inner/notes.md", Content: "changed"}:                             "skill file directories must not be symbolic links",
	} {
		if err := l.write(a); err == nil || err.Error() != want {
			t.Errorf("%+v: %v, want %q", a, err, want)
		}
	}
	if b, err := os.ReadFile(filepath.Join(l.dir, "demo", file)); err != nil || string(b) != sample {
		t.Errorf("written through a link: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(l.dir, "demo", "sub", "notes.md")); !os.IsNotExist(err) {
		t.Errorf("written through a link: %v", err)
	}
	linkedRoot := filepath.Join(t.TempDir(), "skills")
	if err := os.Symlink(outside, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if err := (library{linkedRoot, nil}).write(writeArgs{Name: "demo", Content: sample}); err == nil {
		t.Fatal("followed skills directory link")
	}
}

// What the owner is shown of a call names the file and the skill it writes,
// SKILL.md when no file is given.
func TestShowWrite(t *testing.T) {
	for raw, want := range map[string]string{
		`{"name":"demo"}`:                         "writing SKILL.md of the skill demo",
		`{"name":"demo","file":"notes/check.md"}`: "writing notes/check.md of the skill demo",
		`{"file":"notes/check.md"}`:               "writing a skill file",
		`not JSON`:                                "writing a skill file",
	} {
		if got := showWrite(json.RawMessage(raw)); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}

// Arguments the tool cannot read are an error, and nothing is written.
func TestWriteRefusesArgumentsThatAreNone(t *testing.T) {
	l := library{filepath.Join(t.TempDir(), "skills"), nil}
	if _, err := l.writeTool().Run(context.Background(), json.RawMessage(`{"name":1}`)); err == nil {
		t.Fatal("wrote with a name that is a number")
	}
	if _, err := os.Stat(l.dir); !os.IsNotExist(err) {
		t.Fatalf("the skills directory was made: %v", err)
	}
}

// A further file needs a SKILL.md that names the skill, not just any.
func TestWriteNeedsTheSkillsOwnSkillFile(t *testing.T) {
	l := library{t.TempDir(), nil}
	for _, content := range []string{"no frontmatter", strings.ReplaceAll(sample, "name: demo", "name: other")} {
		write(t, l.dir, "demo/SKILL.md", content)
		if err := l.write(writeArgs{Name: "demo", File: "notes.md", Content: "x"}); err == nil || err.Error() != "write a valid SKILL.md first" {
			t.Errorf("with SKILL.md %q: %v", content, err)
		}
	}
	if _, err := os.Stat(filepath.Join(l.dir, "demo", "notes.md")); !os.IsNotExist(err) {
		t.Fatalf("written: %v", err)
	}
}

// locked takes the permissions mode away from dir until the test ends, and
// skips the test for a user whom permissions do not stop.
func locked(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if f, err := os.CreateTemp(dir, "probe"); err == nil {
		f.Close()
		os.Remove(f.Name())
		t.Skip("runs as a user who writes everywhere")
	}
}

// Where the file system refuses to make or open a directory or the file,
// the write fails with why.
func TestWriteWhereTheFileSystemRefuses(t *testing.T) {
	parent := t.TempDir()
	write(t, parent, "file", "")
	if err := (library{filepath.Join(parent, "file", "skills"), nil}).write(writeArgs{Name: "demo", Content: sample}); err == nil {
		t.Error("made a skills directory in a file")
	}
	for _, tt := range []struct {
		name   string
		locked string // below the skills directory
		mode   os.FileMode
		a      writeArgs
	}{
		{"skills directory unreadable", ".", 0, writeArgs{Name: "demo", Content: sample}},
		{"skills directory read-only", ".", 0o500, writeArgs{Name: "other", Content: strings.ReplaceAll(sample, "demo", "other")}},
		{"skill unreadable", "demo", 0, writeArgs{Name: "demo", Content: sample, Overwrite: true}},
		{"skill read-only, new directory", "demo", 0o500, writeArgs{Name: "demo", File: "notes/check.md", Content: "check"}},
		{"skill read-only, new file", "demo", 0o500, writeArgs{Name: "demo", Content: sample, Overwrite: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := library{t.TempDir(), nil}
			if err := l.write(writeArgs{Name: "demo", Content: sample}); err != nil {
				t.Fatal(err)
			}
			locked(t, filepath.Join(l.dir, tt.locked), tt.mode)
			if err := l.write(tt.a); err == nil || !strings.Contains(err.Error(), "permission denied") {
				t.Fatalf("%+v: %v", tt.a, err)
			}
		})
	}
}

// A file whose name the file system cannot take is an error, not a file
// that is not there yet.
func TestWriteFileNameTooLong(t *testing.T) {
	l := library{t.TempDir(), nil}
	if err := l.write(writeArgs{Name: "demo", Content: sample}); err != nil {
		t.Fatal(err)
	}
	if err := l.write(writeArgs{Name: "demo", File: strings.Repeat("x", 300), Content: "x"}); err == nil || !strings.Contains(err.Error(), "file name too long") {
		t.Fatalf("a name of 300 bytes: %v", err)
	}
}

// A write the file system refuses part way fails and leaves neither the
// file nor a temporary one. The limit on the size of files makes it fail.
func TestWriteThatFailsLeavesNothing(t *testing.T) {
	l := library{t.TempDir(), nil}
	signal.Ignore(syscall.SIGXFSZ) // the write fails instead of ending the process
	defer signal.Reset(syscall.SIGXFSZ)
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	small := limit
	small.Cur = 16
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &small); err != nil {
		t.Skip("cannot limit the size of files:", err)
	}
	err := l.write(writeArgs{Name: "demo", Content: sample})
	syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit)
	if err == nil {
		t.Fatal("a write beyond the limit should fail")
	}
	entries, err := os.ReadDir(filepath.Join(l.dir, "demo"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("left behind: %v, %v", entries, err)
	}
}
