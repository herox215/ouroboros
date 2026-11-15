package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/assistant"
	"ouroboros/internal/chat"
	"ouroboros/internal/history"
)

// pictureNameOf is a name as the window keeps pictures: a hash of what they
// are and their kind.
func pictureNameOf(c byte) string { return strings.Repeat(string(c), 64) + ".png" }

// An answer of the thread shows the owner a picture: the thread tells its name,
// and the archive shows it again where it was shown.
func TestThreadShowsAPictureAndKeepsIt(t *testing.T) {
	name := pictureNameOf('a')
	var s *Service
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if err := s.ShowPicture(ctx, ThreadID, name); err != nil {
			return "", err
		}
		return "here: " + text, nil
	}}
	dir := t.TempDir()
	s = service(t, a)
	s.Archive(history.Open(dir), t.Logf, func(string) string { return "" })
	ctx := context.Background()
	_, live, err := s.Watch(ctx, ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Ask(ctx, ThreadID, "show me the screenshot"); err != nil {
		t.Fatal(err)
	}
	got := until(t, live, Answer)
	if !slices.ContainsFunc(got, func(e Event) bool { return e.Kind == Picture && e.Text == name }) {
		t.Fatalf("the thread did not show the picture: %s", kinds(got))
	}
	if last := got[len(got)-1]; last.Kind != Answer || last.Text != "here: show me the screenshot" {
		t.Fatalf("answer %+v", last)
	}
	// What the assistant keeps of the answer once it ended, as it does.
	if err := s.archive.Append(history.Who{Session: ThreadID}, []chat.Message{
		{Role: "user", Content: "show me the screenshot"},
		{Role: "assistant", Content: "here: show me the screenshot"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	records, err := s.archive.Records(history.Owner(ThreadID))
	if err != nil {
		t.Fatal(err)
	}
	events, _ := replay(records)
	// The picture is where it was shown: after what asked for it, before the
	// answer text the archive writes once the answer ended.
	if got := kinds(events); got != "message picture answer" {
		t.Fatalf("the archive shows %s", got)
	}
	if events[1].Text != name {
		t.Fatalf("the archive shows %q, not %q", events[1].Text, name)
	}
}

// A session that works for the thread shows its pictures there: the window
// shows the thread, and what the session shows is the owner's to see.
func TestPictureOfWorkThatWorksForTheThreadIsShownThere(t *testing.T) {
	name := pictureNameOf('b')
	var s *Service
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if strings.Contains(text, "take the screenshot") {
			if err := s.ShowPicture(ctx, "s1", name); err != nil {
				return "", err
			}
		}
		return "done", nil
	}}
	dir := t.TempDir()
	s = service(t, a)
	s.Archive(history.Open(dir), t.Logf, func(string) string { return "" })
	ctx := context.Background()
	_, thread, err := s.Watch(ctx, ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	_, session, err := s.Watch(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.hand(ctx, "s1", "take the screenshot", nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := until(t, session, Picture); got[len(got)-1].Text != name {
		t.Fatalf("the session showed %q, not %q", got[len(got)-1].Text, name)
	}
	if got := until(t, thread, Picture); got[len(got)-1].Text != name {
		t.Fatalf("the thread showed %q, not %q", got[len(got)-1].Text, name)
	}
	records, err := s.archive.Records(history.Owner(ThreadID))
	if err != nil {
		t.Fatal(err)
	}
	if events, _ := replay(records); !slices.ContainsFunc(events, func(e Event) bool { return e.Kind == Picture }) {
		t.Fatalf("the archive of the thread does not show the picture: %s", kinds(events))
	}
}

// Read back from the archive, a picture is shown where it was shown.
func TestArchivedPictureIsShownAgain(t *testing.T) {
	records := []history.Record{
		{Seq: 1, Answer: 1, Role: "user", Content: "show me"},
		{Seq: 2, Answer: 1, Role: history.Picture, Content: pictureNameOf('c')},
		{Seq: 3, Answer: 1, Role: "assistant", Content: "here"},
		{Seq: 4, Answer: 1, Role: "end", Outcome: "answered"},
	}
	events, _ := replay(records)
	if got := kinds(events); got != "message picture answer" {
		t.Fatalf("events %s", got)
	}
	if events[1].Text != pictureNameOf('c') {
		t.Fatalf("picture %+v", events[1])
	}
}

// A picture is shown under the name a front end keeps pictures under, in a
// conversation that is there: nothing else comes of it.
func TestShowPictureChecksIt(t *testing.T) {
	s := service(t, &fakeAgent{})
	ctx := context.Background()
	if err := s.ShowPicture(ctx, "s1", "../../etc/passwd"); !errors.Is(err, ErrPicture) {
		t.Fatalf("a path as a name: %v", err)
	}
	if err := s.ShowPicture(ctx, "s1", "no-kind"); !errors.Is(err, ErrPicture) {
		t.Fatalf("a name without a kind: %v", err)
	}
	if err := s.ShowPicture(ctx, "s1", pictureNameOf('d')); !errors.Is(err, ErrNoSession) {
		t.Fatalf("a session that is none: %v", err)
	}
	if err := s.ShowPicture(ctx, "no such id", pictureNameOf('d')); !errors.Is(err, ErrNoSession) {
		t.Fatalf("an id that is none: %v", err)
	}
}
