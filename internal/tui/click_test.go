package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// drawnOf is what a dialog draws, without where it is clicked.
func drawnOf(dialog string, _ []hit) string { return dialog }

// linesOf are the lines of the list of the models, without their models.
func linesOf(lines []string, _ []int) []string { return lines }

// clickOn clicks the left button on where the screen shows text, the n-th
// time it shows it, from the first on, and returns what the click asks for.
func clickOn(t *testing.T, m *Model, text string, n int) tea.Cmd {
	t.Helper()
	for y, l := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		for from := 0; ; {
			i := strings.Index(l[from:], text)
			if i < 0 {
				break
			}
			if n == 0 {
				x := ansi.StringWidth(l[:from+i])
				return mouseAt(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
			}
			n, from = n-1, from+i+len(text)
		}
	}
	t.Fatalf("the screen shows no %q:\n%s", text, ansi.Strip(m.View().Content))
	return nil
}

// In a dialog a click selects, a click on what is selected does it, and a
// click beside the dialog closes it.
func TestClicksInTheModelDialog(t *testing.T) {
	m, b := newModel(t)
	b.status.Model = "gpt-4o"
	b.offers = []Models{local("gpt-4o", "gpt-4o-mini")}
	m.Update(m.refreshStatus())
	deliver(m, run(m, "/model"))
	if clickOn(t, m, "gpt-4o-mini", 0); m.picker.sel != 1 || len(b.set) != 0 {
		t.Fatalf("a click selects: %d, set %v", m.picker.sel, b.set)
	}
	if cmd := clickOn(t, m, "gpt-4o-mini", 0); cmd == nil {
		t.Fatal("a click on the model selected sets it")
	} else {
		m.Update(cmd())
	}
	if len(b.set) != 1 || !strings.HasSuffix(b.set[0], "gpt-4o-mini") || m.screen != screenChat {
		t.Fatalf("set %v, screen %v", b.set, m.screen)
	}
	deliver(m, run(m, "/model"))
	if clickOn(t, m, "Model", 0); m.screen != screenModel {
		t.Fatal("a click on the title of a dialog does something")
	}
	if mouseAt(m, tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft}); m.screen != screenChat {
		t.Fatal("a click beside the dialog closes it")
	}
	run(m, "/keys")
	if clickOn(t, m, "ctrl+k", 0); m.screen != screenKeys {
		t.Fatal("a click in the keys does something")
	}
}

// A click on a conversation in the list shows it, and one on the tab that
// is not open opens it.
func TestClicksInTheSessionsDialog(t *testing.T) {
	m, _ := newModel(t)
	answered(t, m, say(t, m, "first question"))
	first := m.chat
	settle(m, run(m, "/new"))
	m.Update(ctrlS)
	if clickOn(t, m, "Peers", 0); !m.sessions.peerTab {
		t.Fatal("a click on Peers opens the conversations of peers")
	}
	clickOn(t, m, "Sessions", 0)
	if m.sessions.peerTab {
		t.Fatal("a click on Sessions opens the sessions")
	}
	if clickOn(t, m, "first question", 0); m.chat != first || m.screen != screenChat {
		t.Fatalf("a click on a conversation shows it: screen %v", m.screen)
	}
}

// A click on an effort moves the slider there, and one on the effort it is
// at sets it.
func TestClicksOnTheEffort(t *testing.T) {
	m, b := newModel(t)
	b.efforts, b.effortsKnown = []string{"low", "medium", "high"}, true
	run(m, "/effort")
	at := m.effort.at
	if clickOn(t, m, "high", 0); m.effort.choices[m.effort.at] != "high" || m.effort.at == at || m.screen != screenEffort {
		t.Fatalf("the slider is at %d of %v", m.effort.at, m.effort.choices)
	}
	cmd := clickOn(t, m, "high", 0)
	if cmd == nil || m.screen != screenChat {
		t.Fatal("a click on the effort the slider is at sets it")
	}
	if msg, ok := cmd().(actionMsg); !ok || msg.err != nil || b.status.Effort != "high" {
		t.Fatalf("set %+v, effort %q", msg, b.status.Effort)
	}
}

// A click on a job selects it, and one on the job selected stops it.
func TestClicksOnTheJobs(t *testing.T) {
	m, _ := newModel(t)
	at := m.now()
	m.screen = screenJobs
	m.jobs.loaded, m.jobs.list = true, []plugin.Job{{Session: "s", N: 1, Command: "serve", Since: at}, {Session: "s", N: 2, Command: "watch", Since: at}}
	m.jobs.services = []plugin.ServiceInfo{{ID: "t1", Name: "web", Command: "npm run dev", Since: at, Until: at.Add(time.Hour)}}
	if clickOn(t, m, "job 2 · watch", 0); m.jobs.sel != 1 {
		t.Fatalf("selected %d", m.jobs.sel)
	}
	if clickOn(t, m, "service web", 0); m.jobs.sel != 2 {
		t.Fatalf("selected %d", m.jobs.sel)
	}
	if cmd := clickOn(t, m, "service web", 0); cmd == nil {
		t.Fatal("a click on the service selected stops it")
	}
}

