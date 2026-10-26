//go:build cgo

package gui

import (
	"cmp"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"ouroboros/internal/chat"
	"ouroboros/plugin"
)

// fynePhase is where a session stands, from what its events tell: the phase
// of the loop to its answer, the call it is in, the one it did last, and what
// comes next.
type fynePhase struct {
	phase    string
	since    time.Time
	call     *chat.Event
	last     *chat.Event
	question string
	jobs     string
	live     bool
}

// sessionPhaseOf reads the trace of a session as the window reads it.
func sessionPhaseOf(trace []chat.Event, running bool) fynePhase {
	state := fynePhase{phase: "empty"}
	for i := range trace {
		event := trace[i]
		switch event.Kind {
		case plugin.SessionStep:
			if !event.At.IsZero() {
				state.since = event.At
			}
			if event.Done {
				state.last = &trace[i]
				state.call = nil
				if state.phase != "asked" {
					state.phase = "thinking"
				}
			} else {
				state.call = &trace[i]
				state.phase = "tool"
			}
		case plugin.SessionAsked:
			if !event.At.IsZero() {
				state.since = event.At
			}
			if event.Done {
				state.question = ""
				if state.phase == "asked" {
					state.phase = "thinking"
				}
			} else {
				state.phase = "asked"
				state.question = event.Text
			}
		case plugin.SessionAnswering:
			if !event.At.IsZero() {
				state.since = event.At
			}
			if state.phase != "asked" {
				state.phase = "answering"
			}
		case plugin.SessionJobs:
			state.jobs = strings.TrimSpace(event.Text)
		case plugin.SessionMessage, plugin.SessionSay, plugin.SessionTask:
			if !event.At.IsZero() {
				state.since = event.At
			}
			if state.phase != "asked" {
				state.phase = "thinking"
			}
		case plugin.SessionAnswer:
			state.phase = "done"
			state.since = event.At
			state.call = nil
			state.question = ""
		case plugin.SessionFailed:
			state.phase = "failed"
			state.since = event.At
			state.call = nil
		case plugin.SessionCanceled:
			state.phase = "canceled"
			state.since = event.At
			state.call = nil
		}
	}
	settled := state.phase == "done" || state.phase == "failed" || state.phase == "canceled" || state.phase == "empty"
	state.live = !settled && running
	return state
}

// word is what the state line says of the phase, as the window words it.
func (p fynePhase) word() string {
	switch p.phase {
	case "asked":
		return tr("phase.asked", nil)
	case "tool":
		if p.live {
			return tr("phase.calling", nil)
		}
		return tr("phase.ready", nil)
	case "thinking":
		if p.live {
			return tr("phase.thinking", nil)
		}
		return tr("phase.ready", nil)
	case "answering":
		if p.live {
			return tr("phase.answering", nil)
		}
		return tr("phase.ready", nil)
	case "done":
		return tr("phase.done", nil)
	case "failed":
		return tr("phase.failed", nil)
	case "canceled":
		return tr("phase.canceled", nil)
	}
	return tr("phase.empty", nil)
}

// what is what the call in the phase does with its tool.
func (p fynePhase) what() string {
	if p.phase != "tool" || !p.live || p.call == nil {
		return ""
	}
	tool := p.call.Tool
	if tool == "" {
		tool = tr("common.tool", nil)
	}
	text := p.call.Text
	switch {
	case strings.HasPrefix(text, tool+" · "):
		return text[len(tool)+len(" · "):]
	case text == tool:
		return ""
	}
	return text
}

// next is what the session does after the phase.
func (p fynePhase) next() string {
	tool := tr("common.tool", nil)
	if p.call != nil && p.call.Tool != "" {
		tool = p.call.Tool
	}
	switch {
	case p.phase == "asked":
		return tr("phase.nextAnswer", nil)
	case p.phase == "tool" && p.live:
		return tr("phase.nextResult", map[string]any{"tool": tool})
	case p.phase == "answering" && p.live:
		return tr("phase.nextEnd", nil)
	case p.phase == "thinking" && p.live:
		return tr("phase.nextTool", nil)
	}
	return tr("phase.nextNothing", nil)
}

