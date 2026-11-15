package history

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"ouroboros/internal/chat"
	"ouroboros/internal/storage"
)

// pictureName is a name as a front end keeps pictures: a hash of what they are
// and their kind.
func pictureName(c byte) string {
	const digits = "0123456789abcdef"
	name := make([]byte, 64)
	for i := range name {
		name[i] = digits[int(c)%16]
	}
	return string(name) + ".png"
}

// say archives one answer of the conversation id, as the session does when its
// answer ended.
func say(t *testing.T, a *Archive, id, text string) {
	t.Helper()
	if err := a.Append(Who{Session: id}, []chat.Message{
		{Role: "user", Content: text},
		{Role: "assistant", Content: "done: " + text},
	}, nil); err != nil {
		t.Fatal(err)
	}
}

func recordsOf(t *testing.T, a *Archive, id string) []Record {
	t.Helper()
	records, err := a.Records(Owner(id))
	if err != nil {
		t.Fatal(err)
	}
	return records
}

// A picture shown while the conversation answers belongs to that answer, and
// carries its number: it is no answer of its own.
func TestPictureBelongsToTheAnswerItWasShownIn(t *testing.T) {
	a := Open(t.TempDir())
	say(t, a, "s1", "first")
	if err := a.BeginAnswer(Owner("s1")); err != nil {
		t.Fatal(err)
	}
	if err := a.NotePicture(Owner("s1"), pictureName('a')); err != nil {
		t.Fatal(err)
	}
	say(t, a, "s1", "second")
	if err := a.EndAnswer(Owner("s1")); err != nil {
		t.Fatal(err)
	}
	records := recordsOf(t, a, "s1")
	// user, assistant, end of the first answer, the picture of the second, and
	// the second answer's own records.
	if len(records) != 7 {
		t.Fatalf("records %d: %+v", len(records), records)
	}
	picture := records[3]
	if picture.Role != Picture || picture.Answer != 2 || picture.Seq != 4 {
		t.Fatalf("the picture is kept as %+v", picture)
	}
	if records[4].Role != "user" || records[4].Answer != 2 || records[6].Answer != 2 {
		t.Fatalf("the answer after it is %+v", records[4:])
	}
}

// A picture shown while the conversation is idle belongs to the answer that
// ended before it, and is read where it was shown: before the next message,
// not at the end of the answer that follows.
func TestPictureShownWhileIdleIsReadWhereItWasShown(t *testing.T) {
	a := Open(t.TempDir())
	say(t, a, "s1", "first")
	if err := a.NotePicture(Owner("s1"), pictureName('b')); err != nil {
		t.Fatal(err)
	}
	say(t, a, "s1", "second")
	records := recordsOf(t, a, "s1")
	if len(records) != 7 {
		t.Fatalf("records %d: %+v", len(records), records)
	}
	if picture := records[3]; picture.Role != Picture || picture.Answer != 1 {
		t.Fatalf("the picture is kept as %+v", picture)
	}
	if records[4].Role != "user" || records[4].Answer != 2 {
		t.Fatalf("the next answer is %+v", records[4:])
	}
	// Read back, the picture is after the first answer and before the message
	// that asked for the second.
	var shown []string
	for _, r := range records {
		switch r.Role {
		case Picture:
			shown = append(shown, "picture")
		case "user":
			shown = append(shown, "message")
		case "assistant":
			shown = append(shown, "answer")
		}
	}
	if got := shown; len(got) != 5 || got[2] != "picture" || got[3] != "message" {
		t.Fatalf("the archive shows %v", got)
	}
}

// A deleted conversation stays deleted: a picture is no reason to list it
// again, and none of it is written.
func TestPictureLeavesADeletedConversationDeleted(t *testing.T) {
	a := Open(t.TempDir())
	say(t, a, "s1", "first")
	if err := a.Delete(Owner("s1")); err != nil {
		t.Fatal(err)
	}
	if err := a.NotePicture(Owner("s1"), pictureName('c')); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Records(Owner("s1")); !errors.Is(err, ErrNotArchived) {
		t.Fatalf("a deleted conversation is listed again: %v", err)
	}
	// Its name taken again by a new conversation: the old picture is not at
	// the start of it.
	if err := a.GoOn(Owner("s1")); err != nil {
		t.Fatal(err)
	}
	say(t, a, "s1", "again")
	for _, r := range recordsOf(t, a, "s1") {
		if r.Role == Picture {
			t.Fatalf("the picture of the deleted conversation came back: %+v", r)
		}
	}
}

// A picture is kept in the order it happened, and its own time.
func TestPictureIsKeptWithItsTime(t *testing.T) {
	a := Open(t.TempDir())
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return at }
	if err := a.NotePicture(Owner("s1"), pictureName('d')); err != nil {
		t.Fatal(err)
	}
	records := recordsOf(t, a, "s1")
	if len(records) != 1 || records[0].Role != Picture || !records[0].Time.Equal(at) || records[0].Answer != 1 {
		t.Fatalf("the picture is kept as %+v", records)
	}
}

