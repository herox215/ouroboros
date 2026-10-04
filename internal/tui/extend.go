package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// The keys the UI uses itself: where the commands are, and on the screen of a
// plugin. A contribution never gets one of them.
var (
	commandKeys = []string{"ctrl+c", "tab", "esc", "enter", "pgup", "pgdown", "i", "q", "s", "c", "n", "R"}
	screenKeys  = []string{"ctrl+c", "esc", "q"}
)

// ownUI is who holds a key of the UI, as a problem names it.
const ownUI = "the terminal UI"

// body is what the screen of a plugin shows.
type body struct {
	text   string
	err    error
	loaded bool // otherwise it is still on its way
}

// bind gives the contributions of the plugins their keys and returns those
// that got one: the screens, each with the keys that work on it, and the keys
// for where the commands are.
//
// A key has one meaning. The UI keeps its own keys, and of two contributions
// that claim the same key the first one gets it, where the screens come
// before the keys. The others are left out, and
// so is a contribution without a key or without what it does. Each of them is
// named in problems, which the UI shows when it opens: a plugin that claims a
// key in vain does not stop the UI, and its user learns what is missing.
func bind(screens []plugin.Screen, keys []plugin.Key) (bound []plugin.Screen, commands []plugin.Key, problems []string) {
	// claim gives key to who unless it is taken, and reports whether it did.
	claim := func(taken map[string]string, key, who string, complete bool) bool {
		holder, isTaken := taken[key]
		switch {
		case key == "" || !complete:
			problems = append(problems, fmt.Sprintf("%s lacks a key or what it does: left out", who))
		case isTaken:
			problems = append(problems, fmt.Sprintf("key %s is taken by %s: %s is left out", key, holder, who))
		default:
			taken[key] = who
			return true
		}
		return false
	}
	// bindKeys returns those of keys that got theirs.
	bindKeys := func(taken map[string]string, keys []plugin.Key, where string) []plugin.Key {
		var got []plugin.Key
		for _, k := range keys {
			if claim(taken, k.Key, fmt.Sprintf("%q%s", k.Help, where), k.Run != nil) {
				got = append(got, k)
			}
		}
		return got
	}
	held := func(keys []string) map[string]string {
		taken := map[string]string{}
		for _, k := range keys {
			taken[k] = ownUI
		}
		return taken
	}

	taken := held(commandKeys)
	for _, s := range screens {
		who := fmt.Sprintf("the screen %q", s.Title)
		if !claim(taken, s.Key, who, s.Body != nil) {
			continue
		}
		on := held(screenKeys)
		on[s.Key] = ownUI // it also leads back
		s.Keys = bindKeys(on, s.Keys, " on "+who)
		bound = append(bound, s)
	}
	return bound, bindKeys(taken, keys, ""), problems
}

// contributed runs what a plugin contributed for key where the commands are:
// it opens a screen or runs a key.
func (m *Model) contributed(key string) tea.Cmd {
	for i, s := range m.screens {
		if s.Key == key {
			m.screen, m.open, m.body = screenPlugin, i, body{}
			return m.loadBody()
		}
	}
	return m.runKey(m.keys, key)
}

// handleScreenKey handles keys on the screen of a plugin.
func (m *Model) handleScreenKey(msg tea.KeyPressMsg) tea.Cmd {
	s := m.screens[m.open]
	switch key := msg.String(); key {
	case "ctrl+c":
		return m.leave()
	case "esc", "q", s.Key:
		m.screen = screenChat
		return nil
	default:
		return m.runKey(s.Keys, key)
	}
}

// runKey runs the one of keys that key stands for, if any. What it returns
// becomes the notice, and what the UI shows is loaded anew.
func (m *Model) runKey(keys []plugin.Key, key string) tea.Cmd {
	for _, k := range keys {
		if k.Key == key {
			return func() tea.Msg {
				done, err := k.Run(m.ctx)
				return actionMsg{done, err}
			}
		}
	}
	return nil
}

// loadBody asks the plugin for what its screen, the one that is open, shows.
func (m *Model) loadBody() tea.Cmd {
	open, load := m.open, m.screens[m.open].Body
	return func() tea.Msg {
		text, err := load(m.ctx)
		return bodyMsg{open, body{text, err, true}}
	}
}

// screenView shows the screen of a plugin that is open.
func (m *Model) screenView() string {
	w, h := m.width, m.bodyHeight()
	text := m.body.text
	switch {
	case !m.body.loaded:
		text = mutedStyle.Render("…")
	case m.body.err != nil:
		text = errStyle.Render("✗ " + m.body.err.Error())
	}
	return box(true).Width(w).Height(h).MaxHeight(h).Render(boldStyle.Render(m.screens[m.open].Title) + "\n\n" + text)
}

// contributedHelp is the part of the footer for what the plugins contributed
// to where the commands are.
func (m *Model) contributedHelp() string {
	var b strings.Builder
	for _, s := range m.screens {
		b.WriteString(s.Key + " " + s.Title + " · ")
	}
	return b.String() + keysHelp(m.keys)
}

// keysHelp is the part of the footer for keys.
func keysHelp(keys []plugin.Key) string {
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k.Key + " " + k.Help + " · ")
	}
	return b.String()
}
