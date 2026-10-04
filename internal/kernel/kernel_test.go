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
	number  int
	none    = struct{}
)

func (english) Greet() string { return "hello" }

// started counts how often each plugin was started, and in which order.
type started struct {
	order []string
	times map[string]int
}

func (s *started) note(name string) {
	if s.times == nil {
		s.times = map[string]int{}
	}
	s.order = append(s.order, name)
	s.times[name]++
}

// adds returns a plugin that needs and provides nothing and contributes its
// name.
func adds(name string) Plugin {
	return NewPlugin(name, func(ctx *Context, _ none) (none, error) {
		Add(ctx, word(name))
		return none{}, nil
	})
}

// A plugin that needs a service is started after the plugin that provides it,
// whatever the order they are listed in, and every plugin is started once.
func TestOrderFollowsDependencies(t *testing.T) {
	k := New()
	var s started
	// top needs the service of middle, which needs the one of bottom.
	top := NewPlugin("top", func(ctx *Context, n struct{ Word word }) (none, error) {
		s.note("top")
		Add(ctx, n.Word)
		return none{}, nil
	})
	middle := NewPlugin("middle", func(_ *Context, n struct{ Greeter greeter }) (struct{ Word word }, error) {
		s.note("middle")
		return struct{ Word word }{word(n.Greeter.Greet())}, nil
	})
	bottom := NewPlugin("bottom", func(*Context, none) (struct{ Greeter greeter }, error) {
		s.note("bottom")
		return struct{ Greeter greeter }{english{}}, nil
	})
	free := NewPlugin("free", func(*Context, none) (none, error) {
		s.note("free")
		return none{}, nil
	})
	// middle is in the order by the time its turn in the list comes: the
	// ones listed after it still get theirs.
	if err := k.Start(top, middle, free, bottom); err != nil {
		t.Fatal(err)
	}
	want := []string{"bottom", "middle", "top", "free"}
	if !slices.Equal(s.order, want) || !slices.Equal(k.Plugins(), want) {
		t.Fatalf("started %v, running %v, want %v", s.order, k.Plugins(), want)
	}
	for name, n := range s.times {
		if n != 1 {
			t.Errorf("%s was started %d times", name, n)
		}
	}
	// The service went through the chain: bottom's greeting, as middle's word.
	if got := All[word](k.Context()); !slices.Equal(got, []word{"hello"}) {
		t.Fatalf("items %v", got)
	}
	if g, ok := Service[greeter](k); !ok || g.Greet() != "hello" {
		t.Fatalf("service %v, %v", g, ok)
	}
}

// A plugin that needs several services waits for all of them, also when one
// of them is there already.
func TestOrderWithSeveralNeeds(t *testing.T) {
	greets := NewPlugin("greets", func(*Context, none) (struct{ Greeter greeter }, error) {
		return struct{ Greeter greeter }{english{}}, nil
	})
	words := NewPlugin("words", func(*Context, none) (struct{ Word word }, error) {
		return struct{ Word word }{"world"}, nil
	})
	type both struct {
		Greeter greeter
		Word    word
	}
	user := NewPlugin("user", func(ctx *Context, n both) (none, error) {
		Add(ctx, word(n.Greeter.Greet())+" "+n.Word)
		return none{}, nil
	})
	// Whichever of the two services is looked at first: one is in the order
	// before user is, the other one is listed after it.
	for _, list := range [][]Plugin{{greets, user, words}, {words, user, greets}} {
		k := New()
		if err := k.Start(list...); err != nil {
			t.Fatal(err)
		}
		if got := k.Plugins(); len(got) != 3 || got[2] != "user" {
			t.Fatalf("started %v, user should be last", got)
		}
		if got := All[word](k.Context()); !slices.Equal(got, []word{"hello world"}) {
			t.Fatalf("items %v", got)
		}
	}
}

// Plugins that do not depend on each other start in the order they are listed.
func TestIndependentPluginsStartAsListed(t *testing.T) {
	k := New()
	if err := k.Start(adds("c"), adds("a"), adds("b")); err != nil {
		t.Fatal(err)
	}
	if got := All[word](k.Context()); !slices.Equal(got, []word{"c", "a", "b"}) {
		t.Fatalf("items %v", got)
	}
}

