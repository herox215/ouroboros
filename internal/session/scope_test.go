package session

import "testing"

// scopeAgent answers with one model in the sessions and another in the thread.
type scopeAgent struct{ fakeAgent }

func (a *scopeAgent) Model(thread bool) string {
	if thread {
		return "t1"
	}
	return "m1"
}

// The model of a conversation is that of its scope: the thread's own, and the
// sessions' otherwise.
func TestModelPerScope(t *testing.T) {
	s := New(&scopeAgent{})
	t.Cleanup(s.Close)
	if got := s.model("s1"); got != "m1" {
		t.Errorf("a session answers with %q", got)
	}
	if got := s.model(ThreadID); got != "t1" {
		t.Errorf("the thread answers with %q", got)
	}
}
