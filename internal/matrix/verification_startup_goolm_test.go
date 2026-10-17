//go:build goolm

package matrix

import (
	"context"
	"testing"
	"time"
)

func TestVerificationPublishedDuringCryptoStartup(t *testing.T) {
	m := newMock(t)
	local := m.client(t, "@bot:test", "BOT")
	remote := m.client(t, "@bot:test", "OTHER")
	crypt := setupCrypto(t, local)
	setupCrypto(t, remote)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := crypt.initVerification(ctx, local); err != nil {
		t.Fatal(err)
	}
	// Run has published verification, but has not yet assigned its crypto field.
	local.mu.Lock()
	local.running = true
	local.crypto = nil
	local.mu.Unlock()
	defer func() { local.mu.Lock(); local.crypto = crypt; local.mu.Unlock() }()
	snap, err := local.StartVerification(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Stage != "requested" || snap.TransactionID == "" {
		t.Fatalf("startup verification: %+v", snap)
	}
	if err := local.CancelVerification(ctx, snap.TransactionID); err != nil {
		t.Fatal(err)
	}
}
