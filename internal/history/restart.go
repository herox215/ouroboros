package history

import (
	"encoding/json"
	"errors"
	"strings"
)

// Restarted is the archive role for a completed process replacement. It is not
// a message to the model and does not end an answer.
const Restarted = "restarted"

// PendingRestart durably marks the triggering owner session before replacement
// is scheduled. processID identifies this executable instance (not its PID).
func (a *Archive) PendingRestart(id, processID, presentationID, binary string) error {
	if _, err := a.path(id); err != nil {
		return err
	}
	if !strings.HasPrefix(id, "owner/") || processID == "" || presentationID == "" {
		return errors.New("restart needs an owner session and process and presentation identities")
	}
	db, err := a.database()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO history_sessions(id) VALUES(?) ON CONFLICT(id) DO UPDATE SET deleted=0", id); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO history_restarts(session_id,process_id,presentation_id,binary) VALUES(?,?,?,?) ON CONFLICT(session_id) DO UPDATE SET process_id=excluded.process_id,presentation_id=excluded.presentation_id,binary=excluded.binary", id, processID, presentationID, binary); err != nil {
		return err
	}
	return tx.Commit()
}

// CompleteRestarts atomically replaces pending marks from other executable
// instances with ordinary durable archive records. Reopening a Service in the
// same instance does not complete a restart. Consumption and append share a
// transaction, so a crash cannot lose or duplicate the completion.
// The returned records are keyed by archive session ID; callers may publish
// them to already-loaded sessions only after this method succeeds.
func (a *Archive) CompleteRestarts(processID string) (map[string]Record, error) {
	if processID == "" {
		return nil, errors.New("restart needs a process identity")
	}
	db, err := a.readDatabase()
	if err != nil || db == nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE history_restarts SET process_id=process_id WHERE 0"); err != nil {
		return nil, err
	}
	rows, err := tx.Query("SELECT r.session_id,r.presentation_id,r.binary FROM history_restarts r JOIN history_sessions s ON s.id=r.session_id WHERE r.process_id<>? AND s.deleted=0 AND r.session_id LIKE 'owner/%' ORDER BY r.session_id", processID)
	if err != nil {
		return nil, err
	}
	completed := map[string]Record{}
	for rows.Next() {
		var id, presentationID, binary string
		if err = rows.Scan(&id, &presentationID, &binary); err != nil {
			rows.Close()
			return nil, err
		}
		completed[id] = Record{Role: Restarted, PresentationID: presentationID, Content: binary, Time: a.now().UTC()}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for id, r := range completed {
		var ordinal int
		if err = tx.QueryRow("SELECT coalesce(max(seq),0)+1,coalesce(max(answer),0),coalesce(max(ordinal),0)+1 FROM history_records WHERE session_id=?", id).Scan(&r.Seq, &r.Answer, &ordinal); err != nil {
			return nil, err
		}
		b, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		if err = insertRecord(tx, id, ordinal, r, b); err != nil {
			return nil, err
		}
		if _, err = tx.Exec("UPDATE history_sessions SET archived=1,modified=?,size=size+? WHERE id=?", r.Time.UnixNano(), len(b)+1, id); err != nil {
			return nil, err
		}
		if _, err = tx.Exec("DELETE FROM history_restarts WHERE session_id=?", id); err != nil {
			return nil, err
		}
		completed[id] = r
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return completed, nil
}
