//go:build cgo

package gui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"ouroboros/internal/chat"
	"ouroboros/plugin"
)

// tcText is all the text an object shows, joined by newlines.
func tcText(o fyne.CanvasObject) string {
	var out []string
	var walk func(o fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch o := o.(type) {
		case nil:
		case *tapText:
			out = append(out, o.Text)
		case *widget.Label:
			out = append(out, o.Text)
		case *widget.RichText:
			out = append(out, o.String())
		case *markdownText:
			out = append(out, o.Text())
		case *fyne.Container:
			for _, child := range o.Objects {
				walk(child)
			}
		case fyne.Widget:
			for _, child := range test.WidgetRenderer(o).Objects() {
				walk(child)
			}
		}
	}
	walk(o)
	return strings.Join(out, "\n")
}

// tcTapTexts are the texts that act as buttons in an object.
func tcTapTexts(o fyne.CanvasObject) []*tapText {
	var out []*tapText
	var walk func(o fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch o := o.(type) {
		case *tapText:
			out = append(out, o)
		case *fyne.Container:
			for _, child := range o.Objects {
				walk(child)
			}
		}
	}
	walk(o)
	return out
}

// tcLast is the text of the newest message of the transcript.
func tcLast(ui *fyneUI) string {
	return tcText(ui.content.Objects[len(ui.content.Objects)-1])
}

// tcSession opens a session of the owner on its own and waits until it is
// read.
func tcSession(t *testing.T, ui *fyneUI, entry SessionEntry) {
	t.Helper()
	fyne.DoAndWait(func() { ui.read(entry) })
	waitFor(t, func() bool { return ui.ready.Load() && ui.selected == entry.ID })
}

// Every kind of event the thread tells shows as the window shows it: queued
// messages, failed and stopped answers, answered questions, and what does not
// belong to the transcript at all.
func TestFyneTranscriptEventKinds(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	now := time.Now()
	fyne.DoAndWait(func() {
		before := len(ui.content.Objects)
		apply := func(events ...chat.Event) { ui.applyEvents(events) }

		apply(chat.Event{Kind: plugin.SessionQueued, Text: "Later, please.", At: now})
		if got := tcLast(ui); !strings.Contains(got, "Later, please.") || !strings.Contains(got, tr("speaker.queue", nil)) {
			t.Errorf("queued message shows %q", got)
		}
		apply(chat.Event{Kind: plugin.SessionQueued, At: now})
		if got := tcLast(ui); !strings.Contains(got, tr("queue.text", nil)) {
			t.Errorf("queued message without text shows %q", got)
		}
		apply(chat.Event{Kind: plugin.SessionFailed, At: now})
		if got := tcLast(ui); !strings.Contains(got, tr("end.failed", nil)) || !strings.Contains(got, tr("speaker.status", nil)) {
			t.Errorf("failed answer shows %q", got)
		}
		apply(chat.Event{Kind: plugin.SessionFailed, Text: "the provider is down", At: now})
		if got := tcLast(ui); !strings.Contains(got, "the provider is down") {
			t.Errorf("failed answer with reason shows %q", got)
		}
		apply(chat.Event{Kind: plugin.SessionCanceled, Text: "ignored", At: now})
		if got := tcLast(ui); !strings.Contains(got, tr("end.stopped", nil)) || strings.Contains(got, "ignored") {
			t.Errorf("stopped answer shows %q", got)
		}
		apply(chat.Event{Kind: plugin.SessionAsked, Text: "Tabs or one page?", At: now})
		question := ui.content.Objects[len(ui.content.Objects)-1].(*fyne.Container)
		if _, right := question.Layout.(ownerMessageLayout); right {
			t.Fatal("the agent's question is on the owner's side")
		}
		apply(chat.Event{Kind: plugin.SessionAsked, Done: true, Text: "Tabs", At: now})
		reply := ui.content.Objects[len(ui.content.Objects)-1].(*fyne.Container)
		reply.Resize(fyne.NewSize(600, 150))
		if _, right := reply.Layout.(ownerMessageLayout); !right || reply.Objects[0].Position().X <= 0 {
			t.Fatal("the owner's reply is not on the right")
		}
		if _, right := question.Layout.(ownerMessageLayout); right {
			t.Fatal("answering moved the original question to the right")
		}
		if got := tcLast(ui); !strings.Contains(got, tr("speaker.answered", nil)) || !strings.Contains(got, "Tabs") {
			t.Errorf("answered question shows %q", got)
		}
		if ui.asking != nil {
			t.Error("an answered question waits for an answer")
		}
		if len(ui.content.Objects) != before+7 {
			t.Errorf("%d messages added, want 7", len(ui.content.Objects)-before)
		}

		// A thought shows as such, and calls of the model and empty words
		// add nothing.
		count := len(ui.content.Objects)
		apply(chat.Event{Kind: plugin.SessionThought, Text: "hmm", At: now})
		apply(chat.Event{Kind: plugin.SessionCalling, At: now},
			chat.Event{Kind: plugin.SessionSay, Text: "   ", At: now},
			chat.Event{Kind: plugin.SessionSuggestion, Text: "", At: now})
		if len(ui.content.Objects) != count+1 {
			t.Errorf("a thought and empty words added %d messages, want 1", len(ui.content.Objects)-count)
		}

		// An answer of the thread ends its own work; the sessions it handed
		// messages to are counted beside it, as in the terminal UI.
		apply(chat.Event{Kind: plugin.SessionMessage, Text: "go on", At: now})
		apply(chat.Event{Kind: plugin.SessionWorking, N: 2, Own: true})
		if !ui.ctx.session.Pending || ui.ctx.session.Active() != 1 {
			t.Errorf("working: %+v", ui.ctx.session)
		}
		apply(chat.Event{Kind: plugin.SessionAnswer, At: now})
		if ui.ctx.session.Pending || ui.ctx.session.Active() != 1 {
			t.Errorf("after its answer: %+v", ui.ctx.session)
		}
		apply(chat.Event{Kind: plugin.SessionWorking})
		if ui.ctx.session.Active() != 0 {
			t.Errorf("idle: %+v", ui.ctx.session)
		}

		// The workspace of the session stands beside its title.
		apply(chat.Event{Kind: sessionDirectory, Text: "/home/lucas/work"})
		if ui.meta.Text != "/home/lucas/work" {
			t.Errorf("meta %q", ui.meta.Text)
		}
		apply(chat.Event{Kind: sessionDirectory})
		if ui.meta.Text != tr("session.workspace", nil) {
			t.Errorf("meta without workspace %q", ui.meta.Text)
		}

		// The thread keeps its title.
		title := ui.title.String()
		apply(chat.Event{Kind: plugin.SessionTitle, Text: "Renamed"})
		if ui.title.String() != title {
			t.Errorf("the thread was renamed to %q", ui.title.String())
		}
	})
}

