package history

import "encoding/json"

// Picture is the role of the record that tells that the agent showed the owner
// a picture as the answer of the conversation went on. Its Content is the name
// of the file a front end keeps it in, see NotePicture; nothing of the picture
// itself is kept, as no other picture the owner and the agent exchange is.
const Picture = "picture"

// NotePicture keeps that the agent showed the conversation id, as List names
// it, the picture name: a front end shows it there again when it reads the
// conversation, as long as it still has the file. The record belongs to the
// answer on its way, which need not be archived yet: it is written in the
// order it happened in, before the records of that answer.
func (a *Archive) NotePicture(id, name string) error {
	if _, err := a.path(id); err != nil {
		return err
	}
	db, err := a.database()
	if err != nil {
		return err
	}
	// A deleted conversation stays deleted: a picture is no reason to list
	// it again.
	if _, err = db.Exec("INSERT INTO history_sessions(id) VALUES(?) ON CONFLICT(id) DO NOTHING", id); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var seq, answer, ordinal int
	if err = tx.QueryRow("SELECT coalesce(max(seq),0),coalesce(max(answer),0),coalesce(max(ordinal),0) FROM history_records WHERE session_id=?", id).Scan(&seq, &answer, &ordinal); err != nil {
		return err
	}
	r := Record{Seq: seq + 1, Answer: answer, Time: a.now().UTC(), Role: Picture, Content: name}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err = insertRecord(tx, id, ordinal+1, r, b); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE history_sessions SET archived=1,modified=?,size=size+? WHERE id=?", r.Time.UnixNano(), len(b)+1, id); err != nil {
		return err
	}
	return tx.Commit()
}
