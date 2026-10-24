package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestComposerClickScrolledNumberedLines(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line%02d", i+1)
	}
	m.Update(tea.PasteMsg{Content: strings.Join(lines, "\n")})
	m.input, _ = m.input.Update(nil)
	top := m.input.ScrollYOffset()
	if top == 0 {
		t.Fatal("fixture must scroll the composer")
	}
	clickOn(t, m, "line19", 0)
	if m.input.Line() != 18 || m.input.Column() != 0 {
		t.Fatalf("clicked line19: cursor = (%d, %d)", m.input.Line(), m.input.Column())
	}
	if got := m.input.ScrollYOffset(); got != top {
		t.Fatalf("click scrolled the composer from %d to %d", top, got)
	}
}

func TestComposerClickDisplayCells(t *testing.T) {
	for _, tt := range []struct {
		name, text   string
		cell, column int
	}{
		{"ascii after wide", "你好abc", 4, 2},
		{"inside wide", "你好abc", 3, 1},
		{"after combining", "e\u0301abc", 1, 2},
		{"past end", "你好abc", 20, 5},
		{"negative", "你好abc", -1, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newModel(t)
			m.input.SetValue(tt.text)
			m.clickComposer(0, tt.cell)
			if got := m.input.Column(); got != tt.column {
				t.Fatalf("cell %d: rune column = %d, want %d", tt.cell, got, tt.column)
			}
		})
	}
}

func TestComposerClickWideTextThroughMouse(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.PasteMsg{Content: "你好abc"})
	clickOn(t, m, "abc", 0)
	if got := m.input.Column(); got != 2 {
		t.Fatalf("click on a: rune column = %d, want 2", got)
	}
}

func TestComposerClickWordWrappedCells(t *testing.T) {
	for _, tt := range []struct {
		name, text        string
		row, cell, column int
	}{
		{"word wrap", "one two three", 1, 2, 10},
		{"wide on wrapped row", "one two 你好abc", 1, 4, 10},
		{"blank before wrap", "one two three", 0, 30, 7},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newModel(t)
			m.input.SetWidth(8)
			m.input.SetHeight(4)
			m.input.SetValue(tt.text)
			m.input, _ = m.input.Update(nil)
			m.clickComposer(tt.row, tt.cell)
			if got := m.input.Column(); got != tt.column {
				t.Fatalf("row %d, cell %d: rune column = %d, want %d", tt.row, tt.cell, got, tt.column)
			}
		})
	}
}

func TestComposerClickScrolledWrap(t *testing.T) {
	m, _ := newModel(t)
	m.input.SetWidth(12)
	m.input.SetHeight(3)
	m.input.SetValue("one two three four five six seven eight nine ten")
	m.input, _ = m.input.Update(nil)
	m.input.MoveToEnd()
	top := m.input.ScrollYOffset()
	if top == 0 || m.input.LineInfo().Height <= 3 {
		t.Fatal("fixture must scroll within a wrapped logical line")
	}
	// Identify each visible segment using the textarea's wrapping API.
	probe, _ := m.input.Update(nil)
	probe.MoveToBegin()
	for range top {
		probe.CursorDown()
		probe.SetCursorColumn(probe.LineInfo().StartColumn)
	}
	for row := range 3 {
		info := probe.LineInfo()
		m.clickComposer(row, 2)
		if m.input.Line() != 0 || m.input.Column() != info.StartColumn+2 {
			t.Fatalf("visible row %d: column = %d, want %d", row, m.input.Column(), info.StartColumn+2)
		}
		if got := m.input.ScrollYOffset(); got != top {
			t.Fatalf("visible row %d: viewport = %d, want %d", row, got, top)
		}
		probe.CursorDown()
		probe.SetCursorColumn(probe.LineInfo().StartColumn)
	}
}