// artefactBlock is one session beside the thread: where it stands, what it
// did last and what it does next, with the chart of its tools under it.
func (ui *fyneUI) artefactBlock(entry SessionEntry, trace []chat.Event) fyne.CanvasObject {
	state := sessionPhaseOf(trace, entry.Running)
	box := tightVBox(-6)
	title := entry.Title
	if title == "" {
		title = entry.ID
	}
	if state.live {
		title += tr("artefact.working", nil)
	}
	box.Objects = append(box.Objects, text(strings.ToUpper(title), sizeMeta, theme.ColorNameForeground,
		fyne.TextStyle{Monospace: true, Bold: true}))

	wordColor := theme.ColorNameForeground
	if state.phase == "failed" {
		wordColor = theme.ColorNameError
	}
	word := text(state.word(), sizeMeta, wordColor, fyne.TextStyle{Monospace: true, Bold: true})
	lineLeft := container.NewHBox(dot(state.live, state.phase == "failed"), word)
	var right fyne.CanvasObject
	if !state.since.IsZero() {
		right = muted(clock(state.since, time.Now()), sizeSmall)
	}
	what := state.what()
	var line fyne.CanvasObject
	if what != "" {
		line = container.NewBorder(nil, nil, lineLeft, right, ellipsis(what, sizeMeta))
	} else {
		line = container.NewBorder(nil, nil, lineLeft, right)
	}
	box.Objects = append(box.Objects, line)

	fact := func(key, value string, at time.Time, bad bool) {
		box.Objects = append(box.Objects, tableRow(key, value, at, bad))
	}
	if state.question != "" {
		fact(tr("artefact.question", nil), state.question, time.Time{}, false)
	}
	if state.last != nil {
		// A call nothing is shown of names its tool, as the transcript does.
		fact(tr("artefact.last", nil), cmp.Or(state.last.Text, state.last.Tool), state.last.At, state.last.Failed)
	} else {
		fact(tr("artefact.last", nil), tr("artefact.noCall", nil), time.Time{}, false)
	}
	fact(tr("artefact.next", nil), state.next(), time.Time{}, false)
	if state.jobs != "" {
		jobs := strings.Split(state.jobs, "\n")[0]
		if strings.Contains(state.jobs, "\n") {
			jobs += " …"
		}
		fact(tr("artefact.running", nil), jobs, time.Time{}, false)
	}

	calls := make([]chat.Event, 0, len(trace))
	for _, event := range trace {
		if event.Kind == plugin.SessionStep && event.Done {
			calls = append(calls, event)
		}
	}
	chart := toolChartOf(calls, state.call, state.live)
	if len(chart.lanes) > 0 {
		box.Objects = append(box.Objects, chartView(chart))
		count := len(calls)
		if state.call != nil {
			count++
		}
		key := "artefact.tools"
		if count == 1 {
			key = "artefact.oneTool"
		}
		kinds := len(chart.lanes)
		summary := tr(key, map[string]any{"n": count}) + itoa(kinds)
		if kinds == 1 {
			summary += tr("artefact.oneKind", nil)
		} else {
			summary += tr("artefact.kinds", nil)
		}
		box.Objects = append(box.Objects, muted(summary, sizeSmall))
	}
	return box
}

// chartToolName is the lane a call belongs to.
func chartToolName(event chat.Event) string {
	if event.Tool != "" {
		return event.Tool
	}
	if first := strings.Split(event.Text, " · ")[0]; first != "" {
		return first
	}
	return tr("common.tool", nil)
}