// A click on a suggestion selects it, and one on accept or reject below the
// one selected decides it; Accept all is selected first, too.
func TestClicksOnTheSuggestions(t *testing.T) {
	m, b := newModel(t)
	b.trust = Contacts{Suggestions: []plugin.Suggestion{{ID: "s1", Text: "first change"}, {ID: "s2", Text: "second change"}}}
	run(m, "/suggestions")
	m.Update(m.loadContacts())
	if clickOn(t, m, "second change", 0); m.suggestionRequests.sel != 1 {
		t.Fatalf("selected %d", m.suggestionRequests.sel)
	}
	cmd := clickOn(t, m, "reject", 1) // what the dialog says of it comes first
	if cmd == nil {
		t.Fatal("a click on reject decides")
	}
	if cmd(); !slices.Equal(b.rejected, []string{"suggestion s2"}) || m.choice != reject {
		t.Fatalf("rejected %v, choice %d", b.rejected, m.choice)
	}
	if clickOn(t, m, "Accept all", 0); m.suggestionRequests.sel != 2 {
		t.Fatalf("Accept all is not selected: %d", m.suggestionRequests.sel)
	}
	if clickOn(t, m, "Accept all", 0) == nil {
		t.Fatal("a click on Accept all selected accepts them all")
	}
}

// Scrolled down, the suggestions are clicked where they are drawn.
func TestClicksOnScrolledSuggestions(t *testing.T) {
	m, b := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	for _, id := range []string{"s1", "s2", "s3", "s4"} {
		b.trust.Suggestions = append(b.trust.Suggestions, plugin.Suggestion{ID: id, Text: "change " + id})
	}
	run(m, "/suggestions")
	m.Update(m.loadContacts())
	press(m, "down", "down", "down")
	if clickOn(t, m, "change s3", 0); m.suggestionRequests.sel != 2 {
		t.Fatalf("selected %d", m.suggestionRequests.sel)
	}
}

// A click on a command of the help runs it.
func TestClickOnACommandOfTheHelp(t *testing.T) {
	m, _ := newModel(t)
	was := m.chat
	run(m, "/help")
	if settle(m, clickOn(t, m, "/new", 0)); m.chat == was || m.screen != screenChat {
		t.Fatal("a click on /new starts a conversation")
	}
}

// In the settings a click on a category selects it, one on it again or on
// open opens it, and one on a line of it selects it.
func TestClicksInTheSettings(t *testing.T) {
	m, b := newModel(t)
	b.settings = Settings{Name: "alpha"}
	deliver(m, run(m, "/settings"))
	if clickOn(t, m, "The agents", 0); m.config.category != 1 || m.config.opened {
		t.Fatalf("category %d, opened %v", m.config.category, m.config.opened)
	}
	if clickOn(t, m, "Info", 0); m.config.info == nil {
		t.Fatal("a click on Info opens it")
	}
	if clickOn(t, m, "The agents · Info", 0); m.config.info == nil {
		t.Fatal("a click on the explanation does something")
	}
	press(m, "esc")
	if clickOn(t, m, "The agents", 0); !m.config.opened {
		t.Fatal("a click on the category selected opens it")
	}
	rows := m.config.shown(m)
	if len(rows) < 2 {
		t.Fatalf("rows %+v", rows)
	}
	if clickOn(t, m, rows[1].label, 0); m.config.sel != 1 {
		t.Fatalf("selected %d", m.config.sel)
	}
	if names := m.config.selectedRow(m).actions; len(names) > 0 {
		clickOn(t, m, names[0].name, 0)
	}
}

// A click on an action of the screen of a plugin runs it.
func TestClickOnAnActionOfAPlugin(t *testing.T) {
	m, c := extended(t)
	m.Update(run(m, "/greetings")())
	if clickOn(t, m, "Greetings", 0) != nil {
		t.Fatal("a click on the title runs something")
	}
	ran := clickOn(t, m, "wink", 0)
	if ran == nil || m.choice != 1 {
		t.Fatal("a click on wink runs it")
	}
	if ran(); !slices.Equal(c.ran, []string{"wink"}) {
		t.Fatalf("ran %v", c.ran)
	}
}

// A click on an answer of a question gives it, and one on what to do below
// the answers selects the composer for own words.
func TestClicksOnAQuestion(t *testing.T) {
	m, _ := asked(t)
	a := m.chat.asking
	clickOn(t, m, "Choose an answer", 0)
	if !a.own() {
		t.Fatalf("a click on what to do selects the composer: %d", a.sel)
	}
	cmd := clickOn(t, m, "SQLite", 0)
	if cmd == nil || !a.sent {
		t.Fatal("a click on an answer gives it")
	}
	if clickOn(t, m, "SQLite", 0) != nil {
		t.Fatal("an answer is given once")
	}
}

// A click on a command that is offered runs it, and one into the composer
// puts the cursor there.
func TestClicksAboveAndInTheComposer(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.PasteMsg{Content: "/he"})
	if clickOn(t, m, "/help", 0); m.screen != screenHelp {
		t.Fatalf("a click on /help opens it: %v", m.screen)
	}
	press(m, "esc")
	m.input.Reset()
	m.Update(tea.PasteMsg{Content: "first\nsecond"})
	clickOn(t, m, "second", 0)
	if m.input.Line() != 1 || m.input.LineInfo().ColumnOffset != 0 {
		t.Fatalf("the cursor is on line %d, column %d", m.input.Line(), m.input.LineInfo().ColumnOffset)
	}
	clickOn(t, m, "irst", 0)
	if m.input.Line() != 0 || m.input.LineInfo().ColumnOffset != 1 {
		t.Fatalf("the cursor is on line %d, column %d", m.input.Line(), m.input.LineInfo().ColumnOffset)
	}
}

// A click on a session in the column on the right shows it; one on its
// head shows nothing.
func TestClickOnASessionOnTheRight(t *testing.T) {
	m, _, fix := threadWithSessions(t, 170, 46)
	if clickOn(t, m, "RUNNING", 0); m.chat == fix {
		t.Fatal("a click on the head of the column shows a session")
	}
	if clickOn(t, m, "FIX · WORKING", 0); m.chat != fix {
		t.Fatal("a click on a session on the right shows it")
	}
}
