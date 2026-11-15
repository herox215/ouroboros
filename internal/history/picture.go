package history

// Picture is the role of the record that tells that the agent showed the owner
// a picture as the answer of the conversation went on. Its Content is the name
// of the file a front end keeps it in, see NotePicture; nothing of the picture
// itself is kept, as no other picture the owner and the agent exchange is.
const Picture = "picture"

// NotePicture keeps that the agent showed the conversation id, as List names
// it, the picture name: a front end shows it there again when it reads the
// conversation, as long as it still has the file. The record belongs to the
// answer on its way, which need not be archived yet: it is written in the
// order it happened in, before the records of that answer, and carries the
// number of that answer already.
func (a *Archive) NotePicture(id, name string) error {
	if _, err := a.path(id); err != nil {
		return err
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
	// A deleted conversation stays deleted: a picture is no reason to list it
	// again, and a record of one would leave a conversation whose archive says
	// it has an answer, and the picture at the start of the next one that takes
	// its name. One that is answering again is not deleted: the answer on its
	// way brings it back, see append, and the picture is shown with it.
	if _, err = tx.Exec("INSERT INTO history_sessions(id) VALUES(?) ON CONFLICT(id) DO NOTHING", id); err != nil {
		return err
	}
	var deleted, answering int
	if err = tx.QueryRow("SELECT deleted,answering FROM history_sessions WHERE id=?", id).Scan(&deleted, &answering); err != nil {
		return err
	}
	if deleted != 0 {
		if answering == 0 {
			return nil // nothing of it is kept: the conversation is gone
		}
		if _, err = tx.Exec("UPDATE history_sessions SET deleted=0 WHERE id=?", id); err != nil {
			return err
		}
	}
	// The answer the picture belongs to: the one on its way while the
	// conversation answers, and otherwise the one that ended last, which the
	// picture was shown after. What ends answers is no picture, so they are not
	// counted here, see append.
	var last int
	if err = tx.QueryRow("SELECT coalesce(max(answer),0) FROM history_records WHERE session_id=? AND role<>?", id, Picture).Scan(&last); err != nil {
		return err
	}
	answer := last
	if answering != 0 {
		answer = last + 1
	}
	if answer == 0 {
		answer = 1 // shown before the first answer: it is shown with it
	}
	if _, err = appendRecord(tx, id, Record{Time: a.now().UTC(), Role: Picture, Content: name, Answer: answer}); err != nil {
		return err
	}
	return tx.Commit()
}
