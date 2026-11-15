package history

import (
	"errors"
	"testing"
	"time"

	"ouroboros/internal/chat"
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