// A session shown on its own takes the title it is given, in the header and
// in the list of sessions.
func TestFyneTranscriptSessionTitle(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	tcSession(t, ui, SessionEntry{ID: "owner/s1", Title: "Review the sidebar"})
	fyne.DoAndWait(func() {
		ui.applyEvents([]chat.Event{{Kind: plugin.SessionTitle}})
		if got := ui.title.String(); got != "REVIEW THE SIDEBAR" {
			t.Errorf("an empty title changed the header to %q", got)
		}
		ui.applyEvents([]chat.Event{{Kind: plugin.SessionTitle, Text: "Sidebar review"}})
		if got := ui.title.String(); got != "SIDEBAR REVIEW" {
			t.Errorf("header %q", got)
		}
		renamed := false
		for _, entry := range ui.entries {
			if entry.ID == "owner/s1" {
				renamed = entry.Title == "Sidebar review"
			}
		}
		if !renamed {
			t.Errorf("entries %+v", ui.entries)
		}
	})
}

// What came before the six hours the thread shows stays in the archive, and
// what the thread hands a session opens the part of the thread it is in.
func TestFyneTranscriptCutoffAndHanded(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	old := time.Now().Add(-threadWindow - time.Hour)
	fyne.DoAndWait(func() {
		thread := ui.chat
		defer func() { ui.chat = thread }()

		// Without a chat there is nothing to apply to.
		ui.chat = nil
		count := len(ui.content.Objects)
		ui.applyEvents([]chat.Event{{Kind: plugin.SessionMessage, Text: "lost", At: time.Now()}})
		if len(ui.content.Objects) != count {
			t.Error("events applied without a chat")
		}

		talk := newFyneChat(ui, 0, ui.thread, true)
		ui.chat = talk
		ui.applyEvents([]chat.Event{{Kind: plugin.SessionHanded, Text: "s5", At: old}})
		if talk.exchange == nil || !talk.exchange.sessions["s5"] {
			t.Fatalf("the handed session is in no part: %+v", talk.exchange)
		}
		ui.applyEvents([]chat.Event{
			{Kind: plugin.SessionMessage, Text: "An old message", At: old},
			{Kind: plugin.SessionTask, Text: "An old task", At: old},
		})
		if talk.shown || len(ui.content.Objects) != count {
			t.Errorf("events before the window show: shown %v, %d added", talk.shown, len(ui.content.Objects)-count)
		}
		ui.applyEvents([]chat.Event{{Kind: plugin.SessionTask, Text: "A new task", At: time.Now()}})
		if !talk.shown || len(ui.content.Objects) != count+1 || !strings.Contains(tcLast(ui), "A new task") {
			t.Errorf("a recent task does not show: %q", tcLast(ui))
		}
		if !strings.Contains(tcLast(ui), tr("speaker.task", nil)) {
			t.Errorf("task line %q", tcLast(ui))
		}
	})

	// A session handed to on the part in view joins the sidebar and the
	// rail at once.
	fyne.DoAndWait(func() {
		ui.inspecting = nil
		ui.chat.exchange = &fyneExchange{at: time.Now(), sessions: map[string]bool{"s2": true}}
		ui.section = ui.chat.exchange
		ui.renderList()
		if strings.Contains(tcText(ui.sidebarList), "REVIEW THE SIDEBAR") {
			t.Fatal("s1 is in the sidebar before it is handed to")
		}
		ui.applyEvents([]chat.Event{{Kind: plugin.SessionHanded, Text: "s1", At: time.Now()}})
		if !strings.Contains(tcText(ui.sidebarList), "REVIEW THE SIDEBAR") {
			t.Errorf("sidebar %q", tcText(ui.sidebarList))
		}
	})
	waitFor(t, func() bool { return len(ui.contextDetails.Objects) == 2 })
}

