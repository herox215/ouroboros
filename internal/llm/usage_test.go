package llm

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"ouroboros/internal/credentials"
)

func TestBookUsage(t *testing.T) {
	store := credentials.Open(t.TempDir())
	saved, err := store.Put(credentials.Access{Provider: "quota", Credentials: Credentials{About: "own access"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []UsageWindow{{Name: "weekly", UsedPercent: 120, ResetsAt: time.Now()}}
	ctx := t.Context()
	failure := errors.New("quota failed")
	var returned error
	p := Provider{Name: "quota", Usage: func(got context.Context, a Access) ([]UsageWindow, error) {
		c, err := a.Credentials()
		if got != ctx || err != nil || c.About != "own access" {
			t.Fatalf("wrong context or access: %+v, %v", c, err)
		}
		return want, returned
	}}
	b := Book{Store: store, Providers: func() []Provider { return []Provider{p} }}
	if got, err := b.Usage(ctx, saved.ID); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("usage: %+v, %v", got, err)
	}
	returned = failure
	if _, err := b.Usage(ctx, saved.ID); !errors.Is(err, failure) {
		t.Fatalf("provider error: %v", err)
	}
	p.Usage = nil
	if got, err := b.Usage(ctx, saved.ID); got != nil || !errors.Is(err, ErrUsageUnavailable) {
		t.Fatalf("unavailable: %+v, %v", got, err)
	}
	if _, err := b.Usage(ctx, "missing"); !errors.Is(err, credentials.ErrGone) {
		t.Fatalf("missing access: %v", err)
	}
	p.Name = "other"
	if _, err := b.Usage(ctx, saved.ID); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("missing provider: %v", err)
	}
}
