package plugin_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/credentials"
	"ouroboros/internal/home"
	"ouroboros/internal/llm"
	"ouroboros/plugin"
)

func TestOwnerUsage(t *testing.T) {
	dir := home.Dir(t.TempDir())
	store := dir.Credentials()
	// An access of a provider that is gone, or that tells no quota, is
	// left out, and the others are asked all the same.
	for _, a := range []credentials.Access{
		{Provider: "gone"},
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

// A configuration or accesses that cannot be read fail the report, and no
// provider is asked.
func TestOwnerUsageThatCannotBeRead(t *testing.T) {
	asked := 0
	p := plugin.Provider{Name: "quota", Usage: func(context.Context, llm.Access) ([]llm.UsageWindow, error) {
		asked++
		return nil, nil
	}}
	dir := home.Dir(t.TempDir())
	if _, err := dir.Credentials().Put(credentials.Access{Provider: "quota"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(string(dir), "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reports, err := plugin.OwnerOf(dir, nil).Usage(context.Background(), []plugin.Provider{p}); err == nil || reports != nil {
		t.Fatalf("a configuration that cannot be read: %+v, %v", reports, err)
	}
	dir = home.Dir(t.TempDir())
	if err := os.WriteFile(filepath.Join(string(dir), credentials.FileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reports, err := plugin.OwnerOf(dir, nil).Usage(context.Background(), []plugin.Provider{p}); err == nil || !strings.Contains(err.Error(), credentials.FileName) || reports != nil {
		t.Fatalf("accesses that cannot be read: %+v, %v", reports, err)
	}
	if asked != 0 {
		t.Fatalf("asked %d times", asked)
	}
}
