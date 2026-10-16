//go:build !cgo || !production

package gui

import (
	"context"
	"strings"
	"testing"
)

func TestNativeBuildRequired(t *testing.T) {
	err := showWindow(context.Background(), page{})
	if err == nil || !strings.Contains(err.Error(), "make build") {
		t.Fatalf("native build hint: %v", err)
	}
}
