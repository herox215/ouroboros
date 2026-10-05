package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"ouroboros/internal/kernel"
	"ouroboros/plugin"
)

// started starts plugins that contribute with add and returns the context to
// read the contributions with.
func started(t *testing.T, add ...func(ctx *plugin.Context)) *plugin.Context {
	t.Helper()
	var ctx *plugin.Context
	plugins := []plugin.Plugin{plugin.New("reader", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	})}
	for i, contribute := range add {
		plugins = append(plugins, plugin.New(string(rune('a'+i)), func(c *plugin.Context, _ struct{}) error {
			contribute(c)
			return nil
		}))
	}
	if err := kernel.New().Start(plugins...); err != nil {
		t.Fatal(err)
	}
	return ctx
}

// frontEnd contributes a front end called name.
func frontEnd(name string) func(*plugin.Context) {
	return func(ctx *plugin.Context) {
		plugin.Add(ctx, plugin.FrontEnd{Name: name, Help: "open " + name, Run: func(context.Context) error { return errors.New(name + " ran") }})
	}
}

// ouro opens a front end if exactly one was contributed and the user sits at
// a terminal. It never picks one of several.
func TestFrontEndFor(t *testing.T) {
	terminal := plugin.NewConsole(plugin.Terminal{In: strings.NewReader(""), Out: io.Discard, Interactive: true})
	pipe := plugin.NewConsole(plugin.Terminal{In: strings.NewReader(""), Out: io.Discard})

	if _, err := plugin.FrontEndFor(started(t), terminal); err != plugin.ErrNoFrontEnd {
		t.Fatalf("without a front end: %v", err)
	}
	one := started(t, frontEnd("tui"))
	if _, err := plugin.FrontEndFor(one, pipe); err != plugin.ErrNoFrontEnd {
		t.Fatalf("without a terminal: %v", err)
	}
	front, err := plugin.FrontEndFor(one, terminal)
	if err != nil || front.Name != "tui" || front.Run(context.Background()).Error() != "tui ran" {
		t.Fatalf("got %+v, %v", front, err)
	}

	two := started(t, frontEnd("tui"), frontEnd("web"))
	_, err = plugin.FrontEndFor(two, terminal)
	if !errors.Is(err, plugin.ErrFrontEnds) || !strings.Contains(err.Error(), "(ouro tui, ouro web)") {
		t.Fatalf("with two front ends: %v", err)
	}
	if _, err := plugin.FrontEndFor(two, pipe); err != plugin.ErrNoFrontEnd {
		t.Fatalf("two front ends without a terminal: %v", err)
	}
}

// What is shown of a tool call is what the first view of that tool says that
// has something to say.
func TestToolLine(t *testing.T) {
	peer := func(args json.RawMessage) string {
		var call struct{ Peer string }
		json.Unmarshal(args, &call)
		return call.Peer
	}
	ctx := started(t,
		func(ctx *plugin.Context) {
			plugin.Add(ctx, plugin.ToolView{Tool: "other", Show: func(json.RawMessage) string { return "something else" }})
			plugin.Add(ctx, plugin.ToolView{Tool: "greet"}) // shows nothing
			plugin.Add(ctx, plugin.ToolView{Tool: "greet", Show: func(args json.RawMessage) string {
				if peer(args) == "" {
					return ""
				}
				return "greeting " + peer(args)
			}})
		},
		func(ctx *plugin.Context) {
			plugin.Add(ctx, plugin.ToolView{Tool: "greet", Show: func(json.RawMessage) string { return "greeting somebody" }})
		})

	for args, want := range map[string]string{
		`{"peer":"beta"}`: "greeting beta",
		`{}`:              "greeting somebody", // the first view passes
	} {
		if got := plugin.ToolLine(ctx, plugin.Step{Tool: "greet", Args: json.RawMessage(args)}); got != want {
			t.Errorf("greet %s: %q, want %q", args, got, want)
		}
	}
	if got := plugin.ToolLine(ctx, plugin.Step{Tool: "list"}); got != "" {
		t.Fatalf("a tool without a view: %q", got)
	}
}

// What became of a tool call is told by the first view of that tool that has
// a Detail, after the line of the call; that view alone decides.
func TestToolDetail(t *testing.T) {
	ctx := started(t,
		func(ctx *plugin.Context) {
			plugin.Add(ctx, plugin.ToolView{Tool: "other", Detail: func(plugin.Step) string { return "of something else" }})
			plugin.Add(ctx, plugin.ToolView{Tool: "greet"}) // tells nothing
			plugin.Add(ctx, plugin.ToolView{Tool: "greet", Detail: func(s plugin.Step) string {
				if !s.Done {
					return ""
				}
				return "✓"
			}})
		},
		func(ctx *plugin.Context) {
			plugin.Add(ctx, plugin.ToolView{Tool: "greet", Detail: func(plugin.Step) string { return "never asked" }})
		})

	if got := plugin.ToolDetail(ctx, plugin.Step{Tool: "greet", Done: true}, "greeting beta"); got != "greeting beta ✓" {
		t.Errorf("ended: %q", got)
	}
	if got := plugin.ToolDetail(ctx, plugin.Step{Tool: "greet"}, "greeting beta"); got != "greeting beta" {
		t.Errorf("a view with nothing to tell: %q", got)
	}
	if got := plugin.ToolDetail(ctx, plugin.Step{Tool: "list", Done: true}, "listing"); got != "listing" {
		t.Errorf("a tool without a Detail: %q", got)
	}
}