// A marker written while an answer is on its way takes the number of the answer
// that ended before it, not the one the picture already carries: otherwise the
// answer that follows would skip a number, and the picture would stand behind
// the marker when the conversation is read.
func TestMarkerAfterAPictureKeepsTheAnswerNumber(t *testing.T) {
	a := Open(t.TempDir())
	defer a.Close()
	say(t, a, "s1", "first")
	if err := a.BeginAnswer(Owner("s1")); err != nil {
		t.Fatal(err)
	}
	if err := a.NotePicture(Owner("s1"), pictureName('f')); err != nil {
		t.Fatal(err)
	}
	if err := a.PendingRestart(Owner("s1"), "old", "restart-id", "/new/ouro"); err != nil {
		t.Fatal(err)
	}
	completed, err := a.CompleteRestarts("new-image")
	if err != nil {
		t.Fatal(err)
	}
	if marker := completed[Owner("s1")]; marker.Answer != 1 {
		t.Fatalf("the marker takes the answer %d, not the one that ended", marker.Answer)
	}
	say(t, a, "s1", "second")
	records := recordsOf(t, a, "s1")
	// The first answer, the picture of the second, the marker, and the second
	// answer itself.
	if len(records) != 8 {
		t.Fatalf("records %d: %+v", len(records), records)
	}
	picture, marker := records[3], records[4]
	if picture.Role != Picture || picture.Answer != 2 {
		t.Fatalf("the picture is kept as %+v", picture)
	}
	if marker.Role != Restarted || marker.Answer != 1 {
		t.Fatalf("the marker is kept as %+v", marker)
	}
	if last := records[7]; last.Role != "end" || last.Answer != 2 {
		t.Fatalf("the answer after them is %+v", records[5:])
	}
}

// A conversation that is deleted and then answers again is live: a picture it
// shows while that answer is on its way is kept with it, and the archive says
// it is there again.
func TestPictureOfAConversationThatAnswersAgainIsKept(t *testing.T) {
	a := Open(t.TempDir())
	defer a.Close()
	say(t, a, "s1", "first")
	if err := a.Delete(Owner("s1")); err != nil {
		t.Fatal(err)
	}
	// The owner starts the same conversation again, and it answers: the answer
	// on its way is what brings it back.
	if err := a.BeginAnswer(Owner("s1")); err != nil {
		t.Fatal(err)
	}
	if err := a.NotePicture(Owner("s1"), pictureName('g')); err != nil {
		t.Fatal(err)
	}
	say(t, a, "s1", "again")
	records := recordsOf(t, a, "s1")
	// The deleted answer is gone with its records: the one that is answered
	// again is the first of the conversation as it is now.
	if last := records[len(records)-1]; last.Role != "end" || last.Answer != 1 {
		t.Fatalf("the answer is kept as %+v", last)
	}
	picture := records[0]
	if len(records) != 4 || picture.Role != Picture || picture.Answer != 1 {
		t.Fatalf("the picture of the answer is not kept: %+v", records)
	}
}

// A database from before the role was a column gets it when it is opened, and
// what a record is is read from its data once: the numbers of the answers that
// follow a picture are the same as in one written since.
func TestRoleColumnOfAnOlderArchive(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, storage.FileName))
	if err != nil {
		t.Fatal(err)
	}
	old := `
CREATE TABLE history_sessions(
 id TEXT PRIMARY KEY, archived INTEGER NOT NULL DEFAULT 0,
 deleted INTEGER NOT NULL DEFAULT 0, modified INTEGER NOT NULL DEFAULT 0,
 size INTEGER NOT NULL DEFAULT 0, title TEXT, effort TEXT, model TEXT, directory TEXT,
 goon INTEGER NOT NULL DEFAULT 0, answering INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE history_records(
 rowid INTEGER PRIMARY KEY, session_id TEXT NOT NULL,
 ordinal INTEGER NOT NULL, seq INTEGER NOT NULL, answer INTEGER NOT NULL,
 data TEXT NOT NULL, search_text TEXT NOT NULL, UNIQUE(session_id,ordinal)
);`
	if _, err := db.Exec(old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO history_sessions(id,archived) VALUES('owner/s1',1)"); err != nil {
		t.Fatal(err)
	}
	record := func(ordinal, seq, answer int, role, content string) {
		t.Helper()
		r := Record{Seq: seq, Answer: answer, Role: role, Content: content, Time: time.Now().UTC()}
		b, _ := json.Marshal(r)
		if _, err := db.Exec("INSERT INTO history_records(session_id,ordinal,seq,answer,data,search_text) VALUES('owner/s1',?,?,?,?,?)",
			ordinal, seq, answer, string(b), content); err != nil {
			t.Fatal(err)
		}
	}
	record(1, 1, 1, "user", "first")
	record(2, 2, 1, "assistant", "one")
	record(3, 3, 1, "end", "")
	record(4, 4, 2, Picture, pictureName('h'))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	a := Open(dir)
	defer a.Close()
	if err := a.BeginAnswer(Owner("s1")); err != nil {
		t.Fatal(err)
	}
	if err := a.NotePicture(Owner("s1"), pictureName('i')); err != nil {
		t.Fatal(err)
	}
	say(t, a, "s1", "second")
	records := recordsOf(t, a, "s1")
	// The old picture belongs to answer 2, and so does the answer that follows
	// it, as in an archive that was written since.
	if picture := records[4]; picture.Role != Picture || picture.Answer != 2 {
		t.Fatalf("the old picture is kept as %+v", picture)
	}
	if last := records[len(records)-1]; last.Role != "end" || last.Answer != 2 {
		t.Fatalf("the answer after it is %+v", records[len(records)-4:])
	}
}
