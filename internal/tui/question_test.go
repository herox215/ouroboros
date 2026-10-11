package tui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/internal/agent"
	"ouroboros/plugin"
)

// asks makes the agent of b ask the owner which database, with Postgres
// recommended, once gate is closed, and answer with what they chose.
func asks(b *fakeBackend, gate <-chan struct{}) {
	b.ask = func(ctx context.Context, _ func(Step), _ func(string)) (string, error) {
		<-gate
		answer, err := agent.AskOwner(ctx, json.RawMessage(`{"question":"Which database?","options":["SQLite","Postgres"],"recommended":"Postgres"}`))
		if err != nil {
			return "", err
		}
		return "using " + answer, nil
	}
}

// asked is a UI whose agent asks which database, and waits for the answer.
func asked(t *testing.T) (*Model, *fakeBackend) {
	t.Helper()
	m, b := newModel(t)
	gate := make(chan struct{})
	close(gate)
	asks(b, gate)
	c := m.chat
	drive(t, m, say(t, m, "go"), func() bool { return c.asking != nil })
	return m, b
}

// texts are the entries of the conversation shown: those of the owner
// after ">".
func texts(m *Model) string {
	var got []string
	for _, e := range m.chat.entries {
		if e.mine {
			got = append(got, ">"+e.text)
		} else {
			got = append(got, e.text)
		}
	}
	return strings.Join(got, "|")
}

// fits reports whether the chat takes the room between the header and the
// footer, whatever the box of the input shows.
func fits(m *Model) bool { return lipgloss.Height(m.chatView()) == m.bodyHeight() }

