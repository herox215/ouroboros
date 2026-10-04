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
// "Greetings" on g, with the key w on it, and the key h for the commands.
type contribution struct {
	loaded  int   // how often the screen was asked what it shows
	bodyErr error // what it fails with
	ran     []string
	keyErr  error
	ctx     context.Context // what the plugin was last called with
}

func (c *contribution) screen() plugin.Screen {
	return plugin.Screen{Key: "g", Title: "Greetings", Keys: []plugin.Key{c.key("w", "wave")},
		Body: func(ctx context.Context) (string, error) {
			c.loaded++
			c.ctx = ctx
			return "beta says hello", c.bodyErr
		}}
}

func (c *contribution) key(key, help string) plugin.Key {
	return plugin.Key{Key: key, Help: help, Run: func(ctx context.Context) (string, error) {
		c.ran = append(c.ran, help)
		c.ctx = ctx
		return help + " done", c.keyErr
	}}
}

// extended is a UI with the contribution, where the commands are.
func extended(t *testing.T) (*Model, *contribution) {
	t.Helper()
	c := &contribution{}
	m := New(&fakeBackend{status: Status{Configured: true, Running: true, Name: "alpha"}},
		[]plugin.Screen{c.screen()}, []plugin.Key{c.key("h", "hail")})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.showSplash = false
	m.Update(m.refreshStatus())
	press(m, "esc")
	return m, c
}

// A plugin adds a screen: its key opens it, it shows what the plugin returns,
// and its own keys work on it.
func TestPluginScreen(t *testing.T) {
	m, c := extended(t)
	if m.notice != "" {
		t.Fatalf("notice %q", m.notice)
	}
	if help := footer(m); !strings.Contains(help, "R requests · g Greetings · h hail · q quit") {
		t.Fatalf("the footer should name what the plugin added: %q", help)
	}

	load := press(m, "g")
	if m.screen != screenPlugin || load == nil {
		t.Fatal("g should open the screen of the plugin")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "Greetings") || !strings.Contains(view, "…") || strings.Contains(view, "beta says hello") {
		t.Fatalf("a screen that is on its way:\n%s", view)
	}
	m.Update(load())
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"● online", "Greetings", "beta says hello", "w wave · esc back · ctrl+c quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "No messages yet") || strings.Contains(view, "h hail") {
		t.Fatalf("the screen takes the place of the chat and its keys:\n%s", view)
	}

	// A key of the screen runs, says what it did, and the screen is read anew.
	run := press(m, "w")
	if run == nil {
		t.Fatal("w should run the key of the screen")
	}
	_, refresh := m.Update(run())
	if !slices.Equal(c.ran, []string{"wave"}) || m.notice != "wave done" || m.noticeErr || !strings.Contains(footer(m), "wave done") {
		t.Fatalf("ran %v, notice %q", c.ran, m.notice)
	}
	if settle(m, refresh); c.loaded != 2 {
		t.Fatalf("loaded %d times", c.loaded)
	}
	// So it is every few seconds while it is open.
	shortRefresh(t)
	_, cmd := m.Update(tickMsg{})
	if settle(m, cmd); c.loaded != 3 {
		t.Fatalf("loaded %d times", c.loaded)
	}
	// The keys of the commands mean nothing here.
	if press(m, "h") != nil || press(m, "n") != nil || len(c.ran) != 1 {
		t.Fatalf("ran %v", c.ran)
	}

	// A screen that cannot say what it shows says why.
	c.bodyErr = errors.New("network is down")
	settle(m, m.loadBody())
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "✗ network is down") || strings.Contains(view, "beta says hello") {
		t.Fatalf("view:\n%s", view)
	}

	// esc, q and the key that opened it lead back; what arrives late is dropped.
	press(m, "esc")
	for _, back := range []string{"esc", "q", "g"} {
		if press(m, "g"); m.screen != screenPlugin {
			t.Fatalf("before %s: g should open the screen", back)
		}
		late := m.loadBody()
		if press(m, back); m.screen != screenChat {
			t.Fatalf("%s should lead back to the chat", back)
		}
		if m.Update(late()); m.body.loaded {
			t.Fatalf("%s: a screen that is closed shows nothing", back)
		}
	}
	// Off the screen it is not read any more.
	loaded := c.loaded
	_, cmd = m.Update(tickMsg{})
	if settle(m, cmd); c.loaded != loaded {
		t.Fatal("a screen that is closed should not be asked what it shows")
	}
}

