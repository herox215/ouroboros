package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
)

// RecordPage is a bounded slice of a conversation in archive order.
type RecordPage struct {
	Records []Record `json:"records"`
	Next    int      `json:"next"`
	More    bool     `json:"more"`
}

// Page reads records after the zero-based record offset, not a sequence number.
// Pages hold at most 40 records and approximately 128 KiB of JSON. A single
// oversized record is returned whole so that every continuation makes progress.
func (a *Archive) Page(id string, after int) (RecordPage, error) {
	out := RecordPage{Records: []Record{}, Next: after}
	if after < 0 {
		return RecordPage{}, fmt.Errorf("invalid archive record offset %d", after)
	}
	path, err := a.path(id)
	if err != nil {
		return RecordPage{}, err
	}
	skipped, size := 0, 0
	var encodeErr error
	err = a.records(path, func(r Record) bool {
		if skipped < after {
			skipped++
			return true
		}
		if len(out.Records) == 40 {
			out.More = true
			return false
		}
		var b []byte
		b, encodeErr = json.Marshal(r)
		if encodeErr != nil {
			return false
		}
		if len(out.Records) > 0 && size+len(b)+1 > 128*1024 {
			out.More = true
			return false
		}
		out.Records = append(out.Records, r)
		size += len(b) + 1
		return true
	})
	if errors.Is(err, fs.ErrNotExist) {
		return RecordPage{}, fmt.Errorf("%w: %s", ErrNotArchived, id)
	}
	if err != nil {
		return RecordPage{}, err
	}
	if encodeErr != nil {
		return RecordPage{}, encodeErr
	}
	out.Next += len(out.Records)
	return out, nil
}
