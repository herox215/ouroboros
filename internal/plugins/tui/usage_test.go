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
	reports := []plugin.AccessUsage{
		{Access: plugin.SetupAccess{Label: "OpenCode Go", Active: true}, Windows: []plugin.UsageWindow{
			{Name: "rolling", UsedPercent: 12, ResetsAt: reset},
			{Name: "weekly", UsedPercent: 34, ResetsAt: reset},
			{Name: "monthly", UsedPercent: 105, ResetsAt: reset},
		}},
		{Access: plugin.SetupAccess{Label: "Other Go"}, Err: errors.New("HTTP 401").Error()},
	}
	text := usageText(reports)
	for _, want := range []string{"OpenCode Go · in use", "5 hours: ~88.0% remaining", "weekly: ~66.0% remaining", "monthly: ~0.0% remaining", reset.Local().Format("02 Jan 15:04 MST"), "Other Go", "Unavailable: HTTP 401"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		t.Fatal("quota screen must not offer links")
	}
	if text := usageText(nil); !strings.Contains(text, "No configured provider") {
		t.Fatal(text)
	}
}