// A question of the agent is part of the conversation, and where the input
// is its answers are to choose from, the one recommended selected: the arrow
// keys select another, enter gives it, and it is part of the conversation
// too.
func TestQuestion(t *testing.T) {
	m, _ := asked(t)
	c := m.chat
	view := ansi.Strip(m.chatView())
	for _, want := range []string{"? Which database?", "  SQLite", "› Postgres  recommended", "  Chat about it  write your own answer"} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Keep on dreaming") || !fits(m) || m.givenUp() != 0 {
		t.Fatalf("the input is not there to write in, and the chat fits (gives up %d):\n%s", m.givenUp(), view)
	}
	if got := m.waiting(); got != "alpha waits for your answer" {
		t.Fatalf("waiting: %q", got)
	}
	if m.notice != "" {
		t.Fatalf("asked in the conversation shown, there is nothing to notice: %q", m.notice)
	}
	if got := ansi.Strip(m.footerView()); !strings.Contains(got, "↑↓ choose · enter answer · esc stop") {
		t.Fatalf("footer: %q", got)
	}
	if got := texts(m); got != ">go|Which database?" {
		t.Fatalf("the conversation: %s", got)
	}
	// Up and down go around the rows; the last is the one for own words,
	// which shows the input, and up in an empty input takes it again.
	press(m, "up")
	if c.asking.sel != 0 {
		t.Fatalf("at the top: %d", c.asking.sel)
	}
	press(m, "up")
	if !c.asking.own() {
		t.Fatalf("up at the first goes to own words: %d", c.asking.sel)
	}
	press(m, "down", "down")
	if view := ansi.Strip(m.chatView()); !c.asking.own() || !strings.Contains(view, "› Chat about it") || !strings.Contains(view, "Keep on dreaming") || !fits(m) || m.givenUp() != 3 {
		t.Fatalf("own words (gives up %d):\n%s", m.givenUp(), view)
	}
	if cmd := press(m, "enter"); cmd != nil || c.asking == nil {
		t.Fatal("nothing written is no answer")
	}
	press(m, "up")
	if c.asking.sel != 1 {
		t.Fatalf("back at the answers: %d", c.asking.sel)
	}
	// A window with room for them shows all of the answers; one with little
	// room those around the one selected.
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	if view := ansi.Strip(m.chatView()); !fits(m) || !strings.Contains(view, "  SQLite") || !strings.Contains(view, "› Postgres") || !strings.Contains(view, "  Chat about it") {
		t.Fatalf("room for all:\n%s", view)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	if view := ansi.Strip(m.chatView()); !fits(m) || !strings.Contains(view, "› Postgres") || strings.Contains(view, "SQLite") || strings.Contains(view, "Chat about it") {
		t.Fatalf("little room:\n%s", view)
	}
	press(m, "down")
	if view := ansi.Strip(m.chatView()); !fits(m) || !strings.Contains(view, "› Chat about it") || strings.Contains(view, "SQLite") {
		t.Fatalf("little room:\n%s", view)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	press(m, "up")

	cmd := press(m, "enter")
	if !c.asking.sent || strings.Contains(ansi.Strip(m.chatView()), "? Which") {
		t.Fatal("the answer is on its way: nothing is left to choose")
	}
	if again, ok := m.handleQuestionKey(tea.KeyPressMsg{Code: tea.KeyEnter}); ok || again != nil {
		t.Fatal("it is not given twice")
	}
	drive(t, m, cmd, func() bool { return !c.pending })
	if got := texts(m); got != ">go|Which database?|>Postgres|using Postgres" || c.asking != nil {
		t.Fatalf("answered: %s, asking %+v", got, c.asking)
	}
	if view := ansi.Strip(m.chatView()); strings.Contains(view, "? Which") || !fits(m) || m.givenUp() != 0 || m.waiting() != "" || m.notice != "" {
		t.Fatalf("after the answer (notice %q):\n%s", m.notice, view)
	}
}

// Typing selects the row for own words, and what is sent from the input
// answers the question.
func TestQuestionInOwnWords(t *testing.T) {
	m, _ := asked(t)
	c := m.chat
	press(m, "n", "o")
	if !c.asking.own() || m.input.Value() != "no" {
		t.Fatalf("typing: row %d, %q", c.asking.sel, m.input.Value())
	}
	// Up is the input's while something is written.
	if _, ok := m.handleQuestionKey(tea.KeyPressMsg{Code: tea.KeyUp}); ok || !c.asking.own() {
		t.Fatal("up left what was written")
	}
	drive(t, m, press(m, "enter"), func() bool { return !c.pending })
	if got := texts(m); got != ">go|Which database?|>no|using no" {
		t.Fatalf("answered: %s", got)
	}
}

// An answer the node did not take is back in the input, with the question
// to answer again; a stopped answer takes its question with it.
func TestQuestionNotAnswered(t *testing.T) {
	m, b := asked(t)
	c := m.chat
	b.fail = map[string]error{"steer": errors.New("the node is gone")}
	deliver(m, press(m, "enter"))
	if c.asking == nil || c.asking.sent || !c.asking.own() || m.input.Value() != "Postgres" || m.notice != "the node is gone" {
		t.Fatalf("not taken: %+v, input %q, notice %q", c.asking, m.input.Value(), m.notice)
	}
	if view := ansi.Strip(m.chatView()); !strings.Contains(view, "? Which database?") {
		t.Fatalf("the question is not offered again:\n%s", view)
	}
	b.fail = nil
	drive(t, m, press(m, "esc"), func() bool { return !c.pending })
	if got := texts(m); got != ">go|Which database?|stopped before it answered" || c.asking != nil {
		t.Fatalf("stopped: %s, asking %+v", got, c.asking)
	}
}

// A question in a session that is not shown is told in the footer, and
// waits there.
func TestQuestionElsewhere(t *testing.T) {
	m, b := newModel(t)
	gate := make(chan struct{})
	asks(b, gate)
	first := m.chat
	drive(t, m, say(t, m, "go"), func() bool { return first.running })
	cmd := m.newConversation()
	close(gate)
	drive(t, m, cmd, func() bool { return first.asking != nil })
	if m.notice != "alpha asks you in “go” · /session" || m.chat.asking != nil {
		t.Fatalf("notice %q", m.notice)
	}
	if view := ansi.Strip(m.chatView()); strings.Contains(view, "? Which") || m.givenUp() != 0 {
		t.Fatalf("the question shows in another conversation:\n%s", view)
	}
	// Nothing is asked without a question: the keys are the chat's.
	if _, ok := m.handleQuestionKey(tea.KeyPressMsg{Code: tea.KeyEnter}); ok {
		t.Fatal("a key of a question without one")
	}
}

// A question without answers to choose from is answered in own words: the
// input is there below it, and no row to select.
func TestQuestionWithoutOptions(t *testing.T) {
	m, b := newModel(t)
	b.ask = func(ctx context.Context, _ func(Step), _ func(string)) (string, error) {
		answer, err := agent.AskOwner(ctx, json.RawMessage(`{"question":"What is it called?"}`))
		return "called " + answer, err
	}
	c := m.chat
	drive(t, m, say(t, m, "go"), func() bool { return c.asking != nil })
	if view := ansi.Strip(m.inputView(96, 20)); !c.asking.own() || !strings.HasPrefix(view, "? What is it called?\n") || !strings.Contains(view, "Keep on dreaming") || strings.Contains(view, ownWords) {
		t.Fatalf("the input alone:\n%s", view)
	}
	// There is no row to go up or down to.
	press(m, "up", "down")
	if !c.asking.own() {
		t.Fatalf("row %d", c.asking.sel)
	}
	m.input.SetValue("ouro")
	drive(t, m, press(m, "enter"), func() bool { return !c.pending })
	if got := texts(m); got != ">go|What is it called?|>ouro|called ouro" {
		t.Fatalf("answered: %s", got)
	}
}

// asksThis makes the agent of b ask the owner question, as the JSON the tool
// wrote, and a UI of it waits for the answer.
func asksThis(t *testing.T, question string) *Model {
	t.Helper()
	m, b := newModel(t)
	b.ask = func(ctx context.Context, _ func(Step), _ func(string)) (string, error) {
		return agent.AskOwner(ctx, json.RawMessage(question))
	}
	c := m.chat
	drive(t, m, say(t, m, "go"), func() bool { return c.asking != nil })
	return m
}

// A question longer than the panel is wide is cut to a line of it.
func TestLongQuestionIsCut(t *testing.T) {
	m := asksThis(t, `{"question":"`+strings.Repeat("why ", 60)+`","options":["a","b"]}`)
	panel := strings.Split(ansi.Strip(m.panel()), "\n")
	if len(panel) != 6 {
		t.Fatalf("the panel has %d rows:\n%s", len(panel), strings.Join(panel, "\n"))
	}
	for _, row := range panel {
		if lipgloss.Width(row) != 96 {
			t.Fatalf("a row of %d cells: %q", lipgloss.Width(row), row)
		}
	}
	if want := "│ ? " + strings.Repeat("why ", 22) + "w… │"; panel[1] != want {
		t.Fatalf("the question: %q, want %q", panel[1], want)
	}
}

// Commands offered while the agent asks leave the answers what room there
// is: in a small window the one selected alone.
func TestQuestionWithSuggestions(t *testing.T) {
	m, _ := asked(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 19})
	press(m, "/", "s")
	view := ansi.Strip(m.chatView())
	if !fits(m) || len(suggested(m)) != 5+2 || !strings.Contains(view, "› Chat about it") || strings.Contains(view, "SQLite") || strings.Contains(view, "Postgres") {
		t.Fatalf("%d rows of the panel above the input:\n%s", len(suggested(m)), view)
	}
}

// A question is noticed in the footer when it is asked in a conversation
// that is not shown: not in the one shown, not when it is answered, and not
// when it was asked before the UI watched.
func TestQuestionIsNoticedElsewhereOnly(t *testing.T) {
	m, _ := newModel(t)
	other := m.conversation("other")
	asked := Event{Kind: plugin.SessionAsked, Text: "Which?"}
	answered := Event{Kind: plugin.SessionAsked, Done: true, Text: "this"}
	for _, tt := range []struct {
		c       *conversation
		e       Event
		live    bool
		noticed bool
	}{
		{other, asked, true, true},
		{other, asked, false, false},
		{other, answered, true, false},
		{other, answered, false, false},
		{m.chat, asked, true, false},
		{m.chat, answered, true, false},
	} {
		m.notice = ""
		m.apply(tt.c, tt.e, tt.live)
		if got := m.notice != ""; got != tt.noticed {
			t.Errorf("shown %v, done %v, live %v: noticed %q", tt.c == m.chat, tt.e.Done, tt.live, m.notice)
		}
	}
}
