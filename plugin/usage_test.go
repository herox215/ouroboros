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
	// Every access is reported, also one of a provider that is gone or that
	// tells no usage, and one that fails keeps no other from being asked.
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
	p := plugin.Provider{Name: "quota", Usage: func(ctx context.Context, a llm.Access) (llm.Usage, error) {
		calls++
		c, err := a.Credentials()
		if err != nil {
			return llm.Usage{}, err
		}
		if c.Title == "bad" {
			return llm.Usage{Windows: []llm.UsageWindow{{Name: "partial"}}}, errors.New("unavailable")
		}
		return llm.Usage{Windows: []llm.UsageWindow{{Name: "monthly", UsedPercent: 42}},
			Balances: []llm.UsageBalance{{Currency: "USD", Amount: "1.00"}}}, nil
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
	if err != nil || len(reports) != 4 || calls != 2 {
		t.Fatalf("reports %+v, calls %d, err %v", reports, calls, err)
	}
	if gone := reports[0]; !strings.Contains(gone.Err, "not part of this ouro") || gone.Unreported {
		t.Fatalf("gone: %+v", gone)
	}
	if good := reports[1]; good.Access.Label != "good" || good.Err != "" || good.Unreported || len(good.Windows) != 1 ||
		good.Windows[0].UsedPercent != 42 || len(good.Balances) != 1 || good.Balances[0].Amount != "1.00" {
		t.Fatalf("good: %+v", good)
	}
	// What a provider told along with its error is not shown.
	if bad := reports[2]; bad.Err != "unavailable" || bad.Windows != nil || bad.Unreported {
		t.Fatalf("bad: %+v", bad)
	}
	if none := reports[3]; !none.Unreported || none.Err != "" || none.Windows != nil {
		t.Fatalf("no quota: %+v", none)
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
	p := plugin.Provider{Name: "quota", Usage: func(context.Context, llm.Access) (llm.Usage, error) {
		asked++
		return llm.Usage{}, nil
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
