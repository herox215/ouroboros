package history

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// The states of a task an agent delegated, as the archive keeps them.
const (
	TaskRunning = ""        // it ran when the archive was written last
	TaskEnded   = "done"    // it ended, and its report came, or it was stopped
	TaskRestart = "restart" // the node went away before it could report
)

// Task is a task an agent delegated in a conversation that runs in the
// background of its answer, as the archive keeps it: its number, what it was
// to do, and since when it ran. Ended tells how it ended, see the states
// above.
type Task struct {
	N     int       `json:"n"`
	Title string    `json:"title"`
	Since time.Time `json:"since"`
	Ended string    `json:"ended,omitempty"`
}

// SetTasks keeps the tasks of the conversation id that run, as running for
// the process processID: the tasks the archive knew that are not among them
// ended while it ran. A task number is given once in a conversation, so the
// archive never forgets one.
func (a *Archive) SetTasks(id string, running []Task, processID string) error {
	if _, err := a.path(id); err != nil {
		return err
	}
	if processID == "" {
		return errors.New("tasks need a process identity")
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
	kept := make(map[int]bool, len(running))
	for _, t := range running {
		kept[t.N] = true
		if _, err = tx.Exec("INSERT INTO history_tasks(session_id,n,title,since,ended,process_id) VALUES(?,?,?,?,?,?) "+
			"ON CONFLICT(session_id,n) DO UPDATE SET title=excluded.title,ended='',process_id=excluded.process_id",
			id, t.N, t.Title, t.Since.UnixNano(), TaskRunning, processID); err != nil {
			return err
		}
	}
	var ended []int
	rows, err := tx.Query("SELECT n FROM history_tasks WHERE session_id=? AND ended=''", id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n int
		if err = rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		if !kept[n] {
			ended = append(ended, n)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, n := range ended {
		if _, err = tx.Exec("UPDATE history_tasks SET ended=? WHERE session_id=? AND n=?", TaskEnded, id, n); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Tasks returns the tasks the conversation id, as List names it, delegated,
// in the order of their numbers.
func (a *Archive) Tasks(id string) ([]Task, error) {
	if _, err := a.path(id); err != nil {
		return nil, err
	}
	db, err := a.readDatabase()
	if err != nil || db == nil {
		return nil, err
	}
	rows, err := db.Query("SELECT n,title,since,ended FROM history_tasks WHERE session_id=? ORDER BY n", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		var t Task
		var since int64
		if err = rows.Scan(&t.N, &t.Title, &since, &t.Ended); err != nil {
			return nil, err
		}
		t.Since = time.Unix(0, since).UTC()
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// RestartedTasks atomically takes the tasks that ran when another executable
// instance went away: it returns them by archive session ID and ends them as
// having ended with that node, so that a session can tell them from those
// that ended while the node ran. Tasks of the process processID still run.
func (a *Archive) RestartedTasks(processID string) (map[string][]Task, error) {
	if processID == "" {
		return nil, errors.New("tasks need a process identity")
	}
	db, err := a.changeDatabase()
	if err != nil || db == nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Acquire the writer lock before selecting the tasks.
	if _, err = tx.Exec("UPDATE history_sessions SET goon=goon WHERE 0"); err != nil {
		return nil, err
	}
	rows, err := tx.Query("SELECT t.session_id,t.n,t.title,t.since FROM history_tasks t JOIN history_sessions s ON s.id=t.session_id "+
		"WHERE t.ended='' AND t.process_id<>? AND s.deleted=0 AND t.session_id LIKE 'owner/%' ORDER BY t.session_id,t.n", processID)
	if err != nil {
		return nil, err
	}
	lost := map[string][]Task{}
	for rows.Next() {
		var id string
		var t Task
		var since int64
		if err = rows.Scan(&id, &t.N, &t.Title, &since); err != nil {
			rows.Close()
			return nil, err
		}
		t.Since = time.Unix(0, since).UTC()
		lost[id] = append(lost[id], t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec("UPDATE history_tasks SET ended=? WHERE ended='' AND process_id<>? AND session_id LIKE 'owner/%'", TaskRestart, processID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return lost, nil
}

// TaskLines are the tasks as a note names them, one per line, as "task 2 ·
// find the callers".
func TaskLines(tasks []Task) string {
	lines := make([]string, len(tasks))
	for i, t := range tasks {
		lines[i] = "task " + strconv.Itoa(t.N) + " · " + t.Title
	}
	return strings.Join(lines, "\n")
}
