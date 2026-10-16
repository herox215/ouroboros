package plugin

import (
	"time"

	"ouroboros/internal/history"
)

// ArchivedSession names a conversation of the owner or a peer in this profile.
// ID is the archive's qualified ID, including its owner or peer party.
type ArchivedSession struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Modified time.Time `json:"modified"`
}

// ArchivedRecord is a record as kept on disk, including tool calls and outcomes.
type ArchivedRecord = history.Record

// ArchivedPage is a bounded page of archived records.
type ArchivedPage = history.RecordPage

// ArchiveSessions lists every archived conversation, most recently modified
// first. It reads directly from disk without starting the node or changing it.
func (o *Owner) ArchiveSessions() ([]ArchivedSession, error) {
	archive := history.Open(o.home.History())
	defer archive.Close()
	entries, err := archive.List()
	if err != nil {
		return nil, err
	}
	out := make([]ArchivedSession, len(entries))
	for i, entry := range entries {
		out[i] = ArchivedSession{ID: entry.ID, Title: entry.Title, Modified: entry.Modified}
	}
	return out, nil
}

// ArchivePage reads a bounded page after a zero-based record offset without
// starting the node or changing the archive.
func (o *Owner) ArchivePage(id string, after int) (ArchivedPage, error) {
	archive := history.Open(o.home.History())
	defer archive.Close()
	return archive.Page(id, after)
}

// ArchiveRecords reads every record of the qualified ID ArchiveSessions lists.
// Invalid IDs and missing sessions are errors; no session is created or resumed.
func (o *Owner) ArchiveRecords(id string) ([]ArchivedRecord, error) {
	archive := history.Open(o.home.History())
	defer archive.Close()
	records, err := archive.Records(id)
	if err != nil {
		return nil, err
	}
	if records == nil {
		records = []ArchivedRecord{}
	}
	return records, nil
}
