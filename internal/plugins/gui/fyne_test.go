//go:build cgo

package gui

import (
	"context"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"ouroboros/plugin"
)

// fyneFixture is a window with a bridge whose session service records what
// the composer sends. The test touches the window inside on, as the driver
// runs all that touches it on one goroutine.
func fyneFixture(t *testing.T) (*fyneUI, chan string) {
	t.Helper()
	application := newLoopApp(t)
	asked := make(chan string, 1)
	sessions := &testSessions{ask: func(_ context.Context, id, text string) error {
		asked <- id + ": " + text
		return nil
	}}
	bridge := newWindowContext(context.Background(), page{
		choices:  []choice{{name: "herox"}},
		profiles: []*Profile{{sessions: sessions}},
	})
	t.Cleanup(bridge.Close)
	var ui *fyneUI
	on(func() {
		application.Settings().SetTheme(&ouroTheme{pref: "light"})
		native := test.NewWindow(nil)
		ui = newFyneUI(context.Background(), application, native, bridge)
		ui.build()
		ui.thread = bridge.state.Thread
		ui.profile = 0
		ui.profiles = bridge.state.Profiles
		ui.ctx = &fyneContext{profile: 0, id: "owner/s1", state: ui.sessionState(0, "owner/s1")}
		ui.ready.Store(true)
		ui.updateComposer()
	})
	return ui, asked
}

// on runs f on the goroutine of the window and waits for it.
func on(f func()) { fyne.DoAndWait(f) }

// Enter in the composer sends what is written and clears it, as the window
// does.
func TestFyneReturnSends(t *testing.T) {
	ui, asked := fyneFixture(t)
	on(func() {
		ui.draft.SetText("hallo")
		ui.updateComposer()
		ui.win.Canvas().Focus(ui.draft)
		ui.draft.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	})
	select {
	case got := <-asked:
		if got != "s1: hallo" {
			t.Fatalf("asked %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was sent")
	}
	waitFor(t, func() bool { return ui.draft.Text == "" })
	on(func() {
		if notice := ui.ctx.state.notice; notice != tr("composer.sent", nil) {
			t.Errorf("notice %q", notice)
		}
	})
}

// Enter in a disabled composer sends nothing.
func TestFyneReturnWhenNotWritable(t *testing.T) {
	ui, asked := fyneFixture(t)
	on(func() {
		ui.ctx.id = "peer-abc/one"
		ui.draft.SetText("hallo")
		ui.updateComposer()
		ui.win.Canvas().Focus(ui.draft)
		ui.draft.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	})
	select {
	case got := <-asked:
		t.Fatalf("a peer session sent %q", got)
	case <-time.After(50 * time.Millisecond):
	}
	on(func() {
		if ui.draft.Text != "hallo" {
			t.Errorf("the draft is %q", ui.draft.Text)
		}
	})
}

// Both Shift keys allow a newline without sending through the bridge.
func TestFyneShiftReturnInsertsNewline(t *testing.T) {
	for _, shift := range []fyne.KeyName{desktop.KeyShiftLeft, desktop.KeyShiftRight} {
		t.Run(string(shift), func(t *testing.T) {
			ui, asked := fyneFixture(t)
			on(func() {
				ui.win.Canvas().Focus(ui.draft)
				test.Type(ui.draft, "hallo")
				ui.draft.KeyDown(&fyne.KeyEvent{Name: shift})
				ui.draft.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
				ui.draft.KeyUp(&fyne.KeyEvent{Name: shift})
				if ui.draft.Text != "hallo\n" {
					t.Errorf("draft %q", ui.draft.Text)
				}
			})
			select {
			case got := <-asked:
				t.Fatalf("Shift+Enter sent %q", got)
			default:
			}
			on(func() { ui.draft.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEnter}) })
			select {
			case got := <-asked:
				if got != "s1: hallo\n" {
					t.Fatalf("asked %q", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Enter after releasing Shift sent nothing")
			}
		})
	}
}

func TestFyneDraftWithoutLiveStream(t *testing.T) {
	ui, asked := fyneFixture(t)
	on(func() {
		ui.win.Canvas().Focus(ui.draft)
		ui.ready.Store(false)
		ui.setStatus("disconnected")
		ui.updateComposer()
		if ui.draft.Disabled() {
			t.Error("a disconnected owner session must still allow drafting")
		}
		test.Type(ui.draft, "draft")
		if ui.draft.Text != "draft" || ui.ctx.state.text != "draft" {
			t.Error("typing did not preserve the draft")
		}
		if ui.statusLabel.Text != "DISCONNECTED" {
			t.Error("typing changed session status")
		}
		if !ui.send.Disabled() {
			t.Error("sending must stay disabled without a live stream")
		}
		ui.draft.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	})
	select {
	case got := <-asked:
		t.Fatalf("disconnected session sent %q", got)
	case <-time.After(50 * time.Millisecond):
	}
	on(func() {
		ui.ready.Store(true)
		ui.updateComposer()
		if ui.send.Disabled() || ui.draft.Text != "draft" {
			t.Error("reconnecting must enable sending and preserve the draft")
		}
	})
}

func TestFyneTypingDoesNotRefreshSessionStatus(t *testing.T) {
	ui, _ := fyneFixture(t)
	on(func() {
		ui.setStatus("unchanged")
		test.Type(ui.draft, "hello")
		if ui.statusLabel.Text != "UNCHANGED" {
			t.Error("typing refreshed session status")
		}
		if ui.send.Disabled() {
			t.Error("typing must enable sending")
		}
		ui.draft.SetText(" ")
		if !ui.send.Disabled() {
			t.Error("a blank draft must disable sending")
		}
	})
}

func TestSessionPhaseOf(t *testing.T) {
	at := time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC)
	question := &ChatQuestion{Text: "Which?", Options: []string{"a", "b"}}
	cases := []struct {
		name    string
		trace   []ChatEvent
		running bool
		phase   string
		word    string
		next    string
		live    bool
		what    string
	}{
		{
			name:  "nothing yet",
			phase: "empty", word: tr("phase.empty", nil), next: tr("phase.nextNothing", nil),
		},
		{
			name:    "a call on its way",
			trace:   []ChatEvent{{Kind: plugin.SessionStep, Tool: "read", Text: "read · a file", At: at}},
			running: true, phase: "tool", word: tr("phase.calling", nil), next: tr("phase.nextResult", map[string]any{"tool": "read"}),
			live: true, what: "a file",
		},
		{
			name:    "a call that ended",
			trace:   []ChatEvent{{Kind: plugin.SessionStep, Tool: "read", Text: "read · a file", Done: true, At: at}},
			running: true, phase: "thinking", word: tr("phase.thinking", nil), next: tr("phase.nextTool", nil), live: true,
		},
		{
			name:    "a question",
			trace:   []ChatEvent{{Kind: plugin.SessionAsked, Question: question, At: at}},
			running: true, phase: "asked", word: tr("phase.asked", nil), next: tr("phase.nextAnswer", nil), live: true,
		},
		{
			name:    "an answer",
			trace:   []ChatEvent{{Kind: plugin.SessionAnswer, Text: "done", At: at}},
			running: false, phase: "done", word: tr("phase.done", nil), next: tr("phase.nextNothing", nil),
		},
		{
			name:    "a stopped answer",
			trace:   []ChatEvent{{Kind: plugin.SessionCanceled, At: at}},
			running: false, phase: "canceled", word: tr("phase.canceled", nil), next: tr("phase.nextNothing", nil),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionPhaseOf(tc.trace, tc.running)
			if got.phase != tc.phase || got.word() != tc.word || got.next() != tc.next || got.live != tc.live {
				t.Fatalf("phase %+v: word %q next %q live %v", got, got.word(), got.next(), got.live)
			}
			if tc.what != "" && got.what() != tc.what {
				t.Fatalf("what %q", got.what())
			}
		})
	}
}

