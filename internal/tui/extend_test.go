package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// contribution is what a plugin adds to the UI in these tests: the screen
// "Greetings" on /greetings, with the actions wave and wink on it, and the
// command /hail.
type contribution struct {
	loaded  int   // how often the screen was asked what it shows
	bodyErr error // what it fails with
	ran     []string
	runErr  error
	ctx     context.Context // what the plugin was last called with
}

func (c *contribution) screen() plugin.Screen {
	return plugin.Screen{Command: "greetings", Title: "Greetings", Actions: []plugin.Action{c.action("wave"), c.action("wink")},
		Body: func(ctx context.Context) (string, error) {
			c.loaded++
			c.ctx = ctx
			return "beta says hello", c.bodyErr
		}}
}

// do is what an action or a command named name does.
func (c *contribution) do(name string) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		c.ran = append(c.ran, name)
		c.ctx = ctx
		return name + " done", c.runErr
	}
}

func (c *contribution) action(name string) plugin.Action {
	return plugin.Action{Name: name, Run: c.do(name)}
}

func (c *contribution) slash(name, help string) plugin.Slash {
	return plugin.Slash{Name: name, Help: help, Run: c.do(name)}
}

// extended is a UI with the contribution.
func extended(t *testing.T) (*Model, *contribution) {
	t.Helper()
	c := &contribution{}
	m := New(&fakeBackend{status: Status{Configured: true, Running: true, Name: "alpha"}},
		[]plugin.Screen{c.screen()}, []plugin.Slash{c.slash("hail", "hail everyone")})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.showSplash = false
	m.Update(m.refreshStatus())
	return m, c
}

// A plugin adds a screen: its command opens it, it shows what the plugin
// returns, and its actions run on it.
func TestPluginScreen(t *testing.T) {
	m, c := extended(t)
	if m.notice != "" {
		t.Fatalf("notice %q", m.notice)
	}

	load := run(m, "/greetings")
	if m.screen != screenPlugin || load == nil {
		t.Fatal("/greetings should open the screen of the plugin")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Greetings") || !strings.Contains(view, "…") || strings.Contains(view, "beta says hello") {
		t.Fatalf("a screen that is on its way:\n%s", view)
	}
	m.Update(load())
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"● online", "Greetings", "beta says hello", "[wave]  wink ", "←→ select · enter run · esc back · ctrl+c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "No messages yet") {
		t.Fatalf("the screen takes the place of the chat:\n%s", view)
	}

	// An action runs, says what it did, and the screen is read anew.
	ran := press(m, "enter")
	if ran == nil {
		t.Fatal("enter should run the action that is selected")
	}
	_, refresh := m.Update(ran())
	if !slices.Equal(c.ran, []string{"wave"}) || m.notice != "wave done" || m.noticeErr || !strings.Contains(footer(m), "wave done") {
		t.Fatalf("ran %v, notice %q", c.ran, m.notice)
	}
	if settle(m, refresh); c.loaded != 2 {
		t.Fatalf("loaded %d times", c.loaded)
	}
	// The arrows select another action, as far as there are actions.
	for _, step := range []struct {
		key  string
		want int
	}{{"right", 1}, {"right", 1}, {"left", 0}, {"h", 0}, {"l", 1}} {
		if press(m, step.key); m.choice != step.want {
			t.Fatalf("after %s: chose %d, want %d", step.key, m.choice, step.want)
		}
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, " wave  [wink]") {
		t.Fatalf("wink should be selected:\n%s", view)
	}
	press(m, "enter")()
	if !slices.Equal(c.ran, []string{"wave", "wink"}) {
		t.Fatalf("ran %v", c.ran)
	}
	// So it is read every few seconds while it is open.
	shortRefresh(t)
	_, cmd := m.Update(tickMsg{})
	if settle(m, cmd); c.loaded != 3 {
		t.Fatalf("loaded %d times", c.loaded)
	}
	// Other keys mean nothing here.
	if press(m, "n") != nil || press(m, "/") != nil || len(c.ran) != 2 || m.input.Value() != "" {
		t.Fatalf("ran %v", c.ran)
	}

	// A screen that cannot say what it shows says why.
	c.bodyErr = errors.New("network is down")
	settle(m, m.loadBody())
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "✗ network is down") || strings.Contains(view, "beta says hello") {
		t.Fatalf("view:\n%s", view)
	}

	// esc leads back, and what arrives late is dropped.
	press(m, "esc")
	run(m, "/greetings")
	late := m.loadBody()
	if press(m, "esc"); m.screen != screenChat {
		t.Fatal("esc should lead back to the chat")
	}
	if m.Update(late()); m.body.loaded {
		t.Fatal("a screen that is closed shows nothing")
	}
	// Off the screen it is not read any more.
	loaded := c.loaded
	_, cmd = m.Update(tickMsg{})
	if settle(m, cmd); c.loaded != loaded {
		t.Fatal("a screen that is closed should not be asked what it shows")
	}
	// Opened again, the first action is selected.
	if run(m, "/greetings"); m.choice != 0 {
		t.Fatalf("chose %d", m.choice)
	}
}

// A screen without actions offers none, and its text may fill it.
func TestPluginScreenWithoutActions(t *testing.T) {
	c := &contribution{}
	s := c.screen()
	s.Actions = nil
	m := New(&fakeBackend{status: Status{Configured: true, Running: true}}, []plugin.Screen{s}, nil)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	m.showSplash = false
	m.Update(run(m, "/greetings")())
	if help := footer(m); strings.Contains(help, "select") || !strings.Contains(help, "esc back") {
		t.Fatalf("footer %q", help)
	}
	if press(m, "right", "enter") != nil || m.choice != 0 {
		t.Fatal("there is nothing to run")
	}
}

