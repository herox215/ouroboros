//go:build cgo

package gui

import (
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"ouroboros/plugin"
)

// textLimit is how much of a long text the transcript shows before it offers
// the rest, as the window keeps very large tool output out of its layout.
const textLimit = 8000

// threadWindow is how much of the thread shows, as in the terminal UI and as
// its agent is told it: the archive keeps the rest.
const threadWindow = 6 * time.Hour

// fyneMessage is one message of the transcript, with the part of the thread
// it opens.
type fyneMessage struct {
	item     fyne.CanvasObject
	box      *fyne.Container
	exchange *fyneExchange
}

// fynePartial is an answer being written, as its pieces arrive.
type fynePartial struct {
	item *fyneMessage
	body *widget.Label
}

// fyneToolCall is one call of a tool in the transcript, with the result that
// loads when it is opened.
type fyneToolCall struct {
	tool    string
	callID  string
	item    *fyneMessage
	summary *tapText
	details *fyne.Container
	note    *widget.Label
	done    bool
	loaded  bool
	loading bool
	result  plugin.SessionToolResult
}

// fyneChat renders the events of one session, as frontend/app.js renders them
// into the transcript.
type fyneChat struct {
	ui       *fyneUI
	profile  int
	id       string
	agent    string
	thread   bool
	exchange *fyneExchange
	shown    bool
	cutoff   time.Time
	partial  *fynePartial
	calls    map[string]*fyneToolCall
}

func newFyneChat(ui *fyneUI, profile int, id string, thread bool) *fyneChat {
	agent := "ouroboros"
	if profile >= 0 && profile < len(ui.profiles) && ui.profiles[profile].Name != "" {
		agent = ui.profiles[profile].Name
	}
	return &fyneChat{
		ui: ui, profile: profile, id: id, agent: agent, thread: thread,
		cutoff: time.Now().Add(-threadWindow),
		calls:  make(map[string]*fyneToolCall),
	}
}

// apply shows one event of the session.
func (c *fyneChat) apply(event ChatEvent) {
	c.ui.onEvent(event)
	switch event.Kind {
	case plugin.SessionMessage:
		c.ui.setQuestion(nil)
		c.exchange = &fyneExchange{at: event.At}
		c.message("user", tr("speaker.you", nil), event.Text, event.At)
	case plugin.SessionSay:
		c.clearPartial()
		if strings.TrimSpace(event.Text) != "" {
			c.message("assistant", c.agent, event.Text, event.At)
		}
	case plugin.SessionAnswer, plugin.SessionSuggestion:
		if event.Kind == plugin.SessionAnswer {
			c.ui.setQuestion(nil)
		}
		c.clearPartial()
		if strings.TrimSpace(event.Text) != "" {
			c.message("assistant", c.agent, event.Text, event.At)
		}
	case plugin.SessionAnswering:
		if c.partial == nil {
			item := c.message("assistant", c.agent+" · "+tr("transcript.writing", nil), "", event.At)
			body := plain("", sizeBody, false)
			body.Wrapping = fyne.TextWrapWord
			if item.box != nil {
				item.box.Objects = append(item.box.Objects, body)
			}
			c.partial = &fynePartial{item: item, body: body}
		}
		c.partial.body.SetText(c.partial.body.Text + event.Text)
	case plugin.SessionHanded:
		if c.exchange == nil {
			c.exchange = &fyneExchange{}
		}
		if c.exchange.handed(event.Text) && c.ui.section == c.exchange {
			c.ui.renderList()
			c.ui.artefacts()
		}
	case plugin.SessionStep:
		c.clearPartial()
		c.step(event)
	case plugin.SessionAsked:
		c.clearPartial()
		c.ui.setQuestion(nil)
		label := tr("speaker.question", nil)
		if event.Done {
			label = tr("speaker.answered", nil)
		}
		body := event.Text
		if event.Question != nil && event.Question.Text != "" {
			body = event.Question.Text
		}
		c.message("question", label, body, event.At)
		if !event.Done && c.ui.owner(c.id) {
			question := &fyneQuestion{text: body, key: questionKey(event)}
			if event.Question != nil {
				question.options = append([]string(nil), event.Question.Options...)
				question.recommended = event.Question.Recommended
			}
			c.ui.setQuestion(question)
		}
	case plugin.SessionQueued:
		text := event.Text
		if text == "" {
			text = tr("queue.text", nil)
		}
		c.message("other", tr("speaker.queue", nil), text, event.At)
	case plugin.SessionFailed, plugin.SessionCanceled:
		c.ui.setQuestion(nil)
		c.clearPartial()
		text := event.Text
		if event.Kind == plugin.SessionCanceled {
			text = tr("end.stopped", nil)
		} else if text == "" {
			text = tr("end.failed", nil)
		}
		c.message("end", tr("speaker.status", nil), text, event.At)
	case plugin.SessionTask:
		c.task(event)
	case plugin.SessionThought:
		// Thoughts are not part of the transcript.
	}
}

