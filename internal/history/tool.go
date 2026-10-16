package history

import (
	"errors"
	"fmt"
	"io/fs"
)

// ErrNoCall means that no top-level model call of this ID is archived.
// Calls made inside a script are not archived separately.
var ErrNoCall = errors.New("the archive has no such tool call (nested script calls are not archived separately)")

// ToolResult reads only the requested call and its matching result. It scans
// records one at a time and stops at the result, without retaining the session.
// Found is false when the call is archived but its result is not yet there.
func (a *Archive) ToolResult(id, callID string) (call Call, content string, found bool, err error) {
	path, err := a.path(id)
	if err != nil {
		return Call{}, "", false, err
	}
	if callID == "" {
		return Call{}, "", false, ErrNoCall
	}
	seen := false
	err = a.records(path, func(r Record) bool {
		if !seen && r.Role == "assistant" {
			for _, c := range r.ToolCalls {
				if c.ID == callID {
					call, seen = c, true
					break
				}
			}
		}
		if seen && r.Role == "tool" && r.ToolCallID == callID {
			content, found = r.Content, true
			return false
		}
		return true
	})
	if errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("%w: %s", ErrNotArchived, id)
	}
	if err == nil && !seen {
		err = fmt.Errorf("%w: %s", ErrNoCall, callID)
	}
	return
}
