package kernel

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

type (
	greeter interface{ Greet() string }
	english struct{}
	word    string
)

func (english) Greet() string { return "hello" }

// A plugin that uses a service is started after the plugin that provides it,
// whatever the order they are listed in.
func TestOrderFollowsServices(t *testing.T) {
	k := New()
	starts := 0
	user := Plugin{"user", func(ctx *Context) error {
		starts++
		Add(ctx, word("before"))
		g, err := Use[greeter](ctx)
		if err != nil {
			return err
		}
		Add(ctx, word(g.Greet()))
		return nil
	}}
	provider := Plugin{"provider", func(ctx *Context) error {
		Provide[greeter](ctx, english{})
		return nil
	}}
	if err := k.Start(user, provider); err != nil {
		t.Fatal(err)
	}
	if got := k.Plugins(); !slices.Equal(got, []string{"provider", "user"}) || starts != 2 {
		t.Fatalf("started %v, user %d times", got, starts)
	}
	// What the first attempt of user added was taken back.
	if got := All[word](k.Context()); !slices.Equal(got, []word{"before", "hello"}) {
		t.Fatalf("items %v", got)
	}
}

func TestMissingService(t *testing.T) {
	k := New()
	err := k.Start(
		Plugin{"fine", func(ctx *Context) error { Add(ctx, word("x")); return nil }},
		Plugin{"lonely", func(ctx *Context) error { _, err := Use[greeter](ctx); return err }},
	)
	if err == nil || !strings.Contains(err.Error(), "plugin lonely needs kernel.greeter") {
		t.Fatalf("got %v", err)
	}
	if len(k.Plugins()) != 0 || len(All[word](k.Context())) != 0 {
		t.Fatal("a failed start should leave nothing behind")
	}
}

func TestStopTakesEverythingBack(t *testing.T) {
	k := New()
	var stopped []string
	plugin := func(name string) Plugin {
		return Plugin{name, func(ctx *Context) error {
			Add(ctx, word(name))
			ctx.OnStop(func() { stopped = append(stopped, name) })
			return nil
		}}
	}
	if err := k.Start(plugin("a"), plugin("b")); err != nil {
		t.Fatal(err)
	}
	k.Stop()
	if !slices.Equal(stopped, []string{"b", "a"}) || len(All[word](k.Context())) != 0 {
		t.Fatalf("stopped %v, items %v", stopped, All[word](k.Context()))
	}
}

// A service cannot be replaced: the plugin that tries fails, and so does the start.
func TestServiceCannotBeReplaced(t *testing.T) {
	k := New()
	provider := func(name string) Plugin {
		return Plugin{name, func(ctx *Context) error { Provide[greeter](ctx, english{}); return nil }}
	}
	err := k.Start(provider("first"), provider("second"))
	if err == nil || !strings.Contains(err.Error(), "plugin second") || !strings.Contains(err.Error(), "provided already") {
		t.Fatalf("got %v", err)
	}
	if _, err := Use[greeter](k.Context()); err == nil {
		t.Fatal("a failed start should leave no service behind")
	}
}

func TestPluginErrors(t *testing.T) {
	boom := errors.New("boom")
	if err := New().Start(Plugin{"bad", func(*Context) error { return boom }}); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	ok := Plugin{"same", func(*Context) error { return nil }}
	if err := New().Start(ok, ok); err == nil {
		t.Fatal("two plugins with one name should fail")
	}
	if err := New().Start(Plugin{Name: "empty"}); err == nil {
		t.Fatal("a plugin without Start should fail")
	}
}

// A plugin started later cannot take the name of one that runs already; the
// refused start changes nothing.
func TestSecondStart(t *testing.T) {
	k := New()
	plugin := func(name string) Plugin {
		return Plugin{name, func(ctx *Context) error { Add(ctx, word(name)); return nil }}
	}
	if err := k.Start(plugin("a")); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(plugin("b"), plugin("a")); err == nil || !strings.Contains(err.Error(), "plugin a is listed twice") {
		t.Fatalf("got %v", err)
	}
	if got := All[word](k.Context()); !slices.Equal(got, []word{"a"}) {
		t.Fatalf("a refused start should change nothing: %v", got)
	}
	if err := k.Start(plugin("b")); err != nil || !slices.Equal(k.Plugins(), []string{"a", "b"}) {
		t.Fatalf("started %v, %v", k.Plugins(), err)
	}
	if err := New().Start(Plugin{Start: func(*Context) error { return nil }}); err == nil {
		t.Fatal("a plugin without a name should fail")
	}
}
