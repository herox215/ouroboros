package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestWriteRefusesInvalidFiles(t *testing.T) {
	l := library{t.TempDir(), nil}
	cases := []writeArgs{
		{Name: "../escape", Content: sample},
		{Name: "demo", File: "../escape", Content: "x"},
		{Name: "demo", File: "/tmp/escape", Content: "x"},
		{Name: "demo", File: ".", Content: "x"},
		{Name: "demo", Content: "no frontmatter"},
		{Name: "other", Content: sample},
		{Name: "demo", File: "notes.md", Content: "no skill yet"},
		{Name: "demo", Content: strings.Repeat("x", maxFile+1)},
		{Name: "demo", Content: "`"},
		{Name: "demo", Content: string([]byte{255})},
	}
	for _, a := range cases {
		if err := l.write(a); err == nil {
			t.Errorf("accepted invalid file %q / %q", a.Name, a.File)
		}
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
	linkedRoot := filepath.Join(t.TempDir(), "skills")
	if err := os.Symlink(outside, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if err := (library{linkedRoot, nil}).write(writeArgs{Name: "demo", Content: sample}); err == nil {
		t.Fatal("followed skills directory link")
	}
}
