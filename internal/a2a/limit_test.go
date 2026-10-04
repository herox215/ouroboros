package a2a

import (
	"context"
	"fmt"
	"testing"
	"time"

	"ouroboros/internal/identity"
)

func TestLimiter(t *testing.T) {
	var l limiter
	now := time.Now()
	for i := range strangerLimit {
		if !l.allow("10.0.0.1", now) {
			t.Fatalf("message %d should be taken", i+1)
		}
	}
	if l.allow("10.0.0.1", now.Add(strangerWindow-time.Second)) {
		t.Fatal("more than the limit within the window")
	}
	if !l.allow("10.0.0.2", now) {
		t.Fatal("another address has its own limit")
	}
	if !l.allow("10.0.0.1", now.Add(strangerWindow)) {
		t.Fatal("the next window should start anew")
	}
}

// The addresses that are counted are bounded as well: beyond the bound a new
// address is refused instead of being counted.
func TestLimiterBoundsAddresses(t *testing.T) {
	var l limiter
	now := time.Now()
	for i := range maxStrangerAddrs {
		if !l.allow(fmt.Sprint("host", i), now) {
			t.Fatalf("address %d should be taken", i+1)
		}
	}
	if l.allow("one more", now) {
		t.Fatal("an address beyond the bound was counted")
	}
	if !l.allow("host0", now) {
		t.Fatal("an address that is counted keeps its limit")
	}
	if len(l.count) != maxStrangerAddrs {
		t.Fatalf("%d addresses counted, want %d", len(l.count), maxStrangerAddrs)
	}
	if !l.allow("one more", now.Add(strangerWindow)) || len(l.count) != 1 {
		t.Fatalf("the next window should forget the addresses: %d counted", len(l.count))
	}
}

// Keys are free, so the connection requests one address can leave are
// limited. Whoever is connected or blocked is not affected.
func TestStrangersAreLimitedByAddress(t *testing.T) {
	alice, bob, carol := newNode(t, "alice"), newNode(t, "bob"), newNode(t, "carol")
	alice.trust.AddTrusted(bob.id.Public(), "bob", "")
	alice.trust.AddIncoming(carol.id.Public(), "carol", "", "")
	alice.trust.Reject(carol.id.Public())
	ctx := context.Background()
	stranger := func() error {
		_, err := (&Client{Identity: identity.Generate()}).Send(ctx, alice.srv.URL, alice.id.Public(), "c", "hi")
		return err
	}
	for i := range strangerLimit {
		if err := stranger(); !IsPending(err) {
			t.Fatalf("stranger %d: %v", i+1, err)
		}
	}
	if err := stranger(); !code(err, CodeTooManyRequests) {
		t.Fatalf("one stranger too many: %v", err)
	}
	if d, _ := alice.trust.Snapshot(); len(d.Incoming) != strangerLimit {
		t.Fatalf("%d requests recorded, want %d", len(d.Incoming), strangerLimit)
	}
	if got, err := bob.client().Send(ctx, alice.srv.URL, alice.id.Public(), "c", "hi"); err != nil || got != "c:hi" {
		t.Fatalf("a connected peer from the same address: %q, %v", got, err)
	}
	if _, err := carol.client().Send(ctx, alice.srv.URL, alice.id.Public(), "c", "hi"); !IsBlocked(err) {
		t.Fatalf("a blocked peer from the same address: %v", err)
	}
}
