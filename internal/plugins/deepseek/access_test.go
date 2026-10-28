package deepseek

import (
	"testing"

	"ouroboros/plugin"
)

// The key a login checks is no login that could be renewed: the provider
// for OpenAI-compatible APIs never asks, but if it did, it would fail.
func TestCheckedKeyIsNotRenewed(t *testing.T) {
	renewed := false
	err := access{plugin.Credentials{}}.Renew(func(*plugin.Credentials) error { renewed = true; return nil })
	if err == nil || renewed {
		t.Fatalf("renewed an API key: %v", err)
	}
}