// A very long text shows cut, and opens and closes on the owner's word.
func TestFyneTranscriptExpandableText(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	long := strings.Repeat("ä", textLimit+5)
	fyne.DoAndWait(func() {
		ui.applyEvents([]chat.Event{{Kind: plugin.SessionSay, Text: long, At: time.Now()}})
		buttons := tcTapTexts(ui.content.Objects[len(ui.content.Objects)-1])
		if len(buttons) != 1 {
			t.Fatalf("%d buttons on a long text", len(buttons))
		}
		button := buttons[0]
		if button.Text != tr("transcript.expandCount", map[string]any{"n": textLimit + 5}) {
			t.Errorf("button %q", button.Text)
		}
		var body *markdownText
		for _, label := range labelsIn(ui.content.Objects[len(ui.content.Objects)-1]) {
			if l, ok := label.(*markdownText); ok {
				body = l
			}
		}
		if body == nil || len([]rune(body.Text())) != textLimit {
			t.Errorf("the long text is not cut: %v", body)
			return
		}
		ui.following = true
		button.Tapped(nil)
		if ui.following || body.Text() != long || button.Text != tr("transcript.collapse", nil) {
			t.Errorf("expanded: following %v, %d runes, button %q", ui.following, len([]rune(body.Text())), button.Text)
		}
		button.Tapped(nil)
		if len([]rune(body.Text())) != textLimit || button.Text != tr("transcript.expand", nil) {
			t.Errorf("collapsed: %d runes, button %q", len([]rune(body.Text())), button.Text)
		}
	})
	if cutText("short") != "short" {
		t.Error("a short text is cut")
	}
}

// The line of a call names what it does, its tool, or that it is a tool.
func TestFyneTranscriptStepLines(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	fyne.DoAndWait(func() {
		ui.applyEvents([]chat.Event{
			{Kind: plugin.SessionStep, Tool: "bash", At: time.Now()},
			{Kind: plugin.SessionStep, At: time.Now()},
			{Kind: plugin.SessionStep, CallID: "x9", At: time.Now()},
		})
		calls := ui.chat.calls
		if calls["bash"] == nil || calls["bash"].summary.Text != "bash" {
			t.Errorf("call without ID: %+v", calls["bash"])
		}
		if calls[""] == nil || calls[""].summary.Text != tr("speaker.tool", nil) {
			t.Errorf("call without tool: %+v", calls[""])
		}
		if calls["x9"] == nil || calls["x9"].summary.Text != tr("speaker.tool", nil)+tr("tool.expandHint", nil) {
			t.Errorf("call with ID: %+v", calls["x9"])
		}
	})
}