// questionKey tells one question from another, as the window compares the
// question it sent with the one it shows.
func questionKey(event ChatEvent) string {
	question := event.Question
	text, recommended := "", ""
	var options []string
	if question != nil {
		text, recommended = question.Text, question.Recommended
		options = question.Options
	}
	return fmt.Sprintf("%s|%s|%v|%d", text, recommended, options, event.At.UnixNano())
}

// clearPartial takes the answer being written out of the transcript, once
// what it became is there.
func (c *fyneChat) clearPartial() {
	if c.partial == nil {
		return
	}
	item := c.partial.item
	c.partial = nil
	objects := c.ui.content.Objects
	for i, object := range objects {
		if object == item.item {
			c.ui.content.Objects = append(objects[:i:i], objects[i+1:]...)
			c.ui.contentChanged()
			break
		}
	}
	for i, message := range c.ui.visible {
		if message == item {
			c.ui.visible = append(c.ui.visible[:i:i], c.ui.visible[i+1:]...)
			break
		}
	}
}

// message adds a message to the transcript, once the part of the thread it is
// in shows.
func (c *fyneChat) message(kind, label, body string, at time.Time) *fyneMessage {
	if !c.shown && !at.Before(c.cutoff) {
		c.shown = true
	}
	box := container.NewVBox()
	message := &fyneMessage{box: box}
	header := label
	if stamp := clock(at, time.Now()); stamp != "" {
		header += " · " + stamp
	}
	switch kind {
	case "end":
		inner := tightVBox(-4,
			text(header, sizeSmall, theme.ColorNameDisabled, fyne.TextStyle{Monospace: true, Bold: true}),
			text(body, sizeBody, theme.ColorNameDisabled, fyne.TextStyle{Monospace: true}))
		box.Objects = append(box.Objects, inner)
		message.item = inset(8, 0, 10, 0, box)
	default:
		line := text(header, sizeSmall, theme.ColorNameDisabled, fyne.TextStyle{Monospace: true, Bold: true})
		if body != "" {
			box.Objects = append(box.Objects, tightVBox(-4, line, c.expandable(body)))
		} else {
			box.Objects = append(box.Objects, line)
		}
		message.item = inset(8, 0, 10, 0, box)
	}
	if !c.shown {
		return message
	}
	message.exchange = c.exchange
	c.ui.content.Objects = append(c.ui.content.Objects, message.item)
	c.ui.contentChanged()
	c.ui.visible = append(c.ui.visible, message)
	c.ui.latest = c.exchange
	return message
}

// expandable adds a text to the transcript, cut where it is very long until
// the owner asks for the rest.
func (c *fyneChat) expandable(value string) fyne.CanvasObject {
	return expandableText(c.ui, value)
}