// A plugin adds a command.
func TestPluginCommand(t *testing.T) {
	m, c := extended(t)
	_, refresh := m.Update(run(m, "/hail")())
	if !slices.Equal(c.ran, []string{"hail"}) || m.notice != "hail done" || m.noticeErr || refresh == nil {
		t.Fatalf("ran %v, notice %q", c.ran, m.notice)
	}
	c.runErr = errors.New("nobody there")
	m.Update(run(m, "/hail")())
	if m.notice != "nobody there" || !m.noticeErr {
		t.Fatalf("notice %q (error %v)", m.notice, m.noticeErr)
	}
	// /help lists it, with the screen.
	run(m, "/help")
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"/new", "/pending", "/greetings  open Greetings", "/hail       hail everyone"} {
		if !strings.Contains(view, want) {
			t.Fatalf("help lacks %q:\n%s", want, view)
		}
	}

	// What a plugin runs stops when the user quits.
	press(m, "esc")
	run(m, "/greetings")()
	if c.ctx.Err() != nil {
		t.Fatal("the plugin should be called with a live context")
	}
	if _, quit := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})); quit == nil || c.ctx.Err() == nil {
		t.Fatal("ctrl+c on the screen of a plugin should quit and stop what the plugin does")
	}
}

// A name has one meaning. The UI keeps its own, the first contribution that
// claims a name gets it, and the user is told what was left out.
func TestCommandConflicts(t *testing.T) {
	c := &contribution{}
	screen := func(name, title string, actions ...plugin.Action) plugin.Screen {
		s := c.screen()
		s.Command, s.Title, s.Actions = name, title, actions
		return s
	}
	screens := []plugin.Screen{
		screen("stop", "Stats"), // a command of the UI
		screen("greetings", "Greetings",
			c.action("wave"),
			plugin.Action{Name: "yell"}, // does nothing
			plugin.Action{Run: c.do("x")}),
		screen("greetings", "Games"), // the command of a screen before it
		screen("", "Lost"),
		screen("/slash", "Slashed"),
		screen("two words", "Spaced"),
		{Command: "empty", Title: "Empty"}, // shows nothing
	}
	slashes := []plugin.Slash{
		c.slash("greetings", "go"), // the command of a screen
		c.slash("pending", "reload"),
		c.slash("hail", "hail"),
		c.slash("hail", "help"),
		c.slash("", "nothing"),
		{Name: "idle", Help: "idle"}, // does nothing
	}
	b := &fakeBackend{status: Status{Configured: true, Running: true, Name: "alpha"}}
	m := New(b, screens, slashes)

	want := []string{
		`/stop is taken by the terminal UI: the screen "Stats" is left out`,
		`an action on the screen "Greetings" lacks a name or what it does: left out`,
		`an action on the screen "Greetings" lacks a name or what it does: left out`,
		`/greetings is taken by the screen "Greetings": the screen "Games" is left out`,
		`the screen "Lost" lacks a command or what it does: left out`,
		`the screen "Slashed" lacks a command or what it does: left out`,
		`the screen "Spaced" lacks a command or what it does: left out`,
		`the screen "Empty" lacks a command or what it does: left out`,
		`/greetings is taken by the screen "Greetings": "go" is left out`,
		`/pending is taken by the terminal UI: "reload" is left out`,
		`/hail is taken by "hail": "help" is left out`,
		`"nothing" lacks a command or what it does: left out`,
		`"idle" lacks a command or what it does: left out`,
	}
	if !m.noticeErr || m.notice != strings.Join(want, "; ") {
		t.Fatalf("notice (error %v):\n%s\nwant:\n%s", m.noticeErr, strings.ReplaceAll(m.notice, "; ", "\n"), strings.Join(want, "\n"))
	}
	// The user sees it when the UI opens, and until the first key.
	m.Update(tea.WindowSizeMsg{Width: 400, Height: 30})
	m.showSplash = false
	m.Update(m.refreshStatus())
	if help := footer(m); !strings.Contains(help, want[0]) {
		t.Fatalf("footer %q", help)
	}
	press(m, "esc")
	if help := footer(m); strings.Contains(help, "left out") {
		t.Fatalf("footer %q", help)
	}

	// The UI still does what its commands always did, and the first claim holds.
	if m.Update(run(m, "/stop")()); b.stopped != 1 || len(c.ran) != 0 {
		t.Fatalf("/stop should stop the node: stopped %d, ran %v", b.stopped, c.ran)
	}
	if run(m, "/pending"); m.screen != screenRequests {
		t.Fatal("/pending should open the connection requests")
	}
	press(m, "esc")
	if run(m, "/hail")(); !slices.Equal(c.ran, []string{"hail"}) {
		t.Fatalf("ran %v", c.ran)
	}
	if run(m, "/greetings"); m.screen != screenPlugin || m.screens[m.open].Title != "Greetings" || len(m.screens[m.open].Actions) != 1 {
		t.Fatalf("/greetings should open the first screen that claimed it: %+v", m.screens)
	}
	press(m, "esc")
	if run(m, "/idle"); m.notice != "unknown command /idle: /help lists them" {
		t.Fatalf("a command that does nothing is not bound: %q", m.notice)
	}
}
