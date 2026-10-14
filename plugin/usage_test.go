package plugin_test

import (
	"context"
	"errors"
	"testing"

	"ouroboros/internal/credentials"
	"ouroboros/internal/home"
	"ouroboros/internal/llm"
	"ouroboros/plugin"
)

func TestOwnerUsage(t *testing.T) {
	dir := home.Dir(t.TempDir())
	store := dir.Credentials()
	for _, a := range []credentials.Access{
		{Provider: "quota", Name: "good"},
		{Provider: "quota", Name: "bad"},
		{Provider: "no-quota"},
	} {
		if _, err := store.Put(a); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	p := plugin.Provider{Name: "quota", Usage: func(ctx context.Context, a llm.Access) ([]llm.UsageWindow, error) {
		calls++
		c, err := a.Credentials()
		if err != nil {
			return nil, err
		}
		if c.Title == "bad" {
			return nil, errors.New("unavailable")
		}
		return []llm.UsageWindow{{Name: "monthly", UsedPercent: 42}}, nil
	}}
	// The provider sees only credentials, not the access name.
	list, _ := store.List()
	for _, a := range list {
		a.Title = a.Name
		if _, err := store.Put(a); err != nil {
			t.Fatal(err)
		}
	}
	o := plugin.OwnerOf(dir, nil)
	reports, err := o.Usage(context.Background(), []plugin.Provider{p, {Name: "no-quota"}})
	if err != nil || len(reports) != 2 || calls != 2 {
		t.Fatalf("reports %+v, calls %d, err %v", reports, calls, err)
	}
	if reports[0].Access.Label != "good" || len(reports[0].Windows) != 1 || reports[0].Windows[0].UsedPercent != 42 {
		t.Fatalf("good: %+v", reports[0])
	}
	if reports[1].Err != "unavailable" || len(reports[1].Windows) != 0 {
		t.Fatalf("bad: %+v", reports[1])
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := o.Usage(ctx, []plugin.Provider{p}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
}
