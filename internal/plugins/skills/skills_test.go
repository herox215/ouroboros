package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/plugin"
)

// write puts content into the file name below dir, with the directories it
// needs.
func write(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixture is a library with the skills review, with a further file, and
// pirate.
func fixture(t *testing.T) library {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "review/SKILL.md", "---\nname: review\ndescription: \"Review a change.\"\n---\n\nRead the diff first.\n")
	write(t, dir, "review/checklist.md", "tests\n")
	write(t, dir, "pirate/SKILL.md", "---\r\nname: pirate\r\ndescription: Talk like a pirate.\r\n---\r\nSay arr.")
	write(t, dir, "notes/README.md", "no skill")
	write(t, dir, "loose.md", "no skill either")
	return library{dir, func(string, ...any) {}}
}

// configured is the tool of l with the settings raw.
func configured(t *testing.T, l library, raw string) plugin.Tool {
	t.Helper()
	tool, err := l.tool().Configure(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

var peer = a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"})

// The description offers the skills the tool may load, and only those.
func TestDescriptionOffersTheAllowedSkills(t *testing.T) {
	l := fixture(t)
	all := configured(t, l, `{}`).Description
	if !strings.Contains(all, "\n- review: Review a change.") || !strings.Contains(all, "\n- pirate: Talk like a pirate.") || strings.Contains(all, "notes") {
		t.Errorf("every skill: %q", all)
	}
	one := configured(t, l, `{"allow": ["pirate"]}`).Description
	if strings.Contains(one, "review") || !strings.Contains(one, "pirate") {
		t.Errorf("allow pirate: %q", one)
	}
	if none := configured(t, l, `{"allow": []}`).Description; !strings.HasSuffix(none, "There are no skills.") {
		t.Errorf("allow none: %q", none)
	}
	if empty := configured(t, library{filepath.Join(t.TempDir(), "missing"), nil}, `{}`).Description; !strings.HasSuffix(empty, "There are no skills.") {
		t.Errorf("without a directory: %q", empty)
	}
}

// Settings the tool does not have, and skills that are not there, are errors.
func TestConfigureRefuses(t *testing.T) {
	l := fixture(t)
	for raw, want := range map[string]string{
		`{"allow": ["dragon"]}`: `unknown skill "dragon" (there are: pirate, review)`,
		`{"deny": ["review"]}`:  `unknown field "deny"`,
		`{"allow": "review"}`:   "cannot unmarshal",
	} {
		if _, err := l.tool().Configure(json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", raw, err, want)
		}
	}
}

// Without skills, the error says there are none; a skills directory that
// cannot be read is an error.
func TestConfigureWithoutSkills(t *testing.T) {
	if _, err := (library{t.TempDir(), nil}).tool().Configure(json.RawMessage(`{"allow": ["review"]}`)); err == nil || !strings.Contains(err.Error(), "(there are: none)") {
		t.Errorf("none: %v", err)
	}
	dir := t.TempDir()
	write(t, dir, "skills", "a file")
	if _, err := (library{filepath.Join(dir, "skills"), nil}).tool().Configure(json.RawMessage(`{}`)); err == nil {
		t.Error("a file as the directory: no error")
	}
}

// A file that cannot be read is an error.
func TestUnreadableFile(t *testing.T) {
	l := fixture(t)
	words := filepath.Join(l.dir, "review", "checklist.md")
	if err := os.Chmod(words, 0); err != nil {
		t.Fatal(err)
	}
	if f, err := os.Open(words); err == nil {
		f.Close()
		t.Skip("runs as a user who reads every file")
	}
	if _, err := configured(t, l, `{}`).Run(context.Background(), json.RawMessage(`{"name": "review", "file": "checklist.md"}`)); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("unreadable: %v", err)
	}
}

// The owner loads every skill unless the settings say otherwise; a peer only
// those allowed by name.
func TestLoad(t *testing.T) {
	l := fixture(t)
	tests := []struct {
		name     string
		ctx      context.Context
		settings string
		args     string
		want     string
		wantErr  string
	}{
		{"owner", context.Background(), `{}`, `{"name": "review"}`, "The skill review:\n\nRead the diff first.", ""},
		{"owner, file", context.Background(), `{}`, `{"name": "review", "file": "checklist.md"}`, "tests\n", ""},
		{"owner, crlf", context.Background(), `{}`, `{"name": "pirate"}`, "The skill pirate:\n\nSay arr.", ""},
		{"owner, not allowed", context.Background(), `{"allow": ["pirate"]}`, `{"name": "review"}`, "", "not allowed here"},
		{"peer without allow", peer, `{}`, `{"name": "review"}`, "", "allows by name"},
		{"peer allowed", peer, `{"allow": ["review"]}`, `{"name": "review"}`, "The skill review:\n\nRead the diff first.", ""},
		{"unknown", context.Background(), `{}`, `{"name": "notes"}`, "", `there is no skill "notes"`},
		{"no name", context.Background(), `{}`, `{}`, "", `skill needs "name", the name of the skill to load`},
		{"path as name", context.Background(), `{}`, `{"name": "../review"}`, "", "there is no skill"},
		{"out of the skill", context.Background(), `{}`, `{"name": "review", "file": "../pirate/SKILL.md"}`, "", "not a file of the skill"},
		{"absolute", context.Background(), `{}`, `{"name": "review", "file": "/etc/hosts"}`, "", "within the skill"},
		{"file of no skill", context.Background(), `{}`, `{"name": "dragon", "file": "checklist.md"}`, "", `there is no skill "dragon"`},
		{"missing file", context.Background(), `{}`, `{"name": "review", "file": "nope.md"}`, "", "no such file"},
		{"directory", context.Background(), `{}`, `{"name": "review", "file": "."}`, "", "not a file"},
		{"bad arguments", context.Background(), `{}`, `[]`, "", "invalid arguments"},
	}
	for _, tt := range tests {
		got, err := configured(t, l, tt.settings).Run(tt.ctx, json.RawMessage(tt.args))
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("%s: %q, %v, want error %q", tt.name, got, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("%s: %q, %v, want %q", tt.name, got, err, tt.want)
		}
	}
}

// No link leads out of a skill.
func TestLinkOutOfTheSkill(t *testing.T) {
	l := fixture(t)
	secret := filepath.Join(t.TempDir(), "secret")
	write(t, filepath.Dir(secret), "secret", "key")
	if err := os.Symlink(secret, filepath.Join(l.dir, "review", "link.md")); err != nil {
		t.Fatal(err)
	}
	if got, err := configured(t, l, `{}`).Run(context.Background(), json.RawMessage(`{"name": "review", "file": "link.md"}`)); err == nil || !strings.Contains(err.Error(), "not a file of the skill") {
		t.Errorf("link: %q, %v", got, err)
	}
}

// A SKILL.md that cannot be used keeps the agent from being built, and says
// why.
func TestBrokenSkill(t *testing.T) {
	for content, want := range map[string]string{
		"Read the diff first.":                                         "no frontmatter",
		"---\nname: review\ndescription: x\n":                          "no line --- that ends it",
		"---\nname: Review\ndescription: x\n---\n":                     "lowercase letters",
		"---\nname: review\n---\nRead the diff first.":                 "no description",
		"---\nname: cute\ndescription: x\n---\n":                       `names it "cute"`,
		"---\nname: review\ndescription: x\n---":                       "",
		"\ufeff---\nname: review\ndescription: x\n---":                 "",
		"---\nname: review\nmetadata:\n  - tag\ndescription: x\n---\n": "",
		"---\nname: review\ndescription: x\x00\n---\n":                 "not text",
	} {
		dir := t.TempDir()
		write(t, dir, "review/SKILL.md", content)
		_, err := library{dir, nil}.tool().Configure(json.RawMessage(`{}`))
		if want == "" {
			if err != nil {
				t.Errorf("%q: %v", content, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "skill review") || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", content, err, want)
		}
	}
}

// A file of more than maxFile is not read; one of maxFile is.
func TestLargeFile(t *testing.T) {
	l := fixture(t)
	write(t, l.dir, "review/big.md", strings.Repeat("a", maxFile+1))
	if _, err := configured(t, l, `{}`).Run(context.Background(), json.RawMessage(`{"name": "review", "file": "big.md"}`)); err == nil || !strings.Contains(err.Error(), "more than 256 KiB") {
		t.Errorf("big: %v", err)
	}
	write(t, l.dir, "review/full.md", strings.Repeat("a", maxFile))
	if got, err := configured(t, l, `{}`).Run(context.Background(), json.RawMessage(`{"name": "review", "file": "full.md"}`)); err != nil || len(got) != maxFile {
		t.Errorf("a file of maxFile: %d bytes, %v", len(got), err)
	}
}

// A value of YAML in quotes of either kind is taken without them; anything
// else is taken as it is.
func TestUnquote(t *testing.T) {
	for v, want := range map[string]string{
		`"Review a change."`: "Review a change.",
		`'Review'`:           "Review",
		`""`:                 "",
		`"`:                  `"`,
		`"open`:              `"open`,
		`"mixed'`:            `"mixed'`,
		`a plain a`:          `a plain a`,
	} {
		if got := unquote(v); got != want {
			t.Errorf("unquote(%s) = %q, want %q", v, got, want)
		}
	}
}

// Every call is logged, without what the skill says.
func TestLog(t *testing.T) {
	l := fixture(t)
	var logs []string
	l.log = func(format string, args ...any) { logs = append(logs, format) }
	tool := configured(t, l, `{}`)
	tool.Run(context.Background(), json.RawMessage(`{"name": "review"}`))
	tool.Run(peer, json.RawMessage(`{"name": "review"}`))
	if len(logs) != 2 || strings.Contains(logs[0], "failed") || !strings.Contains(logs[1], "failed") {
		t.Errorf("logs: %q", logs)
	}
}

// The owner sees which skill is loaded.
func TestShow(t *testing.T) {
	for raw, want := range map[string]string{
		`{"name": "review"}`:                         "loading the skill review",
		`{"name": "review", "file": "checklist.md"}`: "reading checklist.md of the skill review",
		`{}`: "loading a skill",
		`[]`: "loading a skill",
	} {
		if got := show(json.RawMessage(raw)); got != want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}
