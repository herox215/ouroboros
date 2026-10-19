package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	store "ouroboros/internal/memory"
	"ouroboros/plugin"
)

func TestPluginLazyInvocationAndRestart(t *testing.T) {
	dir := home.Dir(t.TempDir())
	k := kernel.New()
	start := func() ([]plugin.Tool, plugin.Serving) {
		t.Helper()
		if err := k.Start(Plugin(dir)); err != nil {
			t.Fatal(err)
		}
		return plugin.All[plugin.Tool](k.Context()), plugin.All[plugin.Serving](k.Context())[0]
	}
	ts, serving := start()
	defer k.Stop()
	assertAbsent := func() {
		t.Helper()
		if _, err := os.Stat(dir.History()); !os.IsNotExist(err) {
			t.Fatalf("archive created during composition: %v", err)
		}
	}
	assertAbsent()
	serving.Start()
	assertAbsent()
	for _, tool := range ts {
		if _, err := tool.Run(context.Background(), json.RawMessage("{}")); err == nil {
			t.Fatal("unauthorized invocation accepted")
		}
	}
	assertAbsent()
	out := callTools(t, ts, context.Background(), "owner", Create, `{"key":"drink","content":"oolong tea"}`)
	var entry store.Entry
	if err := json.Unmarshal([]byte(out), &entry); err != nil {
		t.Fatalf("create: %s: %v", out, err)
	}
	if entry.Source != "owner" || entry.Version != 1 {
		t.Fatalf("bad entry: %+v", entry)
	}
	if _, err := os.Stat(dir.History()); err != nil {
		t.Fatal(err)
	}
	serving.Stop()
	serving.Stop()
	if out := callTools(t, ts, context.Background(), "owner", Search, "{}"); !strings.Contains(out, "store is stopped") {
		t.Fatalf("reopened after stop: %s", out)
	}
	serving.Start()
	if out := callTools(t, ts, context.Background(), "owner", Read, fmt.Sprintf(`{"id":%q}`, entry.ID)); !strings.Contains(out, "oolong tea") {
		t.Fatal(out)
	}
	k.Stop()        // Frontends need not call Serving.Stop.
	serving.Start() // A retained callback cannot revive a retired instance.
	if out := callTools(t, ts, context.Background(), "owner", Search, "{}"); !strings.Contains(out, "store is stopped") {
		t.Fatalf("reopened after kernel stop: %s", out)
	}
	ts, _ = start()
	if out := callTools(t, ts, context.Background(), "owner", Read, fmt.Sprintf(`{"id":%q}`, entry.ID)); !strings.Contains(out, "oolong tea") {
		t.Fatalf("restart lost memory: %s", out)
	}
}

func TestLazyStoreClosesAndConcurrentStop(t *testing.T) {
	s := &lazyStore{dir: t.TempDir()}
	ts := tools(nil)
	for i := range ts {
		ts[i].Run = func(ctx context.Context, raw json.RawMessage) (string, error) {
			return s.run(ctx, raw, i)
		}
	}
	callTools(t, ts, context.Background(), "owner", Search, "{}")
	opened := s.store
	if opened == nil {
		t.Fatal("invocation did not open store")
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Use a real conversation context, not just a reachable handler.
			bot := agent.New(model{Search, "{}"}, "m", "", ts...)
			out, err := bot.Respond(context.Background(), "owner", "search")
			if err != nil || (out != "[]" && !strings.Contains(out, "store is stopped")) {
				t.Errorf("concurrent invocation: %s %v", out, err)
			}
		}()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.stop(false)
	}()
	go func() {
		defer wg.Done()
		s.stop(true)
	}()
	wg.Wait()
	if _, err := opened.Read(context.Background(), "", "missing"); err == nil || err == store.ErrNotFound {
		t.Fatalf("old pool not closed: %v", err)
	}
	if s.store != nil {
		t.Fatal("store retained after shutdown")
	}
}

// lazyTools are the tools of the plugin on s.
func lazyTools(s *lazyStore) []plugin.Tool {
	ts := tools(nil)
	for i := range ts {
		ts[i].Run = func(ctx context.Context, raw json.RawMessage) (string, error) {
			return s.run(ctx, raw, i)
		}
	}
	return ts
}

// A call that is stopped before it starts opens no archive.
func TestStoppedCallOpensNothing(t *testing.T) {
	s := &lazyStore{dir: filepath.Join(t.TempDir(), "history")}
	probe := agent.NewTool("probe", "", nil, func(ctx context.Context, _ struct{}) (string, error) {
		stopped, cancel := context.WithCancel(ctx)
		cancel()
		_, err := s.run(stopped, json.RawMessage("{}"), 0)
		return fmt.Sprint(err), nil
	})
	if out := callTools(t, []plugin.Tool{probe}, context.Background(), "owner", "probe", "{}"); out != context.Canceled.Error() {
		t.Fatalf("stopped call: %s", out)
	}
	if _, err := os.Stat(s.dir); !os.IsNotExist(err) || s.store != nil {
		t.Fatalf("a stopped call opened the archive: %v", err)
	}
}

// A call whose archive cannot be opened fails, and the next call tries
// again.
func TestArchiveThatCannotBeOpened(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &lazyStore{dir: dir}
	defer s.stop(true)
	ts := lazyTools(s)
	if out := callTools(t, ts, context.Background(), "owner", Search, "{}"); !strings.HasPrefix(out, "error:") || s.store != nil {
		t.Fatalf("searched in an archive that cannot be: %s", out)
	}
	os.Remove(dir)
	if out := callTools(t, ts, context.Background(), "owner", Search, "{}"); out != "[]" || s.store == nil {
		t.Fatalf("search once the archive can be: %s", out)
	}
}
