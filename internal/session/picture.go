package session

import (
	"context"
	"errors"
	"regexp"

	"ouroboros/internal/history"
)

// PictureAPI optionally extends API with the pictures an agent shows the owner
// in a conversation, like a screenshot of what it did: the front end that
// keeps the file shows it in the chat. Service and Client implement it; a
// text-only backend need not.
type PictureAPI interface {
	// ShowPicture tells the conversation id that its agent shows the owner
	// the picture name in the chat: the front end that keeps the file shows
	// it there as the answer goes on. Where the conversation works for the
	// thread, the thread shows it too, see ThreadID.
	ShowPicture(ctx context.Context, id, name string) error
}

// pictureName is what the name of a picture may be: a file name of letters,
// digits, - and _, a dot and the kind, as the front end that keeps it makes
// it. No path, and nothing to climb out of the directory with.
var pictureName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,120}\.[A-Za-z0-9]{1,8}$`)

// ErrPicture means that a picture has no name a front end keeps pictures
// under.
var ErrPicture = errors.New("the picture needs a name of letters, digits, - and _, a dot and its kind")

// ShowPicture shows the picture name in the conversation id, as the answer on
// its way shows it. The picture itself stays where the front end keeps it: it
// is not read, copied or sent to the model, and only its name is kept with the
// answer, so that the conversation shows it again when it is read.
func (s *Service) ShowPicture(_ context.Context, id, name string) error {
	if err := valid(id); err != nil {
		return err
	}
	if !pictureName.MatchString(name) {
		return ErrPicture
	}
	s.mu.Lock()
	se := s.lookup(id)
	if se == nil {
		s.mu.Unlock()
		return ErrNoSession
	}
	// Which conversations show the picture is read while the lock is held;
	// nothing is written there: the archive is a database, and one that is busy
	// would hold up Ask, Watch and Stop of every session in the node.
	shown := []*session{se}
	if se.id != ThreadID && forThread(se) {
		// The session reaches the owner through the thread, and the thread is
		// what the window shows: the picture is shown there, and kept with the
		// thread, where it was seen.
		shown = append(shown, s.get(ThreadID))
	}
	s.mu.Unlock()
	// The archive is written with the lock of no session held: one that is busy
	// would otherwise hold up Ask, Watch and Stop of every session in the node.
	// Then it is told, so that a front end that shows the picture can find it
	// in the archive.
	for _, se := range shown {
		s.keepPicture(se, name)
	}
	s.mu.Lock()
	for _, se := range shown {
		s.emit(se, Event{Kind: Picture, Text: name})
	}
	s.mu.Unlock()
	return nil
}

// keepPicture keeps name with the answer on its way in se, which need not be
// archived yet: the front ends show the picture again once the conversation is
// read from the archive. It is called with s.mu released.
func (s *Service) keepPicture(se *session, name string) {
	if s.archive == nil {
		return
	}
	if err := s.archive.NotePicture(history.Owner(se.id), name); err != nil {
		s.archive.log("the picture %s of the session %s cannot be kept: %v", name, se.id, err)
	}
}