// A service nobody provides is found before any plugin starts, and the error
// names every plugin that lacks one and what it lacks.
func TestMissingService(t *testing.T) {
	k := New()
	starts := 0
	fine := NewPlugin("fine", func(ctx *Context, _ none) (none, error) {
		starts++
		Add(ctx, word("x"))
		return none{}, nil
	})
	lonely := NewPlugin("lonely", func(*Context, struct{ Greeter greeter }) (none, error) {
		starts++
		return none{}, nil
	})
	lost := NewPlugin("lost", func(*Context, struct {
		Word   word
		Number number
	}) (none, error) {
		starts++
		return none{}, nil
	})
	err := k.Start(fine, lonely, lost)
	for _, want := range []string{
		"plugin lonely needs kernel.greeter, which no plugin provides",
		"plugin lost needs kernel.word, which no plugin provides",
		"plugin lost needs kernel.number, which no plugin provides",
		"add a plugin that provides it",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("got %v, want %q in it", err, want)
		}
	}
	if starts != 0 || len(k.Plugins()) != 0 || len(All[word](k.Context())) != 0 {
		t.Fatalf("an incomplete set of plugins should not be started at all: %d starts, running %v", starts, k.Plugins())
	}
	if _, ok := Service[greeter](k); ok {
		t.Fatal("there is no such service")
	}
}

// Plugins that need each other cannot be ordered: none of them is started,
// and the error names the plugins and services of the circle.
func TestCycle(t *testing.T) {
	starts := 0
	count := func() { starts++ }
	a := NewPlugin("a", func(*Context, struct{ Word word }) (struct{ Greeter greeter }, error) {
		count()
		return struct{ Greeter greeter }{english{}}, nil
	})
	b := NewPlugin("b", func(*Context, struct{ Number number }) (struct{ Word word }, error) {
		count()
		return struct{ Word word }{"b"}, nil
	})
	c := NewPlugin("c", func(*Context, struct{ Greeter greeter }) (struct{ Number number }, error) {
		count()
		return struct{ Number number }{1}, nil
	})
	// Reaches the circle without being part of it.
	outside := NewPlugin("outside", func(*Context, struct{ Greeter greeter }) (none, error) {
		count()
		return none{}, nil
	})
	k := New()
	err := k.Start(adds("free"), outside, a, b, c)
	want := "plugins need each other in a circle, so none of them can start first: " +
		"a needs kernel.word of b, b needs kernel.number of c, c needs kernel.greeter of a: take one of these needs out"
	if err == nil || err.Error() != want {
		t.Fatalf("got  %v\nwant %s", err, want)
	}
	if starts != 0 || len(k.Plugins()) != 0 || len(All[word](k.Context())) != 0 {
		t.Fatalf("plugins in a circle should not be started: %d starts, running %v", starts, k.Plugins())
	}

	// The smallest circle: a plugin that needs what it provides itself.
	self := NewPlugin("self", func(*Context, struct{ Word word }) (struct{ Word word }, error) {
		return struct{ Word word }{"x"}, nil
	})
	if err := New().Start(self); err == nil || !strings.Contains(err.Error(), "circle") || !strings.Contains(err.Error(), ": self needs kernel.word of self:") {
		t.Fatalf("got %v", err)
	}
}

func TestStopTakesEverythingBack(t *testing.T) {
	k := New()
	var stopped []string
	plugin := func(name string) Plugin {
		return NewPlugin(name, func(ctx *Context, _ none) (struct{ Greeter greeter }, error) {
			Add(ctx, word(name))
			ctx.OnStop(func() { stopped = append(stopped, name) })
			return struct{ Greeter greeter }{english{}}, nil
		})
	}
	if err := k.Start(plugin("a"), adds("b")); err != nil {
		t.Fatal(err)
	}
	k.Stop()
	if !slices.Equal(stopped, []string{"a"}) || len(All[word](k.Context())) != 0 || len(k.Plugins()) != 0 {
		t.Fatalf("stopped %v, items %v", stopped, All[word](k.Context()))
	}
	if _, ok := Service[greeter](k); ok {
		t.Fatal("the service of a stopped plugin should be gone")
	}
}

