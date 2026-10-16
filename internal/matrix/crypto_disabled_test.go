//go:build !goolm

package matrix

import (
	"context"
	"strings"
	"testing"
)

func TestMissingCryptoFailsClosed(t *testing.T) {
	c := testClient(t, "https://matrix.test")
	if _, err := newCrypto(context.Background(), c); err == nil || !strings.Contains(err.Error(), "-tags=goolm") {
		t.Fatalf("want actionable E2EE build error, got %v", err)
	}
	if err := c.Send(context.Background(), "!dm:test", "must not leave"); err == nil {
		t.Fatal("sent without initialized encryption")
	}
}
