// Package health keeps what the last run of the tests of a rebuild found: in
// cyberpsycho mode the tests of a build are a reading of the node, not a gate
// that keeps the build out, and the owner sees the reading on screen.
package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"ouroboros/internal/safefile"
)

// Record is what the last run of the tests of a rebuild found.
type Record struct {
	// Ran says that a run of tests was recorded at all.
	Ran bool `json:"ran"`
	// OK says that every package the run named passed.
	OK bool `json:"ok"`
	// Tests are the packages the run named.
	Tests []string `json:"tests,omitempty"`
	// Fail names the first failing package and test, like
	// "./internal/tui/: TestFrameHeader"; "" when nothing failed.
	Fail string `json:"fail,omitempty"`
	// At is when the run ended.
	At time.Time `json:"at"`
}

// Read returns the record at path, and false if there is none or it cannot be
// read: a record that is not there is no failure.
func Read(path string) (Record, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, false
	}
	var r Record
	if err := json.Unmarshal(data, &r); err != nil || !r.Ran {
		return Record{}, false
	}
	return r, true
}

// Write keeps r at path, replacing what was there.
func Write(path string, r Record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return safefile.Write(path, append(data, '\n'), 0o600)
}

// Line is the record in one short line for the owner: "green", "failing:
// ./internal/tui/: TestFrameHeader", or "not measured". A nil record is one
// that was never taken.
func (r *Record) Line() string {
	switch {
	case r == nil || !r.Ran:
		return "not measured"
	case r.OK:
		return "green"
	case r.Fail != "":
		return "failing: " + r.Fail
	default:
		return "failing"
	}
}