// Opening a call loads its result once: what failed, what the archive lacks,
// an empty result, and an answer that comes after the view changed.
func TestFyneTranscriptToggle(t *testing.T) {
	sessions := newE2ESessions()
	slow := make(chan struct{})
	held := make(chan struct{}, 4)
	sessions.details = func(_ context.Context, _ string, call string) (plugin.SessionToolResult, error) {
		switch call {
		case "broken":
			return plugin.SessionToolResult{}, errors.New("disk on fire")
		case "gone":
			return plugin.SessionToolResult{}, nil
		case "empty":
			return plugin.SessionToolResult{Found: true}, nil
		case "slow", "stale":
			held <- struct{}{}
			<-slow
		}
		return plugin.SessionToolResult{Found: true, Content: "result of " + call}, nil
	}
	ui := e2eWindowWith(t, fyne.NewSize(1400, 900), sessions)
	calls := map[string]*fyneToolCall{}
	fyne.DoAndWait(func() {
		var events []chat.Event
		for _, id := range []string{"broken", "gone", "empty", "slow", "stale"} {
			events = append(events, chat.Event{Kind: plugin.SessionStep, Tool: "read", CallID: id, Done: true, At: time.Now()})
		}
		events = append(events, chat.Event{Kind: plugin.SessionStep, Tool: "plain", Done: true, At: time.Now()})
		ui.applyEvents(events)
		for _, id := range []string{"broken", "gone", "empty", "slow", "stale", "plain"} {
			calls[id] = ui.chat.calls[id]
		}
		for _, id := range []string{"broken", "gone", "empty"} {
			ui.chat.toggle(calls[id])
		}
		// A call without an ID has nothing to load.
		ui.chat.toggle(calls["plain"])
		if !calls["plain"].details.Visible() || calls["plain"].loading {
			t.Error("a call without ID loads")
		}
	})
	waitFor(t, func() bool {
		return calls["broken"].note.Text == tr("tool.failed", map[string]any{"error": errors.New("disk on fire")})
	})
	waitFor(t, func() bool { return calls["gone"].note.Text == tr("tool.missing", nil) })
	waitFor(t, func() bool { return calls["empty"].loaded })
	fyne.DoAndWait(func() {
		if calls["broken"].loaded || calls["gone"].loaded {
			t.Error("a failed load counts as loaded")
		}
		if got := tcText(calls["empty"].details); !strings.Contains(got, tr("tool.empty", nil)) {
			t.Errorf("empty result shows %q", got)
		}
		// Open again, what was loaded shows in the rail without a load.
		ui.chat.toggle(calls["empty"])
		ui.inspecting = nil
		ui.chat.toggle(calls["empty"])
		if ui.inspecting == nil || !calls["empty"].details.Visible() {
			t.Error("a loaded result does not show again")
		}
	})

	// While a result loads, opening it again loads nothing more; the owner
	// at the end keeps following once it is there.
	fyne.DoAndWait(func() { ui.chat.toggle(calls["slow"]) })
	<-held
	fyne.DoAndWait(func() {
		ui.chat.toggle(calls["slow"])
		ui.chat.toggle(calls["slow"])
		if !calls["slow"].loading || !calls["slow"].details.Visible() {
			t.Error("the loading call is not open")
		}
		ui.following = true
	})
	slow <- struct{}{}
	waitFor(t, func() bool { return calls["slow"].loaded })
	fyne.DoAndWait(func() {
		if !ui.following {
			t.Error("the owner stopped following")
		}
	})

	// A result that comes after the view changed is dropped.
	fyne.DoAndWait(func() { ui.chat.toggle(calls["stale"]) })
	<-held
	fyne.DoAndWait(func() { ui.epoch.Add(1) })
	slow <- struct{}{}
	time.Sleep(50 * time.Millisecond)
	fyne.DoAndWait(func() {})
	fyne.DoAndWait(func() {
		if calls["stale"].loaded || !calls["stale"].loading {
			t.Errorf("a stale result was shown: loaded %v", calls["stale"].loaded)
		}
	})
}

// The window lays the transcript and the composer out at once outside a
// batch of events.
func TestFyneTranscriptChangedOutsideBatch(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(900, 640))
	fyne.DoAndWait(func() {
		ui.contentChanged()
		ui.composerChanged()
		if ui.contentDirty || ui.composerDirty {
			t.Error("a change outside a batch waits for one")
		}
	})
}