// A plugin adds a key for where the commands are.
func TestPluginKey(t *testing.T) {
	m, c := extended(t)
	_, refresh := m.Update(press(m, "h")())
	if !slices.Equal(c.ran, []string{"hail"}) || m.notice != "hail done" || m.noticeErr || refresh == nil {
		t.Fatalf("ran %v, notice %q", c.ran, m.notice)
	}
	c.keyErr = errors.New("nobody there")
	m.Update(press(m, "h")())
	if m.notice != "nobody there" || !m.noticeErr {
		t.Fatalf("notice %q (error %v)", m.notice, m.noticeErr)
	}
	// A key nobody claimed does nothing, and in the input a key is a letter.
	if press(m, "z") != nil {
		t.Fatal("z is no key")
	}
	press(m, "enter", "h")
	if m.input.Value() != "h" || len(c.ran) != 2 {
		t.Fatalf("typed %q, ran %v", m.input.Value(), c.ran)
	}

	// What a plugin runs stops when the user quits.
	press(m, "esc", "g")()
	if c.ctx.Err() != nil {
		t.Fatal("the plugin should be called with a live context")
	}
	if _, quit := m.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})); quit == nil || c.ctx.Err() == nil {
		t.Fatal("ctrl+c on the screen of a plugin should quit and stop what the plugin does")
	}
}

// A key has one meaning. The UI keeps its own, the first contribution that
// claims a key gets it, and the user is told what was left out.
func TestKeyConflicts(t *testing.T) {
	c := &contribution{}
	screen := func(key, title string, keys ...plugin.Key) plugin.Screen {
		s := c.screen()
		s.Key, s.Title, s.Keys = key, title, keys
		return s
	}
	screens := []plugin.Screen{
		screen("s", "Stats"), // a key of the UI
		screen("g", "Greetings",
			c.key("q", "quack"), // leads back
			c.key("g", "greet"), // leads back as well
			c.key("w", "wave"),
			c.key("w", "wink"),
			plugin.Key{Key: "y", Help: "yell"}), // does nothing
		screen("g", "Games"), // the key of a screen before it
		screen("", "Lost"),
		{Key: "e", Title: "Empty"}, // shows nothing
	}
	keys := []plugin.Key{
		c.key("g", "go"), // the key of a screen
		c.key("R", "reload"),
		c.key("h", "hail"),
		c.key("h", "help"),
		c.key("", "nothing"),
		c.key("w", "wave"), // free here: taken only on the screen
	}
	b := &fakeBackend{status: Status{Configured: true, Running: true, Name: "alpha"}}
	m := New(b, screens, keys)

	want := []string{
		`key s is taken by the terminal UI: the screen "Stats" is left out`,
		`key q is taken by the terminal UI: "quack" on the screen "Greetings" is left out`,
		`key g is taken by the terminal UI: "greet" on the screen "Greetings" is left out`,
		`key w is taken by "wave" on the screen "Greetings": "wink" on the screen "Greetings" is left out`,
		`"yell" on the screen "Greetings" lacks a key or what it does: left out`,
		`key g is taken by the screen "Greetings": the screen "Games" is left out`,
		`the screen "Lost" lacks a key or what it does: left out`,
		`the screen "Empty" lacks a key or what it does: left out`,
		`key g is taken by the screen "Greetings": "go" is left out`,
		`key R is taken by the terminal UI: "reload" is left out`,
		`key h is taken by "hail": "help" is left out`,
		`"nothing" lacks a key or what it does: left out`,
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
	if help := footer(m); strings.Contains(help, "left out") || !strings.Contains(help, "R requests · g Greetings · h hail · w wave · q quit") {
		t.Fatalf("footer %q", help)
	}

	// The UI still does what its keys always did, and the first claim holds.
	if m.Update(press(m, "s")()); b.stopped != 1 || len(c.ran) != 0 {
		t.Fatalf("s should stop the node: stopped %d, ran %v", b.stopped, c.ran)
	}
	if press(m, "R"); m.screen != screenRequests {
		t.Fatal("R should open the connection requests")
	}
	press(m, "esc")
	if press(m, "h")(); !slices.Equal(c.ran, []string{"hail"}) {
		t.Fatalf("ran %v", c.ran)
	}
	if press(m, "g"); m.screen != screenPlugin || m.screens[m.open].Title != "Greetings" {
		t.Fatalf("g should open the first screen that claimed it: %+v", m.screens)
	}
	if help := footer(m); !strings.Contains(help, " w wave · esc back") || strings.Contains(help, "wink") || strings.Contains(help, "quack") {
		t.Fatalf("footer %q", help)
	}
	if press(m, "y") != nil {
		t.Fatal("a key that does nothing is not bound")
	}
	if press(m, "w")(); !slices.Equal(c.ran, []string{"hail", "wave"}) {
		t.Fatalf("ran %v", c.ran)
	}
	if press(m, "q"); m.screen != screenChat || len(c.ran) != 2 {
		t.Fatalf("q should lead back: ran %v", c.ran)
	}
}
