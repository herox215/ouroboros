package tui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// A command runs in the UI and never goes to the agent; one that is not
// known stays in the input, to be corrected.
func TestCommands(t *testing.T) {
	m, b := newModel(t)
	for line, want := range map[string]string{
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
	if len(b.messages()) != 0 || len(m.chat.entries) != 0 {
		t.Fatalf("a command is no message: sent %v", b.messages())
	}

	// A message that starts with a slash is typed with two.
	answered(t, m, say(t, m, "//etc is a directory"))
	if sent := b.messages(); len(sent) != 1 || sent[0].text != "/etc is a directory" {
		t.Fatalf("sent %+v", sent)
	}

	// A command typed in full runs too, with a space after it.
	m.input.SetValue("/help ")
	if press(m, "enter"); m.screen != screenHelp || m.input.Value() != "" {
		t.Fatalf("screen %v, input %q", m.screen, m.input.Value())
	}
	press(m, "esc")

	// /new starts over and clears the input.
	if run(m, "/new"); len(m.chat.entries) != 0 || m.input.Value() != "" || m.notice != "new conversation" {
		t.Fatalf("entries %+v, notice %q", m.chat.entries, m.notice)
	}
}

// suggested are the lines of the list of suggestions in the view, without
// colors and with one space between the words: what the panel over the chat
// shows above the input.
func suggested(m *Model) []string {
	var lines []string
	for _, l := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		// The panel lies two cells right of the border of the chat.
		if cells := []rune(l); len(cells) > 4 && cells[2] == '│' {
			lines = append(lines, strings.Join(strings.Fields(string(cells[3:len(cells)-3])), " "))
		}
	}
	return lines[:max(0, len(lines)-inputHeight)]
}

// While a command is typed, a list above the input offers those that fit:
// up and down select one, tab completes the input to it and enter runs it.
func TestCommandSuggestions(t *testing.T) {
	m, _ := newModel(t)
	height := len(strings.Split(m.View().Content, "\n"))
	if help := footer(m); strings.TrimSpace(help) != "" || len(suggested(m)) != 0 {
		t.Fatalf("footer %q, suggested %q", help, suggested(m))
	}
	press(m, "/", "s")
	want := []string{"› /session list the conversations and switch to one", "/start start the node", "/stop stop the node", "/setup set up the node", "/settings the configuration"}
	if got := suggested(m); !slices.Equal(got, want) {
		t.Fatalf("suggested %q", got)
	}
	if help := strings.TrimSpace(footer(m)); help != "↑↓ select · tab complete · enter run" {
		t.Fatalf("footer %q", help)
	}
	// The list takes its rows from the conversation, not from the window.
	if got := len(strings.Split(m.View().Content, "\n")); got != height {
		t.Fatalf("the view is %d lines high, not %d", got, height)
	}

	// Down and up select, around the ends of the list.
	if press(m, "down"); suggested(m)[1] != "› /start start the node" {
		t.Fatalf("suggested %q", suggested(m))
	}
	if press(m, "up", "up"); suggested(m)[4] != "› /settings the configuration" {
		t.Fatalf("suggested %q", suggested(m))
	}
	if press(m, "down"); suggested(m)[0] != want[0] {
		t.Fatalf("suggested %q", suggested(m))
	}

	// Typing on narrows the list and selects its first command again.
	press(m, "down", "down", "e")
	if got := suggested(m); !slices.Equal(got, []string{"› /session list the conversations and switch to one", "/setup set up the node", "/settings the configuration"}) {
		t.Fatalf("suggested %q", got)
	}

	// Tab completes the input to the command selected.
	if press(m, "down", "down", "tab"); m.input.Value() != "/settings" || !slices.Equal(suggested(m), []string{"› /settings the configuration"}) {
		t.Fatalf("input %q, suggested %q", m.input.Value(), suggested(m))
	}

	// Enter runs the command selected, however little of it is typed.
	m.input.SetValue("/he")
	if press(m, "enter"); m.screen != screenHelp || m.input.Value() != "" {
		t.Fatalf("screen %v, input %q", m.screen, m.input.Value())
	}
	press(m, "esc")

	// More commands than the list has rows: it scrolls to the one selected.
	m.input.SetValue("/")
	if got := suggested(m); len(got) != maxSuggestions || got[0] != "› /new start a new conversation; the one shown goes on" {
		t.Fatalf("suggested %q", got)
	}
	press(m, "up")
	if got := suggested(m); len(got) != maxSuggestions || !strings.HasPrefix(got[maxSuggestions-1], "› /quit") {
		t.Fatalf("suggested %q", got)
	}

	// Once the name is complete, or for a message, there is nothing to offer.
	for _, text := range []string{"/stop now", "/x", "//", "hello"} {
		m.input.SetValue(text)
		if help := footer(m); strings.TrimSpace(help) != "" || len(suggested(m)) != 0 {
			t.Fatalf("%q: footer %q, suggested %q", text, help, suggested(m))
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
	// Descriptions can wrap within the narrower dialog.
	helpText := strings.Join(strings.Fields(strings.ReplaceAll(ansi.Strip(m.helpView()), "│", " ")), " ")
	for _, c := range own {
		if !strings.Contains(view, "/"+c.name) || !strings.Contains(helpText, c.help) {
			t.Fatalf("help lacks /%s:\n%s", c.name, view)
		}
	}
	if help := strings.TrimSpace(footer(m)); help != "esc back" {
		t.Fatalf("footer %q", help)
	}
	if press(m, "x"); m.screen != screenHelp || m.input.Value() != "" {
		t.Fatal("only esc leaves the help")
	}
	if press(m, "esc"); m.screen != screenChat {
		t.Fatal("esc should lead back to the chat")
	}
}

// /debug turns the debug mode of the node on and off again, and the header
// says so on the right while it is on.
func TestDebug(t *testing.T) {
	m, b := newModel(t)
	header := func() string { return strings.Split(ansi.Strip(m.View().Content), "\n")[0] }
	if strings.Contains(header(), "DEBUG MODE") {
		t.Fatalf("header %q", header())
	}
	settle(m, run(m, "/debug"))
	if !slices.Equal(b.debug, []bool{true}) || !strings.HasPrefix(m.notice, "debug mode on") {
		t.Fatalf("debug %v, notice %q", b.debug, m.notice)
	}

	b.status.Debug = true
	m.Update(m.refreshStatus())
	if h := header(); !strings.HasSuffix(h, " DEBUG MODE ") || ansi.StringWidth(h) != m.width || !strings.HasPrefix(h, " alpha ") {
		t.Fatalf("header %q", h)
	}
	settle(m, run(m, "/debug"))
	if !slices.Equal(b.debug, []bool{true, false}) || m.notice != "debug mode off" {
		t.Fatalf("debug %v, notice %q", b.debug, m.notice)
	}

	// A narrow window keeps the mark, not the rest.
	m.Update(tea.WindowSizeMsg{Width: 12, Height: 30})
	if h := header(); h != " DEBUG MODE " {
		t.Fatalf("header %q", h)
	}
}

// A paste that leaves fewer commands to offer than the one selected selects
// the first again.
func TestPasteNarrowsTheSuggestions(t *testing.T) {
	m, _ := newModel(t)
	press(m, "/", "s", "down", "down", "down")
	m.Update(tea.PasteMsg{Content: "e"})
	if got := suggested(m); !slices.Equal(got, []string{"› /session list the conversations and switch to one", "/setup set up the node", "/settings the configuration"}) {
		t.Fatalf("suggested %q", got)
	}
}
