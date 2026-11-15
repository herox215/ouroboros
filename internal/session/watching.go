package session

import "context"

// The front ends that watch a session, as WithWatchSource tells them apart.
// One that shows the thread in the window of ouro can show a picture in it,
// see PictureAPI; the page of the thread in a browser does, and the terminal
// does not.
const (
	// SourceWindow is the window of ouro, see package gui.
	SourceWindow = "gui"
	// SourcePage is the page of the thread in a browser, which is the same
	// app in another host.
	SourcePage = "web"
)

// watchSourceKey is the context key of the front end a watch is for.
type watchSourceKey struct{}

// WithWatchSource says which front end watches the session that Watch is
// called for. It is how a node tells whether a window of ouro is open, see
// Watching, and a front end that does not say is one that shows no picture.
func WithWatchSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, watchSourceKey{}, source)
}

// WatchSource is the front end ctx watches for, and "" for one that does not
// say.
func WatchSource(ctx context.Context) string {
	source, _ := ctx.Value(watchSourceKey{}).(string)
	return source
}

// ShowsThread reports whether a picture of the conversation id is seen where
// the thread is: id is the thread, or a session that works for it, whose
// pictures the thread shows too, see ShowPicture.
func (s *Service) ShowsThread(id string) bool {
	if id == ThreadID {
		return true
	}
	if err := valid(id); err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	se, ok := s.sessions[id]
	return ok && forThread(se)
}

// Watching reports how many front ends of source watch the session id now: the
// window of ouro is what tells a node that a picture can be shown in it, see
// PictureAPI. A source of "" counts the front ends that do not say.
func (s *Service) Watching(id, source string) int {
	if err := valid(id); err != nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	se, ok := s.sessions[id]
	if !ok {
		return 0
	}
	return se.watching[source]
}
