package tui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/tui"
	"ouroboros/plugin"
)

// handedOver is the value of resumeEnv with which a restart starts the new
// process, to go on from r.
func handedOver(t *testing.T, r resume) string {
	t.Helper()
	restarting, ok := errors.AsType[*plugin.Restarting](handOver("/new/ouro", r))
	if !ok {
		t.Fatal("ouro does not start anew")
	}
	value := ""
	for _, kv := range restarting.Env {
		if v, ok := strings.CutPrefix(kv, resumeEnv+"="); ok {
			value = v
		}
	}
	return value
}

// resumedFrom is what the UI of the new process reads of value.
func resumedFrom(t *testing.T, value string) *resume {
	t.Helper()
	t.Setenv(resumeEnv, value)
	r, err := resumed()
	if err != nil || r == nil || r.Session != "c1" {
		t.Fatalf("got %+v, %v", r, err)
	}
	return r
}

// A draft too long for the environment goes over in a file that only the
// owner may read, and the new process removes it. Where no file can be
// written or read, ouro starts anew all the same, without the draft; a file
// that is none of the drafts is left alone.
func TestLongDraftGoesInAFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	// Escaped as JSON, it is three times as long: more than Linux takes.
	long := tui.Draft{Text: strings.Repeat("<a & b>\n", maxResumeEnv/4), Images: []string{"/tmp/picture.png"}}
	value := handedOver(t, resume{Session: "c1", Draft: &long})
	var handed resume
	json.Unmarshal([]byte(value), &handed)
	info, err := os.Stat(handed.DraftFile)
	if len(value) > maxResumeEnv || handed.Draft != nil || err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%d bytes in the environment, the file %q: %v", len(value), handed.DraftFile, err)
	}
	if d := resumedFrom(t, value).Draft; d == nil || d.Text != long.Text || !slices.Equal(d.Images, long.Images) {
		t.Fatal("the draft does not go over")
	}
	if left, _ := os.ReadDir(tmp); len(left) > 0 {
		t.Fatalf("left behind: %v", left)
	}

	broken := filepath.Join(tmp, draftPrefix+"1.json")
	others := []string{filepath.Join(t.TempDir(), draftPrefix+"2.json"), filepath.Join(tmp, "notes.json")}
	for _, path := range append([]string{broken}, others...) {
		if err := os.WriteFile(path, []byte(`{"text":`), 0o600); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(resume{Session: "c1", DraftFile: path})
		if d := resumedFrom(t, string(b)).Draft; d != nil {
			t.Fatalf("%s: the draft is %+v", path, d)
		}
	}
	if _, err := os.Stat(broken); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a draft that cannot be read stays: %v", err)
	}
	for _, path := range others {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("a file that is no draft is removed: %v", err)
		}
	}

	t.Setenv("TMPDIR", filepath.Join(tmp, "missing"))
	value = handedOver(t, resume{Session: "c1", Draft: &long})
	if r := resumedFrom(t, value); len(value) > maxResumeEnv || r.Draft != nil || r.DraftFile != "" {
		t.Fatalf("%d bytes in the environment, the file %q", len(value), r.DraftFile)
	}
}