// toolChartOf lays the calls of a session on their lanes: by their time over the session, or by their order without
// stamps.
func toolChartOf(calls []chat.Event, pending *chat.Event, running bool) toolChart {
	all := append([]chat.Event(nil), calls...)
	if pending != nil {
		all = append(all, *pending)
	}
	var order []string
	tally := make(map[string]int)
	for _, step := range all {
		name := chartToolName(step)
		if _, ok := tally[name]; !ok {
			order = append(order, name)
		}
		tally[name]++
	}
	lanes := make([]chartLane, 0, len(tally))
	for _, name := range order {
		lanes = append(lanes, chartLane{name: name, count: tally[name]})
	}
	sort.SliceStable(lanes, func(i, j int) bool { return lanes[i].count > lanes[j].count })
	if len(lanes) > chartLanes {
		lanes = lanes[:chartLanes]
	}
	lane := make(map[string]int, len(lanes))
	for i, entry := range lanes {
		lane[entry.name] = i
	}
	first, last := time.Time{}, time.Time{}
	has := false
	for _, step := range all {
		if step.At.IsZero() {
			continue
		}
		if !has || step.At.Before(first) {
			first = step.At
		}
		if !has || step.At.After(last) {
			last = step.At
		}
		has = true
	}
	if running && time.Now().After(last) {
		last = time.Now()
	}
	span := last.Sub(first)
	at := func(step chat.Event, index int) float64 {
		if has && span > 0 && !step.At.IsZero() {
			offset := float64(step.At.Sub(first)) / float64(span)
			return clamp01(offset)
		}
		return clamp01(float64(index) / float64(max(1, len(all))))
	}
	ticks := make([]chartTick, 0, len(all))
	for i, step := range calls {
		if index, ok := lane[chartToolName(step)]; ok {
			ticks = append(ticks, chartTick{lane: index, at: at(step, i)})
		}
	}
	if pending != nil {
		if index, ok := lane[chartToolName(*pending)]; ok {
			ticks = append(ticks, chartTick{lane: index, at: at(*pending, len(all)-1)})
		}
	}
	return toolChart{lanes: lanes, ticks: ticks, running: running}
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// artefacts draws the sessions of the part of the thread in view in the rail,
// each read from its own trace.
func (ui *fyneUI) artefacts() {
	if ui.section == nil || ui.thread == "" || ui.ctx == nil || ui.ctx.id != ui.thread || ui.inspecting != nil {
		return
	}
	ui.drawing++
	draw := ui.drawing
	profile := ui.ctx.profile
	section := ui.section
	shown := make([]SessionEntry, 0, len(ui.entries))
	for _, entry := range ui.entries {
		if ui.inSection(entry) {
			shown = append(shown, entry)
		}
	}
	setContextTitle(ui, tr("context.stand", nil))
	if len(shown) > 0 {
		ui.contextNote.SetText("T – " + clock(section.at, time.Now()))
	} else {
		ui.contextNote.SetText(tr("common.none", nil))
	}
	known := make(map[string]*fyneTrace, len(shown))
	for _, entry := range shown {
		known[entry.ID] = ui.steps[entry.ID]
	}
	type traceResult struct {
		entry SessionEntry
		list  []chat.Event
	}
	go func() {
		results := make([]traceResult, 0, len(shown))
		for _, entry := range shown {
			kept := known[entry.ID]
			list := []chat.Event{}
			if kept != nil {
				list = kept.list
			}
			if kept == nil || entry.Running || kept.running {
				if fresh, err := ui.bridge.SessionTrace(profile, entry.ID); err == nil {
					list = fresh
				}
			}
			results = append(results, traceResult{entry: entry, list: list})
		}
		fyne.Do(func() {
			if ui.drawing != draw || ui.ctx == nil || ui.ctx.id != ui.thread || ui.ctx.profile != profile || ui.section != section {
				return
			}
			blocks := make([]fyne.CanvasObject, 0, len(results))
			for _, result := range results {
				ui.steps[result.entry.ID] = &fyneTrace{list: result.list, running: result.entry.Running}
				blocks = append(blocks, ui.artefactBlock(result.entry, result.list))
			}
			ui.setContextDetails(blocks...)
		})
	}()
}

// ownArtefacts draws the session shown on its own from the events it told
// while it was open.
func (ui *fyneUI) ownArtefacts() {
	if ui.ctx == nil || ui.ctx.id == ui.thread || ui.inspecting != nil || len(ui.own) == 0 {
		return
	}
	listed := SessionEntry{ID: ui.ctx.id}
	for _, entry := range ui.entries {
		if entry.ID == ui.ctx.id {
			listed = entry
		}
	}
	listed.Running = listed.Running || (ui.heard && ui.ctx.session.Pending)
	setContextTitle(ui, tr("context.stand", nil))
	ui.contextNote.SetText("T – " + clock(ui.own[len(ui.own)-1].At, time.Now()))
	ui.setContextDetails(ui.artefactBlock(listed, ui.own))
}

// inspect shows a tool result in the rail, as the transcript asks for it.
func (ui *fyneUI) inspect(tool string, result plugin.SessionToolResult) {
	if ui.ctx == nil {
		return
	}
	if tool == "" {
		tool = tr("common.tool", nil)
	}
	ui.inspecting = &fyneInspect{profile: ui.ctx.profile, id: ui.ctx.id}
	setContextTitle(ui, "["+tool+"]")
	ui.contextNote.SetText(tr("context.toolResult", map[string]any{"id": cmp.Or(ui.shownTitle, ui.ctx.id)}))
	objects := make([]fyne.CanvasObject, 0, 2)
	if result.Arguments != "" {
		objects = append(objects, expandableText(ui, result.Arguments))
	}
	content := result.Content
	if content == "" {
		content = tr("tool.empty", nil)
	}
	objects = append(objects, expandableText(ui, content))
	ui.setContextDetails(objects...)
}

// setContextTitle writes the heading of the rail, in capitals as the
// stylesheet sets it.
func setContextTitle(ui *fyneUI, value string) {
	setRich(ui.contextTitle, strings.ToUpper(value), sizeSection, theme.ColorNameForeground)
}

// tableRow is a fact of the rail: its name, what it says and when.
func tableRow(key, value string, at time.Time, bad bool) fyne.CanvasObject {
	valueLine := ellipsis(value, sizeSmall)
	if bad {
		valueLine.Importance = widget.DangerImportance
	}
	var right fyne.CanvasObject
	if !at.IsZero() {
		right = inset(6, 0, 0, 0, muted(clock(at, time.Now()), sizeSmall))
	}
	return container.NewBorder(nil, nil, fixedWidth(46, muted(key, sizeSmall)), right, valueLine)
}

// setContextDetails updates both the rail contents and the space it occupies.
func (ui *fyneUI) setContextDetails(objects ...fyne.CanvasObject) {
	setChildren(ui.contextDetails, objects...)
	if ui.columns != nil {
		ui.columns.Refresh()
	}
}