// expandableText is a text that offers its rest once it is very long, as the
// window keeps very large tool output out of its layout.
func expandableText(ui *fyneUI, value string) fyne.CanvasObject {
	body := selectable(cutText(value), sizeBody)
	if len([]rune(value)) <= textLimit {
		return body
	}
	var button *tapText
	button = newTapText(tr("transcript.expandCount", map[string]any{"n": len([]rune(value))}),
		sizeSmall, fyne.TextStyle{Monospace: true}, func() {
			ui.following = false
			open := len([]rune(body.Text)) > textLimit
			if open {
				body.SetText(cutText(value))
				button.SetText(tr("transcript.expand", nil))
			} else {
				body.SetText(value)
				button.SetText(tr("transcript.collapse", nil))
			}
		})
	return container.NewVBox(body, button)
}

func cutText(value string) string {
	runes := []rune(value)
	if len(runes) <= textLimit {
		return value
	}
	return string(runes[:textLimit])
}

// step is one call of a tool: its line, and the result that loads when the
// owner opens it.
func (c *fyneChat) step(event ChatEvent) {
	key := event.CallID
	if key == "" {
		key = event.Tool
	}
	call := c.calls[key]
	if call == nil || (!event.Done && call.done) {
		item := c.message("tool", tr("speaker.tool", nil), "", event.At)
		details := container.NewVBox()
		details.Hide()
		note := muted("", sizeBody)
		summary := newTapText("", sizeBody, fyne.TextStyle{Monospace: true}, nil)
		call = &fyneToolCall{tool: event.Tool, callID: event.CallID, item: item,
			summary: summary, details: details, note: note}
		summary.onTap = func() { c.toggle(call) }
		if item.box != nil {
			edge := newPaint(func(r *canvas.Rectangle, p *palette) { r.FillColor = p.line })
			item.box.Objects = append(item.box.Objects, container.NewBorder(nil, nil, container.New(&fixedWidthLayout{width: 1}, edge), nil,
				container.NewVBox(inset(10, 14, 10, 14, summary), details)))
		}
		c.calls[key] = call
	}
	call.done = event.Done
	text := event.Text
	if text == "" {
		text = event.Tool
	}
	if text == "" {
		text = tr("speaker.tool", nil)
	}
	if event.CallID != "" {
		text += tr("tool.expandHint", nil)
	}
	call.summary.SetText(text)
}

// toggle opens a tool call: the first time it reads the result from the
// archive, and each time it shows what was read in the rail beside the
// thread.
func (c *fyneChat) toggle(call *fyneToolCall) {
	c.ui.following = false
	if call.details.Visible() {
		call.details.Hide()
		return
	}
	call.details.Show()
	if call.loaded {
		c.ui.inspect(call.tool, call.result)
		return
	}
	if call.loading || call.callID == "" {
		return
	}
	call.loading = true
	call.note.SetText(tr("tool.loading", nil))
	call.details.Objects = []fyne.CanvasObject{widget.NewSeparator(), inset(10, 14, 10, 14, call.note)}
	call.details.Refresh()
	epoch := c.ui.epoch.Load()
	profile, id := c.profile, c.id
	go func() {
		result, err := c.ui.bridge.ToolResult(profile, id, call.callID)
		fyne.Do(func() {
			if c.ui.epoch.Load() != epoch {
				return
			}
			call.loading = false
			if err != nil {
				call.note.SetText(tr("tool.failed", map[string]any{"error": err}))
				return
			}
			if !result.Found {
				call.note.SetText(tr("tool.missing", nil))
				return
			}
			objects := make([]fyne.CanvasObject, 0, 2)
			if result.Arguments != "" {
				objects = append(objects, c.expandable(result.Arguments))
			}
			content := result.Content
			if content == "" {
				content = tr("tool.empty", nil)
			}
			objects = append(objects, c.expandable(content))
			call.details.Objects = []fyne.CanvasObject{widget.NewSeparator(), inset(10, 14, 10, 14, container.NewVBox(objects...))}
			call.details.Refresh()
			call.loaded = true
			call.result = result
			c.ui.inspect(call.tool, result)
			if c.ui.following {
				c.ui.stick()
			}
		})
	}()
}

