package tui

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"ouroboros/plugin"
)

// The memory dialog edits a memory where it shows it: ctrl+e on a star, or e
// while a memory is read in full, puts its key and what it holds into the
// fields of the dialog. What is saved replaces the memory — the store takes
// it only if the memory did not change since the dialog read it, so that an
// edit of the agent is never overwritten unseen. Tab moves from one field to
// the other, ctrl+s saves, and esc gives the edit up.

// memoryEdit is a memory the dialog edits: what was read of it, and the
// fields its key and what it holds are typed in.
type memoryEdit struct {
	e       plugin.Memory   // the memory as the dialog read it
	key     textinput.Model // its key, one line
	content textarea.Model  // what it holds
	at      int             // the field the keys go to: 0 the key, 1 what it holds
	err     error           // why the store did not take it, nil while it did not
	saving  bool            // the store is asked to take it
}

// editing opens the memory e for editing, with the cursor in what it holds:
// that is usually what an edit changes.
func editing(e plugin.Memory, light bool) (*memoryEdit, tea.Cmd) {
	key := textinput.New()
	key.Prompt = ""
	key.Placeholder = "key"
	key.CharLimit = memoryMaxKey // the store takes a key of at most 128 bytes
	key.SetValue(e.Key)
	key.SetStyles(textinput.DefaultStyles(!light))
	content := textarea.New()
	content.Prompt = ""
	content.Placeholder = "what it holds"
	content.ShowLineNumbers = false
	content.CharLimit = memoryMaxContent
	content.SetValue(e.Content)
	content.SetStyles(inputStyles(light)) // as the composer, which also holds lines of text
	return &memoryEdit{e: e, key: key, content: content, at: 1}, content.Focus()
}

// The limits the store of the memories takes: a key of 128 bytes, and what
// a memory holds of 8192. The fields take as many runes, and the save says
// when the text is longer in bytes than the store takes.
const (
	memoryMaxKey     = 128
	memoryMaxContent = 8192
)

// editKey handles the keys of the edit: tab moves between its fields, ctrl+s
// saves it, esc gives it up, and everything else is typed into the field
// that has the keys.
func (s *memoryScreen) editKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	e := s.edit
	switch msg.String() {
	case "esc":
		s.edit = nil
		return nil
	case "ctrl+s":
		return s.saveEdit(m)
	case "tab", "shift+tab":
		if e.at == 0 {
			e.at = 1
			e.key.Blur()
			return e.content.Focus()
		}
		e.at = 0
		e.content.Blur()
		return e.key.Focus()
	}
	var cmd tea.Cmd
	if e.at == 0 {
		e.key, cmd = e.key.Update(msg)
	} else {
		e.content, cmd = e.content.Update(msg)
	}
	return cmd
}

// paste puts what was pasted into the field that has the keys, in the field
// the whole text fits: the key keeps one line, as a key is.
func (s *memoryScreen) paste(text string) tea.Cmd {
	e := s.edit
	if e == nil {
		return nil
	}
	var cmd tea.Cmd
	if e.at == 0 {
		e.key, cmd = e.key.Update(tea.PasteMsg{Content: strings.SplitN(text, "\n", 2)[0]})
	} else {
		e.content, cmd = e.content.Update(tea.PasteMsg{Content: text})
	}
	return cmd
}

// memorySavedMsg is what the store says to an edit of a memory: the memory
// as it is then, or why it did not take it.
type memorySavedMsg struct {
	e   plugin.Memory
	id  string
	err error
}

// saveEdit asks the store to take what the fields hold.
func (s *memoryScreen) saveEdit(m *Model) tea.Cmd {
	e := s.edit
	if e == nil || e.saving {
		return nil
	}
	key, content := strings.TrimSpace(e.key.Value()), strings.TrimSpace(e.content.Value())
	switch {
	case key == "":
		e.err = errors.New("a memory without a key holds nowhere")
		return nil
	case content == "":
		e.err = errors.New("a memory without what it holds says nothing")
		return nil
	case len(key) > memoryMaxKey:
		e.err = fmt.Errorf("a key of %d bytes is longer than the %d a memory key may take", len(key), memoryMaxKey)
		return nil
	case len(content) > memoryMaxContent:
		e.err = fmt.Errorf("what it holds is %d bytes, longer than the %d a memory may take", len(content), memoryMaxContent)
		return nil
	}
	e.saving, e.err = true, nil
	project, id, version := e.e.Project, e.e.ID, e.e.Version
	return func() tea.Msg {
		saved, err := m.backend.SetMemory(m.ctx, project, id, version, key, content)
		return memorySavedMsg{saved, id, err}
	}
}

// memorySaved shows the memories again once one was saved, with the one
// edited selected; an edit the store did not take stays in its fields, with
// why under them, so that nothing typed is lost.
func (m *Model) memorySaved(msg memorySavedMsg) tea.Cmd {
	s := &m.memories
	if m.screen != screenMemory || s.edit == nil || s.edit.e.ID != msg.id {
		return nil
	}
	if msg.err != nil {
		s.edit.saving, s.edit.err = false, memoryEditError(msg.err)
		return nil
	}
	s.edit, s.pick = nil, msg.id
	if s.detail != nil { // edited while it was read: it is read as it is now
		*s.detail, s.detailAt = msg.e, 0
	}
	return m.loadMemories()
}

// memoryEditError says why the store did not take an edit, in words of the
// dialog: what changed since the dialog read the memory, or else what the
// store itself says.
func memoryEditError(err error) error {
	if errors.Is(err, plugin.ErrMemoryChanged) {
		return errors.New("the memory changed since it was read — esc, and read it again")
	}
	return err
}

// editingView shows the memory being edited: its key on a line, what it
// holds below it, and what went wrong with the save under them.
func (s *memoryScreen) editingView(m *Model, w, h, inner int) string {
	e := s.edit
	head := []string{boldStyle.Render("Edit memory") + mutedStyle.Render(" · "+holds(&e.e)), ""}
	keyWidth := max(1, inner-lipgloss.Width("key ")-1) // a cell is left for the cursor after the key
	e.key.SetWidth(keyWidth)
	// What it holds takes the rows the rest leaves: the title and the blank
	// line under it, the key, the blank and the rule above the text, and the
	// line of the error if there is one.
	rows := max(3, h-2-len(head)-3)
	if e.err != nil {
		rows--
	}
	e.content.SetWidth(inner)
	e.content.SetHeight(rows)
	lines := append([]string{}, head...)
	lines = append(lines, mutedStyle.Render("key ")+e.key.View(), "",
		lipgloss.NewStyle().Foreground(ui.line).Render(strings.Repeat("─", inner)))
	lines = append(lines, strings.Split(e.content.View(), "\n")...)
	if e.err != nil {
		lines = append(lines, errStyle.Render("✗ "+e.err.Error()))
	}
	return s.box(m, w, h, inner, lines)
}
