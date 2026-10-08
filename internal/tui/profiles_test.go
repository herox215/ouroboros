package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// shiftTab is the press of shift+tab.
var shiftTab = tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift})

// profiles returns the UI of two profiles, Terra and Luna, which shows
// Terra; Luna is not started yet.
func profiles(t *testing.T) (root, terra, luna *Model, lb *fakeBackend) {
	t.Helper()
	terra, _ = newModel(t)
	lb = newBackend(t)
	lb.status = Status{Configured: true, Running: true, ID: "bbb222", Name: "Luna"}
	luna = New(lb, nil, nil)
	t.Cleanup(luna.quit)
	root = WithProfiles([]*Model{terra, luna}, 0)
	root.Init()
	return root, terra, luna, lb
}

// shift+tab shows the next profile, which keeps what was typed in it; what
// comes in for one that is not shown goes to it.
func TestProfileSwitchKeepsDraftAndRoutesLateEvents(t *testing.T) {
	root, terra, luna, _ := profiles(t)
	terra.input.SetValue("Terra draft")
	terra.chat.pending = true
	root.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	root.Update(shiftTab)
	if root.profiles.shown != 1 || luna.showSplash || luna.width != 100 || !terra.chat.pending {
		t.Fatal("the switch changed the other profile")
	}
	if view := ansi.Strip(root.View().Content); !strings.Contains(view, "profile Luna (bbb222) · shift+tab shows the next") {
		t.Fatalf("the switch is not told:\n%s", view)
	}
	luna.input.SetValue("Luna draft")
	root.Update(profileMsg{0, eventMsg{id: terra.chat.contextID, event: Event{Kind: plugin.SessionAnswer, Text: "Terra answer"}}})
	if terra.chat.pending || !containsText(terra.chat, "Terra answer") || containsText(luna.chat, "Terra answer") {
		t.Fatal("what came in for Terra went to Luna")
	}
	root.Update(shiftTab)
	if root.profiles.shown != 0 || terra.input.Value() != "Terra draft" || luna.input.Value() != "Luna draft" {
		t.Fatal("a draft was lost")
	}
	// A key goes to the profile shown.
	root.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if terra.input.Value() != "Terra draftx" {
		t.Fatalf("typed %q", terra.input.Value())
	}
	if root.Current() != terra.Current() || root.Profile() != "aaa111" {
		t.Fatalf("the UI shows %s of %s", root.Current(), root.Profile())
	}
}

// containsText reports whether c has an entry of text.
func containsText(c *conversation, text string) bool {
	for _, e := range c.entries {
		if e.text == text {
			return true
		}
	}
	return false
}

// A profile whose node cannot tell who it is is named by its place.
func TestProfileThatCannotTell(t *testing.T) {
	root, _, _, lb := profiles(t)
	lb.fail = map[string]error{"status": errors.New("gone")}
	root.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	root.Update(shiftTab)
	if view := ansi.Strip(root.View().Content); !strings.Contains(view, "profile 2 · shift+tab shows the next") {
		t.Fatalf("the switch is not told:\n%s", view)
	}
	if root.Profile() != "" {
		t.Fatalf("the profile is %q", root.Profile())
	}
	// None is selected by an ID no profile tells.
	root.SelectProfile("bbb222")
	if root.profiles.shown != 1 {
		t.Fatal("a profile that cannot tell was selected")
	}
}

// Messages keep the profile they are for, but those of Bubble Tea itself,
// which go to the program.
func TestTaggedCommands(t *testing.T) {
	if tagged(1, nil) != nil {
		t.Fatal("no command is none")
	}
	if msg := tagged(1, func() tea.Msg { return nil })(); msg != nil {
		t.Fatalf("no message is %v", msg)
	}
	if msg, ok := tagged(1, func() tea.Msg { return tickMsg{} })().(profileMsg); !ok || msg.profile != 1 {
		t.Fatalf("got %v", msg)
	}
	batch := tagged(1, tea.Batch(func() tea.Msg { return tickMsg{} }, func() tea.Msg { return statusMsg{} }))().(tea.BatchMsg)
	for _, cmd := range batch {
		if msg, ok := cmd().(profileMsg); !ok || msg.profile != 1 {
			t.Fatal("a batch lost what it is for")
		}
	}
	if _, ok := tagged(0, tea.Quit)().(tea.QuitMsg); !ok {
		t.Fatal("quitting was kept from the program")
	}
}

// The UI selects the profile it goes on with, and hands it on with a new
// build; the UI of one profile has none to name.
func TestProfilesAcrossARestart(t *testing.T) {
	root, terra, luna, _ := profiles(t)
	if exe, ok := root.Restarts(); ok || exe != "" {
		t.Fatal("nobody asked for a new build")
	}
	root.SelectProfile("bbb222")
	root.Resume("s1")
	if root.profiles.shown != 1 || luna.Current() != "s1" || root.Current() != "s1" || luna.showSplash {
		t.Fatalf("the UI goes on with %s", root.Current())
	}
	root.SelectProfile("ccc333") // none: it stays
	if root.profiles.shown != 1 {
		t.Fatal("an unknown profile was selected")
	}
	terra.restarts = "/new/ouro"
	if exe, ok := root.Restarts(); !ok || exe != "/new/ouro" {
		t.Fatalf("restarts with %q, %v", exe, ok)
	}
	root.CloseProfiles()
	if terra.ctx.Err() == nil || luna.ctx.Err() == nil {
		t.Fatal("the profiles still ask the node")
	}

	single, _ := newModel(t)
	single.SelectProfile("aaa111")
	single.CloseProfiles()
	if single.Profile() != "" || single.ctx.Err() != nil {
		t.Fatal("a UI of one profile acts as one of several")
	}
}