// Where a session stands reads from its trace for every phase.
func TestSessionPhaseOfMore(t *testing.T) {
	at := time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		trace    []chat.Event
		running  bool
		phase    string
		word     string
		next     string
		question string
		jobs     string
	}{
		{
			name:    "a question answered",
			trace:   []chat.Event{{Kind: plugin.SessionAsked, Text: "Which?", At: at}, {Kind: plugin.SessionAsked, Done: true, At: at}},
			running: true, phase: "thinking", word: tr("phase.thinking", nil), next: tr("phase.nextTool", nil),
		},
		{
			name:    "a question without text",
			trace:   []chat.Event{{Kind: plugin.SessionAsked, At: at}},
			running: true, phase: "asked", word: tr("phase.asked", nil), next: tr("phase.nextAnswer", nil),
		},
		{
			name:    "writing an answer",
			trace:   []chat.Event{{Kind: plugin.SessionMessage, At: at}, {Kind: plugin.SessionAnswering, At: at}, {Kind: plugin.SessionJobs, Text: "  make test \n"}},
			running: true, phase: "answering", word: tr("phase.answering", nil), next: tr("phase.nextEnd", nil), jobs: "make test",
		},
		{
			name:    "an answer left unwritten",
			trace:   []chat.Event{{Kind: plugin.SessionAnswering}},
			running: false, phase: "answering", word: tr("phase.ready", nil), next: tr("phase.nextNothing", nil),
		},
		{
			name:    "writing while asked",
			trace:   []chat.Event{{Kind: plugin.SessionAsked, Text: "Which?"}, {Kind: plugin.SessionAnswering}, {Kind: plugin.SessionSay}},
			running: true, phase: "asked", word: tr("phase.asked", nil), next: tr("phase.nextAnswer", nil), question: "Which?",
		},
		{
			name:    "a thought left unthought",
			trace:   []chat.Event{{Kind: plugin.SessionTask}},
			running: false, phase: "thinking", word: tr("phase.ready", nil), next: tr("phase.nextNothing", nil),
		},
		{
			name:    "a failed answer",
			trace:   []chat.Event{{Kind: plugin.SessionStep, Tool: "bash"}, {Kind: plugin.SessionFailed, At: at}},
			running: true, phase: "failed", word: tr("phase.failed", nil), next: tr("phase.nextNothing", nil),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionPhaseOf(tc.trace, tc.running)
			if got.phase != tc.phase || got.word() != tc.word || got.next() != tc.next {
				t.Fatalf("phase %+v: word %q next %q", got, got.word(), got.next())
			}
			if got.question != tc.question || got.jobs != tc.jobs {
				t.Fatalf("question %q jobs %q", got.question, got.jobs)
			}
			if tc.phase == "failed" && got.call != nil {
				t.Fatal("a failed answer keeps its call")
			}
		})
	}
}

// What a call does is its text without its tool.
func TestFynePhaseWhat(t *testing.T) {
	live := func(call chat.Event) fynePhase { return fynePhase{phase: "tool", live: true, call: &call} }
	cases := []struct {
		phase fynePhase
		want  string
	}{
		{live(chat.Event{Tool: "read", Text: "read · a file"}), "a file"},
		{live(chat.Event{Tool: "read", Text: "read"}), ""},
		{live(chat.Event{Tool: "read", Text: "something else"}), "something else"},
		{live(chat.Event{Text: tr("common.tool", nil) + " · x"}), "x"},
		{live(chat.Event{Text: "plain"}), "plain"},
		{fynePhase{phase: "tool", call: &chat.Event{Text: "x"}}, ""},
		{fynePhase{phase: "tool", live: true}, ""},
	}
	for _, tc := range cases {
		if got := tc.phase.what(); got != tc.want {
			t.Errorf("what of %+v = %q, want %q", tc.phase.call, got, tc.want)
		}
	}
	if got := (fynePhase{phase: "tool", live: true}).next(); got != tr("phase.nextResult", map[string]any{"tool": tr("common.tool", nil)}) {
		t.Errorf("next without call %q", got)
	}
}

