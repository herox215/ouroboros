package history

import (
	"database/sql"
	"encoding/json"
)

// noteRecord appends one record of role and content to the conversation id, as
// the conversation has it now: the next sequence number, the answer it is in,
// and the next ordinal. The conversation needs not be archived yet; it is
// listed from here on.
func (a *Archive) noteRecord(id, role, content string) (Record, error) {
	if _, err := a.path(id); err != nil {
		return Record{}, err
	}
	db, err := a.database()
	if err != nil {
		return Record{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO history_sessions(id) VALUES(?) ON CONFLICT(id) DO UPDATE SET deleted=0", id); err != nil {
		return Record{}, err
	}
	r, err := appendRecord(tx, id, Record{Time: a.now().UTC(), Role: role, Content: content})
	if err != nil {
		return Record{}, err
	}
	return r, tx.Commit()
}

// appendRecord writes r in tx under the numbers the conversation id has at
// that point: the next sequence number and ordinal, and its own answer, or the
// answer the conversation is in when r names none. It returns r with them. The
// conversation's size and time are kept with it, so a caller that writes more
// than one record updates them through this too.
func appendRecord(tx *sql.Tx, id string, r Record) (Record, error) {
	var seq, ordinal int
	if err := tx.QueryRow("SELECT coalesce(max(seq),0),coalesce(max(ordinal),0) FROM history_records WHERE session_id=?", id).Scan(&seq, &ordinal); err != nil {
		return Record{}, err
	}
	r.Seq = seq + 1
	if r.Answer == 0 {
		// The answer that ended last, which a record of its own belongs to —
		// a marker is no answer of its own. A picture does not count here: it
		// is kept under the answer on its way before that is written, see
		// NotePicture, and would otherwise lend its number to a marker, and
		// push the answer that follows one number further.
		var answer int
		if err := tx.QueryRow("SELECT coalesce(max(answer),0) FROM history_records WHERE session_id=? AND role<>?", id, Picture).Scan(&answer); err != nil {
			return Record{}, err
		}
		r.Answer = answer
	}
	b, err := json.Marshal(r)
	if err != nil {
		return Record{}, err
	}
	if err = insertRecord(tx, id, ordinal+1, r, b); err != nil {
		return Record{}, err
	}
	if _, err = tx.Exec("UPDATE history_sessions SET archived=1,modified=?,size=size+? WHERE id=?", r.Time.UnixNano(), len(b)+1, id); err != nil {
		return Record{}, err
	}
	return r, nil
}