// task is what the agent handed to a session, a line of its own.
func (c *fyneChat) task(event ChatEvent) {
	if !c.shown && !event.At.Before(c.cutoff) {
		c.shown = true
	}
	header := tr("speaker.task", nil)
	if stamp := clock(event.At, time.Now()); stamp != "" {
		header += " · " + stamp
	}
	box := container.NewVBox(text(header+"  "+event.Text, sizeMeta, theme.ColorNameDisabled,
		fyne.TextStyle{Monospace: true}))
	item := inset(2, 0, 8, 0, box)
	if !c.shown {
		return
	}
	message := &fyneMessage{item: item, box: box, exchange: c.exchange}
	c.ui.content.Objects = append(c.ui.content.Objects, item)
	c.ui.contentChanged()
	c.ui.visible = append(c.ui.visible, message)
	c.ui.latest = c.exchange
}

// applyEvents shows a batch of events of the session in view. The
// transcript and the composer are laid out once for the batch, not once for
// each event, so that a long history opens without stalling the window.
func (ui *fyneUI) applyEvents(events []ChatEvent) {
	if ui.chat == nil {
		return
	}
	ui.applying = true
	for _, event := range events {
		ui.chat.apply(event)
	}
	ui.applying = false
	if ui.contentDirty {
		ui.contentDirty = false
		ui.content.Refresh()
	}
	if ui.composerDirty {
		ui.composerDirty = false
		ui.updateComposer()
	}
}

// contentChanged lays the transcript out anew, or once at the end of the
// batch of events being applied.
func (ui *fyneUI) contentChanged() {
	if ui.applying {
		ui.contentDirty = true
		return
	}
	ui.content.Refresh()
}

// composerChanged updates the composer, or once at the end of the batch of
// events being applied.
func (ui *fyneUI) composerChanged() {
	if ui.applying {
		ui.composerDirty = true
		return
	}
	ui.updateComposer()
}

// onEvent keeps the state of the window: how much works, what a session of
// its own did, and where the header stands.
func (ui *fyneUI) onEvent(event ChatEvent) {
	if ui.ready.Load() {
		ui.heard = true
	}
	switch event.Kind {
	case plugin.SessionWorking:
		ui.activity.n = event.N
		ui.activity.own = event.Own
		ui.activity.busy = event.N > 0
	case plugin.SessionMessage, plugin.SessionTask:
		ui.activity.busy = true
		ui.stand(event)
	case plugin.SessionAnswering:
		ui.activity.busy = true
		if len(ui.own) == 0 || ui.own[len(ui.own)-1].Kind != plugin.SessionAnswering {
			ui.stand(event)
		}
	case plugin.SessionThought, plugin.SessionCalling:
		ui.activity.busy = true
	case plugin.SessionAsked:
		if !event.Done {
			ui.activity.busy = true
		}
		ui.stand(event)
	case plugin.SessionAnswer, plugin.SessionFailed, plugin.SessionCanceled:
		if ui.ctx != nil && ui.ctx.id == ui.thread {
			active := ui.activity.n
			if ui.activity.own {
				active--
			}
			ui.activity.busy = active > 0
		} else {
			ui.activity.busy = false
		}
		ui.stand(event)
	case sessionDirectory:
		text := event.Text
		if text == "" {
			text = tr("session.workspace", nil)
		}
		ui.meta.SetText(text)
	case plugin.SessionStep:
		ui.stand(event)
	case plugin.SessionJobs, plugin.SessionSay:
		ui.stand(event)
	case plugin.SessionTitle:
		if ui.ctx != nil && ui.ctx.id != ui.thread && event.Text != "" {
			for i := range ui.entries {
				if ui.entries[i].ID == ui.ctx.id {
					ui.entries[i].Title = event.Text
				}
			}
			setTitleText(ui.title, strings.ToUpper(event.Text))
			ui.renderList()
		}
	}
	ui.composerChanged()
}

// stand adds an event to what the session shown on its own did, for the
// artefacts beside it.
func (ui *fyneUI) stand(event ChatEvent) {
	if ui.ctx == nil || ui.ctx.id == ui.thread {
		return
	}
	ui.own = append(ui.own, event)
	ui.ownArtefacts()
}