// A session in the rail tells its name, its state, its question, what it
// did last, what runs and its tools.
func TestFyneArtefactBlock(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	at := time.Now().Add(-time.Minute)
	fyne.DoAndWait(func() {
		working := ui.artefactBlock(SessionEntry{ID: "owner/x1", Running: true}, []chat.Event{
			{Kind: plugin.SessionAsked, Text: "Which file?", At: at},
			{Kind: plugin.SessionJobs, Text: "make test\nmake lint"},
			{Kind: plugin.SessionStep, Tool: "grep", Text: "grep · pattern", At: at},
		})
		got := tcText(working)
		for _, want := range []string{
			strings.ToUpper("owner/x1" + tr("artefact.working", nil)), tr("phase.calling", nil), "pattern",
			"Which file?", tr("artefact.noCall", nil), "make test …",
			tr("artefact.oneTool", map[string]any{"n": 1}) + "1" + tr("artefact.oneKind", nil),
		} {
			if !strings.Contains(got, want) {
				t.Errorf("working block lacks %q:\n%s", want, got)
			}
		}

		failed := ui.artefactBlock(SessionEntry{ID: "owner/x2", Title: "Broken build"}, []chat.Event{
			{Kind: plugin.SessionJobs, Text: "make"},
			{Kind: plugin.SessionStep, Tool: "bash", Text: "bash · go build", Done: true, Failed: true, At: at},
			{Kind: plugin.SessionStep, Tool: "read", Text: "read · x", Done: true, At: at},
			{Kind: plugin.SessionStep, Tool: "bash", Text: "bash · go vet", Done: true, Failed: true, At: at},
			{Kind: plugin.SessionFailed, At: at},
		})
		got = tcText(failed)
		for _, want := range []string{"BROKEN BUILD", tr("phase.failed", nil), "bash · go vet", "make",
			tr("artefact.tools", map[string]any{"n": 3}) + "2" + tr("artefact.kinds", nil)} {
			if !strings.Contains(got, want) {
				t.Errorf("failed block lacks %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, tr("artefact.working", nil)) {
			t.Errorf("failed block works:\n%s", got)
		}
		bad := false
		for _, label := range labelsIn(failed) {
			if l, ok := label.(*widget.Label); ok && l.Text == "bash · go vet" {
				bad = l.Importance == widget.DangerImportance
			}
		}
		if !bad {
			t.Error("the failed call does not read as failed")
		}
	})
}

// The lanes of a chart are named by tool, by the start of the text, or as a
// tool; no more than six show, and calls without a time keep their order.
func TestToolChartLanes(t *testing.T) {
	if got := chartToolName(chat.Event{Text: "grep · pattern"}); got != "grep" {
		t.Errorf("lane %q", got)
	}
	if got := chartToolName(chat.Event{}); got != tr("common.tool", nil) {
		t.Errorf("lane %q", got)
	}
	start := time.Date(2026, 10, 22, 20, 0, 0, 0, time.UTC)
	var calls []chat.Event
	for i, tool := range []string{"a", "b", "c", "d", "e", "f", "g", "a"} {
		at := start.Add(time.Duration(i) * time.Minute)
		if tool == "b" {
			at = time.Time{}
		}
		calls = append(calls, chat.Event{Kind: plugin.SessionStep, Tool: tool, Done: true, At: at})
	}
	chart := toolChartOf(calls, nil, false)
	if len(chart.lanes) != chartLanes || chart.lanes[0].name != "a" || chart.lanes[0].count != 2 {
		t.Fatalf("lanes %+v", chart.lanes)
	}
	if len(chart.ticks) != 7 {
		t.Fatalf("ticks %+v", chart.ticks)
	}
	for _, tick := range chart.ticks {
		if tick.at < 0 || tick.at > 1 {
			t.Errorf("tick %+v out of the chart", tick)
		}
	}
	if clamp01(-0.5) != 0 || clamp01(1.5) != 1 || clamp01(0.25) != 0.25 {
		t.Error("clamp01")
	}
	if lanesIndex(chart.lanes, "zzz") != 0 || lanesIndex(chart.lanes, chart.lanes[2].name) != 2 {
		t.Error("lanesIndex")
	}
	want := []uint8{0xff, 0xbd, 0x8c, 0x66, 0x47, 0x2e, 0x2e}
	for i, shade := range want {
		if got := chartShade(i); got != shade {
			t.Errorf("chartShade(%d) = %#x, want %#x", i, got, shade)
		}
	}
	if itoa(0) != "0" || itoa(7) != "7" || itoa(1234) != "1234" {
		t.Error("itoa")
	}
}

// The rail draws the sessions of the part in view from what it knows of a
// settled session, keeps only the newest drawing, and draws nothing for a
// session shown on its own.
func TestFyneArtefactsRail(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	fyne.DoAndWait(func() {
		ui.inspecting = nil
		ui.section = &fyneExchange{at: time.Now(), sessions: map[string]bool{"s1": true}}
		ui.steps["owner/s1"] = &fyneTrace{list: []chat.Event{{Kind: plugin.SessionFailed, At: time.Now()}}}
		ui.artefacts()
		ui.artefacts()
	})
	waitFor(t, func() bool {
		return len(ui.contextDetails.Objects) == 1 && strings.Contains(tcText(ui.contextDetails), tr("phase.failed", nil))
	})

	// The tool result of a call without a name.
	fyne.DoAndWait(func() {
		ui.inspect("", plugin.SessionToolResult{Found: true, Arguments: `{"q":1}`})
		if got := ui.contextTitle.String(); got != strings.ToUpper("["+tr("common.tool", nil)+"]") {
			t.Errorf("rail title %q", got)
		}
		got := tcText(ui.contextDetails)
		if !strings.Contains(got, `{"q":1}`) || !strings.Contains(got, tr("tool.empty", nil)) {
			t.Errorf("rail %q", got)
		}
		ctx := ui.ctx
		ui.ctx = nil
		ui.inspect("read", plugin.SessionToolResult{Content: "nothing"})
		ui.ctx = ctx
		if strings.Contains(tcText(ui.contextDetails), "nothing") {
			t.Error("a result shows without a session")
		}
	})

	tcSession(t, ui, SessionEntry{ID: "owner/s1", Title: "Review the sidebar"})
	fyne.DoAndWait(func() {
		ui.inspecting = nil
		setChildren(ui.contextDetails)
		ui.section = &fyneExchange{at: time.Now(), sessions: map[string]bool{"s1": true}}
		ui.artefacts()
		if len(ui.contextDetails.Objects) != 0 {
			t.Error("the rail of a session on its own draws the thread")
		}
	})
}

// A card of the sidebar names a session without title by its ID, and shows
// where the focus is.
func TestFyneSidebarCard(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	fyne.DoAndWait(func() {
		card := ui.sessionItem(SessionEntry{ID: "owner/untitled"}).(*tapBox)
		if got := tcText(card); !strings.Contains(got, "OWNER/UNTITLED") {
			t.Errorf("card %q", got)
		}
		background := card.content.(*fyne.Container).Objects[0].(*paint)
		renderer := test.WidgetRenderer(background)
		card.FocusGained()
		renderer.Refresh()
		if background.rect.StrokeWidth != 2 {
			t.Errorf("focused card stroke %v", background.rect.StrokeWidth)
		}
		card.FocusLost()
		renderer.Refresh()
		if background.rect.StrokeWidth != 1 {
			t.Errorf("unfocused card stroke %v", background.rect.StrokeWidth)
		}
	})
}

// A queued message shows until the answer takes it: then it shows once, as
// the owner's. One taken back goes.
func TestFyneTranscriptQueueTaken(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	now := time.Now()
	fyne.DoAndWait(func() {
		apply := func(events ...chat.Event) { ui.applyEvents(events) }
		count := func(text string) (queued, mine int) {
			for _, o := range ui.content.Objects {
				got := tcText(o)
				if !strings.Contains(got, text) {
					continue
				}
				if strings.Contains(got, tr("speaker.queue", nil)) {
					queued++
				} else {
					mine++
				}
			}
			return queued, mine
		}
		apply(chat.Event{Kind: plugin.SessionQueued, Text: "first in line", At: now},
			chat.Event{Kind: plugin.SessionQueued, Text: "second in line", At: now})
		if q, _ := count("in line"); q != 2 {
			t.Fatalf("%d queued show", q)
		}
		apply(chat.Event{Kind: plugin.SessionUnqueued, At: now})
		if q, _ := count("second in line"); q != 0 {
			t.Fatal("the message taken back still shows")
		}
		apply(chat.Event{Kind: plugin.SessionMessage, Text: "first in line", At: now})
		if q, mine := count("first in line"); q != 0 || mine != 1 {
			t.Fatalf("taken: %d queued, %d of the owner", q, mine)
		}
	})
}

// While the model thinks the transcript shows it, with the end of what it
// thinks; once the round goes on that folds into a line that opens it. A
// While the model thinks the transcript shows an animation of its own at its
// end, not a message, with the last words of the thought; once the round
// goes on it goes, and what was thought stays as a line that opens it. A
// round without thoughts leaves nothing.
func TestFyneTranscriptThinking(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	now := time.Now()
	fyne.DoAndWait(func() {
		apply := func(events ...chat.Event) { ui.applyEvents(events) }
		talk := ui.chat
		apply(chat.Event{Kind: plugin.SessionMessage, Text: "Think it over", At: now})
		thinking := talk.thinking
		if thinking == nil || !holds(ui.content.Objects[len(ui.content.Objects)-1], thinking.pulse) {
			t.Fatal("the model thinking shows no animation at the end")
		}
		if got := tcLast(ui); got != "" {
			t.Fatalf("the animation reads as a message: %q", got)
		}
		count := len(ui.content.Objects)
		apply(chat.Event{Kind: plugin.SessionThought, Text: "First I look ", At: now},
			chat.Event{Kind: plugin.SessionThought, Text: strings.Repeat("closer ", 80) + "at the end.", At: now})
		if len(ui.content.Objects) != count {
			t.Fatal("pieces of a thought add messages")
		}
		target := string(thinking.pulse.target)
		if !strings.HasSuffix(target, "at the end.") || !strings.HasPrefix(target, "…") || len([]rune(target)) > pulseWords+2 {
			t.Fatalf("the end of the thought does not show: %q", target)
		}
		for range len(thinking.pulse.target) {
			thinking.pulse.frame(0.5)
		}
		if thinking.pulse.line.Text != target {
			t.Fatalf("the thought does not decode: %q", thinking.pulse.line.Text)
		}
		if label := thinking.pulse.label.Text; !strings.Contains(label, strings.ToUpper(tr("pulse.thinking", nil))) || !strings.Contains(label, "0:0") {
			t.Fatalf("the animation reads %q", label)
		}

		apply(chat.Event{Kind: plugin.SessionAnswer, Text: "Done.", At: now})
		if talk.thinking != nil || !thinking.pulse.stopped {
			t.Fatal("the animation stays once the answer is there")
		}
		for _, o := range ui.content.Objects {
			if holds(o, thinking.pulse) {
				t.Fatal("the animation stays in the transcript")
			}
		}
		if thinking.body.Visible() || !strings.Contains(tcText(thinking.item.item), tr("transcript.thought", nil)) {
			t.Fatalf("the thought is not kept folded: %q", tcText(thinking.item.item))
		}
		if !strings.Contains(tcLast(ui), "Done.") {
			t.Fatal("the answer does not follow the thought")
		}
		thinking.toggle.Tapped(nil)
		if !thinking.body.Visible() || !strings.HasPrefix(thinking.body.Text, "First I look") {
			t.Fatal("the folded thought does not open in full")
		}

		count = len(ui.content.Objects)
		apply(chat.Event{Kind: plugin.SessionMessage, Text: "Quick one", At: now},
			chat.Event{Kind: plugin.SessionAnswer, Text: "Sure.", At: now})
		if len(ui.content.Objects) != count+2 {
			t.Fatalf("a round without thoughts added %d messages, want 2", len(ui.content.Objects)-count)
		}
	})
}

// A question waits past the end of the answer that asked it, until its wait
// is over, as in the terminal UI: in the thread, which relays the questions
// of the sessions it handed messages to, and in a session.
func TestFyneQuestionOutlivesItsAnswer(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	now := time.Now()
	question := chat.Event{Kind: plugin.SessionAsked, Text: "Ohne Commit-Hooks?", Options: []string{"Einmal erlauben", "Nicht ausführen"}, At: now}
	// Fatal would stop the goroutine of Fyne, not the test, and DoAndWait
	// would wait for good: a failure returns instead.
	fyne.DoAndWait(func() {
		for _, id := range []string{ui.thread, "owner/s1"} {
			ui.ctx = &fyneContext{id: id, state: &fyneSessionState{}}
			ui.chat = newFyneChat(ui, 0, id, id == ui.thread)
			ui.applyEvents([]chat.Event{
				{Kind: plugin.SessionMessage, Text: "Ohne Commit Hooks!", At: now},
				question,
				{Kind: plugin.SessionAnswer, Text: "Alles klar.", At: now},
			})
			if ui.asking == nil || ui.asking.question.Text != "Ohne Commit-Hooks?" {
				t.Errorf("%s: the answer ended the question: %+v", id, ui.asking)
				return
			}
			ui.applyEvents([]chat.Event{{Kind: plugin.SessionFailed, At: now}, {Kind: plugin.SessionMessage, Text: "hm", At: now}})
			if ui.asking == nil {
				t.Errorf("%s: a failed answer or a message ended the question", id)
				return
			}
			ui.applyEvents([]chat.Event{{Kind: plugin.SessionAsked, Done: true, Text: "Einmal erlauben", At: now}})
			if ui.asking != nil {
				t.Errorf("%s: the question waits after its wait is over", id)
			}
		}
	})
}
