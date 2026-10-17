package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ouroboros/plugin"
)

func TestUsageText(t *testing.T) {
	reset := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	// An access that cannot report, or reports nothing, says so, and the
	// ones after it are reported all the same.
	reports := []plugin.AccessUsage{
		{Access: plugin.SetupAccess{Label: "Other Go"}, Err: errors.New("HTTP 401").Error()},
		{Access: plugin.SetupAccess{Label: "Quiet Go"}},
		{Access: plugin.SetupAccess{Label: "OpenCode Go", Active: true}, Windows: []plugin.UsageWindow{
			{Name: "rolling", UsedPercent: 12, ResetsAt: reset},
			{Name: "weekly", UsedPercent: 34, ResetsAt: reset},
			{Name: "monthly", UsedPercent: 105, ResetsAt: reset},
		}},
	}
	text := usageText(reports)
	at := reset.Local().Format("02 Jan 15:04 MST")
	want := strings.Join([]string{
		"Other Go",
		"  Unavailable: HTTP 401",
		"",
		"Quiet Go",
		"  No quota windows reported.",
		"",
		"OpenCode Go · in use",
		"  5 hours: ~88.0% remaining · resets " + at,
		"  weekly: ~66.0% remaining · resets " + at,
		"  monthly: ~0.0% remaining · resets " + at,
	}, "\n")
	if text != want {
		t.Errorf("got\n%s\nwant\n%s", text, want)
	}
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		t.Fatal("quota screen must not offer links")
	}
	if text := usageText(nil); !strings.Contains(text, "No configured provider") {
		t.Fatal(text)
	}
}
