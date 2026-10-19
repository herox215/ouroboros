package home

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestGatewayStateIsProtected(t *testing.T) {
	d := Dir(t.TempDir())
	if got := d.Gateways(); got != filepath.Join(string(d), "gateways") {
		t.Fatal(got)
	}
	if !slices.Contains(d.Secrets(), d.Gateways()) {
		t.Fatal("gateway tokens and device keys must be protected")
	}
}
