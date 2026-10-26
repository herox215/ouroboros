package config

import "testing"

// A scope that has an access and a model of its own is answered with those,
// and with the node's own otherwise: a scope with only one of the two is no
// choice.
func TestSessionAndThreadLLM(t *testing.T) {
	base := Config{Access: "a", Model: "m"}
	for _, tt := range []struct {
		name     string
		cfg      Config
		sessions [2]string
		thread   [2]string
	}{
		{"neither has its own", base, [2]string{"a", "m"}, [2]string{"a", "m"}},
		{"the sessions", Config{Access: "a", Model: "m", SessionAccess: "b", SessionModel: "s"}, [2]string{"b", "s"}, [2]string{"a", "m"}},
		{"the thread", Config{Access: "a", Model: "m", ThreadAccess: "c", ThreadModel: "t"}, [2]string{"a", "m"}, [2]string{"c", "t"}},
		{"one of the two is no choice", Config{Access: "a", Model: "m", SessionAccess: "b", ThreadModel: "t"}, [2]string{"a", "m"}, [2]string{"a", "m"}},
		{"only the model of the sessions", Config{Access: "a", Model: "m", SessionModel: "s"}, [2]string{"a", "m"}, [2]string{"a", "m"}},
		{"only the access of the thread", Config{Access: "a", Model: "m", ThreadAccess: "c"}, [2]string{"a", "m"}, [2]string{"a", "m"}},
		{"both scopes", Config{Access: "a", Model: "m", SessionAccess: "b", SessionModel: "s", ThreadAccess: "c", ThreadModel: "t"}, [2]string{"b", "s"}, [2]string{"c", "t"}},
	} {
		if got, model, own := tt.cfg.SessionLLM(); own != (tt.sessions != [2]string{"a", "m"}) || [2]string{got, model} != tt.sessions {
			t.Errorf("%s: sessions %q of %q (own %v)", tt.name, model, got, own)
		}
		if got, model, own := tt.cfg.ThreadLLM(); own != (tt.thread != [2]string{"a", "m"}) || [2]string{got, model} != tt.thread {
			t.Errorf("%s: thread %q of %q (own %v)", tt.name, model, got, own)
		}
	}
}
