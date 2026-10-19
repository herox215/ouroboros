package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSettingsCategoryInfo(t *testing.T) {
	m, b := openSettings(t, someSettings())
	press(m, "right", "enter")
	if m.config.info == nil || !strings.Contains(dialogView(m), "The access and the models") {
		t.Fatalf("category Info missing:\n%s", dialogView(m))
	}
	press(m, "esc")
	if m.config.opened || m.config.info != nil || m.config.category != 0 || m.choice != 1 || len(b.changes) != 0 {
		t.Fatal("Info changed the category or configuration")
	}
	press(m, "left", "enter")
	if !m.config.opened {
		t.Fatal("open no longer opens the category")
	}
}

func TestSettingsInfoScroll(t *testing.T) {
	s := someSettings()
	s.Tools[0].Tools[0].Description = strings.Repeat("a long explanation ", 100) + "THE END"
	m, _ := openSettings(t, s)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 16})
	pick(t, m, ownerSection, "read", "Info")
	dialogView(m)
	top := m.config.top
	do(m)
	if strings.Contains(dialogView(m), "THE END") {
		t.Fatal("long Info was not scrolled")
	}
	for range 200 {
		press(m, "down")
		dialogView(m)
	}
	if !strings.Contains(dialogView(m), "THE END") {
		t.Fatal("cannot reach the end of Info")
	}
	if got := footer(m); got != "↑↓ scroll · enter / esc back" {
		t.Errorf("the footer with Info: %q", got)
	}
	// Up goes back to the beginning, and stops there.
	for range 200 {
		press(m, "up")
		dialogView(m)
	}
	if view := dialogView(m); strings.Contains(view, "THE END") || m.config.infoTop != 0 {
		t.Fatalf("up did not go back to the beginning: %d\n%s", m.config.infoTop, view)
	}
	press(m, "j", "j", "k")
	if m.config.infoTop != 1 {
		t.Errorf("j j k: %d", m.config.infoTop)
	}
	press(m, "esc")
	if m.config.top != top || m.config.selectedRow(m).label != "read" {
		t.Fatal("Info moved the underlying list")
	}
}