// A service cannot be replaced: two plugins that provide the same type stop
// the kernel from starting, and the error names both.
func TestServiceCannotBeReplaced(t *testing.T) {
	k := New()
	starts := 0
	provider := func(name string) Plugin {
		return NewPlugin(name, func(*Context, none) (struct{ Greeter greeter }, error) {
			starts++
			return struct{ Greeter greeter }{english{}}, nil
		})
	}
	err := k.Start(provider("first"), provider("second"))
	if err == nil || !strings.Contains(err.Error(), "kernel.greeter is provided by plugin first and by plugin second") {
		t.Fatalf("got %v", err)
	}
	if _, ok := Service[greeter](k); ok || starts != 0 {
		t.Fatal("a refused start should leave no service behind")
	}

	// Neither can a plugin that is started later take over a service.
	if err := k.Start(provider("first")); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(provider("late")); err == nil || !strings.Contains(err.Error(), "by plugin first and by plugin late") {
		t.Fatalf("got %v", err)
	}
}

func TestPluginErrors(t *testing.T) {
	boom := errors.New("boom")
	k := New()
	stopped := false
	fine := NewPlugin("fine", func(ctx *Context, _ none) (struct{ Greeter greeter }, error) {
		ctx.OnStop(func() { stopped = true })
		return struct{ Greeter greeter }{english{}}, nil
	})
	bad := NewPlugin("bad", func(ctx *Context, _ none) (struct{ Word word }, error) {
		Add(ctx, word("bad"))
		return struct{ Word word }{"bad"}, boom
	})
	err := k.Start(fine, bad)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "plugin bad") {
		t.Fatalf("got %v", err)
	}
	if _, ok := Service[word](k); ok || !stopped || len(k.Plugins()) != 0 || len(All[word](k.Context())) != 0 {
		t.Fatal("a failed start should leave nothing behind")
	}

	if err := New().Start(adds("same"), adds("same")); err == nil || !strings.Contains(err.Error(), "plugin same is listed twice") {
		t.Fatalf("two plugins with one name: %v", err)
	}
	for name, p := range map[string]Plugin{
		"no start function": NewPlugin[none, none]("empty", nil),
		"made by hand":      {Name: "empty"},
		"no name":           adds(""),
	} {
		if err := New().Start(p); err == nil || !strings.Contains(err.Error(), "needs a name and a start function") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

// What a plugin needs and provides has to be readable from its signature.
func TestDeclarations(t *testing.T) {
	type hidden struct{ word word }
	type twice struct{ A, B word }
	for want, p := range map[string]Plugin{
		"kernel.word is not a struct":                      NewPlugin("p", func(*Context, word) (none, error) { return none{}, nil }),
		"field word of kernel.hidden is not exported":      NewPlugin("p", func(_ *Context, h hidden) (none, error) { return none{}, errors.New(string(h.word)) }),
		"kernel.twice lists the service kernel.word twice": NewPlugin("p", func(*Context, none) (twice, error) { return twice{}, nil }),
	} {
		if err := New().Start(p); err == nil || !strings.Contains(err.Error(), "plugin p: ") || !strings.Contains(err.Error(), want) {
			t.Errorf("got %v, want %q in it", err, want)
		}
	}
}

// A plugin that declares a service has to hand it over.
func TestDeclaredServiceThatIsNotProvided(t *testing.T) {
	type services struct {
		Word    word
		Greeter greeter
	}
	k := New()
	forgetful := NewPlugin("forgetful", func(*Context, none) (services, error) {
		return services{Word: "x"}, nil
	})
	err := k.Start(forgetful)
	if err == nil || !strings.Contains(err.Error(), "plugin forgetful: declares the service kernel.greeter but did not provide it") {
		t.Fatalf("got %v", err)
	}
	if _, ok := Service[word](k); ok || len(k.Plugins()) != 0 {
		t.Fatal("what the plugin did provide should be taken back")
	}
}

// A plugin started later builds on the ones that run; it cannot take the name
// of one of them, and a refused start changes nothing.
func TestSecondStart(t *testing.T) {
	k := New()
	provider := NewPlugin("provider", func(*Context, none) (struct{ Greeter greeter }, error) {
		return struct{ Greeter greeter }{english{}}, nil
	})
	if err := k.Start(adds("a"), provider); err != nil {
		t.Fatal(err)
	}
	if err := k.Start(adds("b"), adds("a")); err == nil || !strings.Contains(err.Error(), "plugin a is listed twice") {
		t.Fatalf("got %v", err)
	}
	if got := All[word](k.Context()); !slices.Equal(got, []word{"a"}) {
		t.Fatalf("a refused start should change nothing: %v", got)
	}
	user := NewPlugin("user", func(ctx *Context, n struct{ Greeter greeter }) (none, error) {
		Add(ctx, word(n.Greeter.Greet()))
		return none{}, nil
	})
	if err := k.Start(user, adds("b")); err != nil || !slices.Equal(k.Plugins(), []string{"a", "provider", "user", "b"}) {
		t.Fatalf("started %v, %v", k.Plugins(), err)
	}
	if got := All[word](k.Context()); !slices.Equal(got, []word{"a", "hello", "b"}) {
		t.Fatalf("items %v", got)
	}
}

// What is sealed, for the tests below.
type (
	secret struct{ key string }
	vault  interface{ Open() *secret }
	// Types through which a secret can be had.
	holder   struct{ Secret *secret }
	giver    struct{}
	ptrGiver struct{}
	taker    func(secret) string
	asker    interface{ Ask(map[string][]secret) }
	nested   struct{ List []map[word]chan [1]holder }
	// Types that keep it to themselves.
	keeper struct {
		secret *secret
		Self   *keeper
		Name   string
	}
	user struct{}
)

func (giver) Secret() (string, secret)        { return "", secret{} }
func (*ptrGiver) Secret() *secret             { return nil }
func (k *keeper) Use(secret) string           { return k.secret.key }
func (user) Use(*secret, vault) (word, error) { return "", nil }

// provides returns a plugin that provides a T.
func provides[T any](value T) Plugin {
	return NewPlugin("p", func(*Context, none) (struct{ Service T }, error) {
		return struct{ Service T }{value}, nil
	})
}

// needs returns a plugin that needs a T.
func needs[T any]() Plugin {
	return NewPlugin("p", func(*Context, struct{ Service T }) (none, error) { return none{}, nil })
}

// A sealed type is never a service, and neither is a type that hands one out
// or is handed one: such a plugin stops the kernel from starting.
func TestSealedTypesAreNoServices(t *testing.T) {
	sealed := func() *Kernel {
		k := New()
		Seal[secret](k)
		Seal[vault](k)
		return k
	}
	for name, p := range map[string]Plugin{
		"the type itself":             provides(secret{}),
		"a pointer to it":             provides(&secret{}),
		"needing it":                  needs[*secret](),
		"a sealed interface":          needs[vault](),
		"an exported field":           provides(holder{}),
		"what a method returns":       provides(giver{}),
		"a method of the pointer":     provides(ptrGiver{}),
		"an argument of a function":   provides(taker(func(secret) string { return "" })),
		"an argument of an interface": needs[asker](),
		"a result of a function":      provides(func() ([]vault, error) { return nil, nil }),
		"deep inside":                 provides(nested{}),
	} {
		err := sealed().Start(p)
		if err == nil || !strings.Contains(err.Error(), "plugin p declares the service") || !strings.Contains(err.Error(), "never a service") {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if err := sealed().Start(provides(holder{})); err == nil || !strings.Contains(err.Error(), "kernel.holder, which gives access to kernel.secret") {
		t.Errorf("the error should name the service and what it gives away: %v", err)
	}

	// What a type keeps in unexported fields, and what its own methods are
	// handed, stays with it.
	for name, p := range map[string]Plugin{
		"an unexported field":  provides(&keeper{}),
		"arguments of methods": provides(user{}),
		"unrelated":            provides(english{}),
	} {
		if err := sealed().Start(p); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Without the seal the same types are services like any other.
	if err := New().Start(provides(&secret{})); err != nil {
		t.Fatal(err)
	}
}
