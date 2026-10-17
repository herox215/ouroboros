package matrix

import (
	"context"
	"testing"
)

func TestVerificationRequiresRunningCrypto(t *testing.T) {
	c := testClient(t, "https://matrix.test")
	if c.VerificationStatus().Stage != "idle" {
		t.Fatal("new client is not idle")
	}
	if _, err := c.StartVerification(context.Background()); err == nil {
		t.Fatal("verification without running crypto")
	}
	if err := c.ConfirmVerification(context.Background(), "stale"); err == nil {
		t.Fatal("confirmation without running crypto")
	}
	if err := c.CancelVerification(context.Background(), "stale"); err == nil {
		t.Fatal("cancellation without running crypto")
	}
}
