package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A command runs in the UI and never goes to the agent; one that is not
// known stays in the input, to be corrected.
func TestCommands(t *testing.T) {
	m, b := newModel(t)
	for line, want := range map[string]string{
		"/":          "type a command after the slash: /help lists them",
		"/ ":         "type a command after the slash: /help lists them",
		"/nwe":       "unknown command /nwe: /help lists them",
		"/new chat":  "/new takes no arguments",
		"/help more": "/help takes no arguments",
	} {
		m.input.SetValue(line)
		if cmd := press(m, "enter"); cmd != nil || m.notice != want || !m.noticeErr || m.input.Value() != line {
			t.Fatalf("%q: notice %q (error %v), input %q", line, m.notice, m.noticeErr, m.input.Value())
		}
	}
	if len(b.sent) != 0 || len(m.chat.entries) != 0 {
		t.Fatalf("a command is no message: sent %v", b.sent)
	}

	// A message that starts with a slash is typed with two.
	deliver(m, say(t, m, "//etc is a directory"))
	if len(b.sent) != 1 || b.sent[0].text != "/etc is a directory" {
		t.Fatalf("sent %+v", b.sent)
	}

	// /new starts over and clears the input.
	if run(m, "/new"); len(m.chat.entries) != 0 || m.input.Value() != "" || m.notice != "new conversation" {
		t.Fatalf("entries %+v, notice %q", m.chat.entries, m.notice)
	}
}

// While a command is typed, the footer offers those that fit.
func TestCommandSuggestions(t *testing.T) {
	m, _ := newModel(t)
	if help := footer(m); !strings.Contains(help, "/help commands") {
		t.Fatalf("footer %q", help)
	}
	press(m, "/", "s")
	if help := strings.TrimSpace(footer(m)); help != "/start start the node · /stop stop the node · /setup set up the node" {
		t.Fatalf("footer %q", help)
	}
	press(m, "t", "o")
	if help := strings.TrimSpace(footer(m)); help != "/stop stop the node" {
		t.Fatalf("footer %q", help)
	}
	// Once the name is complete, or for a message, there is nothing to offer.
	for _, text := range []string{"/stop now", "/x", "//", "hello"} {
		m.input.SetValue(text)
		if help := footer(m); !strings.Contains(help, "/help commands") {
			t.Fatalf("%q: footer %q", text, help)
		}
	}
}

func TestHelp(t *testing.T) {
	m, _ := newModel(t)
	run(m, "/help")
	if m.screen != screenHelp {
		t.Fatal("/help should list the commands")
	}
	view := ansi.Strip(m.View().Content)
	for _, c := range own {
		if !strings.Contains(view, "/"+c.name) || !strings.Contains(view, c.help) {
			t.Fatalf("help lacks /%s:\n%s", c.name, view)
		}
	}
	if help := strings.TrimSpace(footer(m)); help != "esc back · ctrl+c quit" {
		t.Fatalf("footer %q", help)
	}
	if press(m, "x"); m.screen != screenHelp || m.input.Value() != "" {
		t.Fatal("only esc leaves the help")
	}
	if press(m, "esc"); m.screen != screenChat {
		t.Fatal("esc should lead back to the chat")
	}
}