func TestToolChartOf(t *testing.T) {
	start := time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC)
	calls := []ChatEvent{
		{Kind: plugin.SessionStep, Tool: "read", Text: "read · a", Done: true, At: start},
		{Kind: plugin.SessionStep, Tool: "bash", Text: "bash · b", Done: true, At: start.Add(time.Minute)},
		{Kind: plugin.SessionStep, Tool: "read", Text: "read · c", Done: true, At: start.Add(2 * time.Minute)},
	}
	pending := ChatEvent{Kind: plugin.SessionStep, Tool: "bash", Text: "bash · d", At: start.Add(3 * time.Minute)}
	chart := toolChartOf(calls, &pending, true)
	if len(chart.lanes) != 2 || chart.lanes[0].name != "read" || chart.lanes[0].count != 2 {
		t.Fatalf("lanes %+v", chart.lanes)
	}
	if len(chart.ticks) != 4 || !chart.running {
		t.Fatalf("ticks %+v running %v", chart.ticks, chart.running)
	}
	if chart.ticks[0].at < 0 || chart.ticks[3].at > 1 || chart.ticks[0].at >= chart.ticks[3].at {
		t.Fatalf("ticks %+v", chart.ticks)
	}
	if chart.height() != 2*chartRow+2*chartPad {
		t.Fatalf("height %v", chart.height())
	}
}

func TestDateGroupAndClock(t *testing.T) {
	now := time.Date(2026, 10, 22, 15, 0, 0, 0, time.Local)
	cases := []struct {
		value time.Time
		group string
		clock string
	}{
		{now.Add(-time.Hour), tr("date.today", nil), "14:00"},
		{time.Date(2026, 10, 21, 9, 5, 0, 0, time.Local), tr("date.yesterday", nil), "21 Oct 2026, 09:05"},
		{time.Date(2026, 9, 1, 9, 5, 0, 0, time.Local), tr("date.older", nil), "01 Sep 2026, 09:05"},
		{time.Time{}, tr("date.undated", nil), ""},
	}
	for _, tc := range cases {
		if got := dateGroup(tc.value, now); got != tc.group {
			t.Fatalf("group of %v is %q, want %q", tc.value, got, tc.group)
		}
		if got := clock(tc.value, now); got != tc.clock {
			t.Fatalf("clock of %v is %q, want %q", tc.value, got, tc.clock)
		}
	}
}

func TestFyneThemePalette(t *testing.T) {
	light := &ouroTheme{pref: "light"}
	dark := &ouroTheme{pref: "dark"}
	if light.Color(theme.ColorNameBackground, theme.VariantDark) != lightPalette.background {
		t.Fatal("the light window follows the dark variant")
	}
	if dark.Color(theme.ColorNameBackground, theme.VariantLight) != darkPalette.background {
		t.Fatal("the dark window follows the light variant")
	}
	if monoFont(fyne.TextStyle{Monospace: true, Bold: true}) == monoFont(fyne.TextStyle{Monospace: true}) {
		t.Fatal("bold draws in the regular face")
	}
}

func TestSessionIDHelpers(t *testing.T) {
	if !strings.HasPrefix("owner/s1", "owner/") {
		t.Fatal("owner prefix")
	}
	if got := chartLaneName("a-very-long-tool-name"); got != "a-very-long…" {
		t.Fatalf("lane name %q", got)
	}
	if got := cutText("0123456789"); got != "0123456789" {
		t.Fatalf("cut %q", got)
	}
}
