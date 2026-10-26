package assistant

import (
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/identity"
)

// Model tells the model of the scope: the one the owner chose for it, or
// else their own.
func TestModelOfAScope(t *testing.T) {
	cfg := config.Config{Access: "a", Model: "m", Name: "me", PrivateKey: identity.Generate().Private(),
		SessionAccess: "b", SessionModel: "s", ThreadAccess: "c", ThreadModel: "t"}
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{}, discard)
	if got := a.Model(false); got != "s" {
		t.Errorf("sessions answer with %q", got)
	}
	if got := a.Model(true); got != "t" {
		t.Errorf("the thread answers with %q", got)
	}
}
