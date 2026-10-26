package schedule

import (
	"context"
	"testing"
	"time"

	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	store "ouroboros/internal/schedule"
	"ouroboros/plugin"
)

type pluginSessions struct {
	plugin.Sessions
	fake *fakeSessions
}

func (p pluginSessions) RunScheduled(ctx context.Context, name, text, directory string, ask bool) (string, <-chan plugin.ScheduledResult, error) {
	return p.fake.RunScheduled(ctx, name, text, directory, ask)
}

func TestPluginCompositionAndKernelStop(t *testing.T) {
	dir := home.Dir(t.TempDir())
	st, err := store.Open(dir.Schedule())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_, err = st.Create(store.Job{Name: "Test", Text: "Fixture", At: now.Add(-time.Minute).Format(time.RFC3339), Zone: "UTC"}, now.Add(-2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSessions{calls: make(chan invocation, 5)}
	provider := plugin.NewService("fixture", func(*plugin.Context, struct{}) (struct{ Sessions plugin.Sessions }, error) {
		return struct{ Sessions plugin.Sessions }{pluginSessions{fake: f}}, nil
	})
	k := kernel.New()
	if err := k.Start(Plugin(dir), provider); err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	tools := plugin.All[plugin.Tool](k.Context())
	serving := plugin.All[plugin.Serving](k.Context())
	if len(tools) != 2 || len(serving) != 1 {
		t.Fatalf("bad contributions: %d tools, %d serving", len(tools), len(serving))
	}
	select {
	case <-f.calls:
		t.Fatal("composition dispatched before Serving.Start")
	default:
	}
	serving[0].Start()
	c := receive(t, f)
	// Kernel shutdown must also stop a scheduler when Serving.Stop was omitted.
	k.Stop()
	if c.ctx.Err() == nil {
		t.Fatal("kernel stop left scheduled answer running")
	}
	serving[0].Start()
	serving[0].Stop()
	select {
	case <-f.calls:
		t.Fatal("retired plugin restarted")
	default:
	}
}
