package history

import "strings"

// The archive keeps which conversations had an answer on its way when the
// image of ouro that answered ended: BeginAnswer marks one, EndAnswer takes
// the mark back, and Interrupted consumes the marks left over. A mark left
// over belongs to an answer no image finished, because ouro was replaced by a
// new build, stopped or died: the session goes on with it, see
// session.Service.GoOn.

// BeginAnswer marks the conversation id, as List names it, as having an
// answer on its way. The conversation needs not be archived yet.
func (a *Archive) BeginAnswer(id string) error {
	if _, err := a.path(id); err != nil {
		return err
	}
	db, err := a.database()
	if err != nil {
		return err
	}
	_, err = db.Exec("INSERT INTO history_sessions(id,answering) VALUES(?,1) ON CONFLICT(id) DO UPDATE SET deleted=0,answering=1", id)
	return err
}

// EndAnswer takes the mark of BeginAnswer back: the answer ended, or the
// owner stopped it, in the image that made it.
func (a *Archive) EndAnswer(id string) error {
	db, err := a.readDatabase()
	if err != nil || db == nil {
		return err
	}
	_, err = db.Exec("UPDATE history_sessions SET answering=0 WHERE id=?", id)
	return err
}

// Interrupted atomically consumes the marks of the answers that were on their
// way when an image of ouro ended, and returns them: those sessions go on
// with their answers. Peer marks are not consumed here.
func (a *Archive) Interrupted() ([]string, error) {
	db, err := a.readDatabase()
	if err != nil || db == nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Acquire the writer lock before selecting marks.
	if _, err = tx.Exec("UPDATE history_sessions SET answering=answering WHERE 0"); err != nil {
		return nil, err
	}
	// The IDs are separated by spaces, which none of them holds.
	var marked string
	if err = tx.QueryRow("SELECT coalesce(group_concat(id,' ' ORDER BY id),'') FROM history_sessions WHERE answering=1 AND deleted=0 AND id LIKE 'owner/%'").Scan(&marked); err != nil {
		return nil, err
	}
	ids := strings.Fields(marked)
	if _, err = tx.Exec("UPDATE history_sessions SET answering=0 WHERE id LIKE 'owner/%'"); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}
