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
	// An access that cannot report, reports nothing or whose provider tells
	// ouro nothing says so, and the ones after it are reported all the same.
	reports := []plugin.AccessUsage{
		{Access: plugin.SetupAccess{Label: "Other Go"}, Err: errors.New("HTTP 401").Error()},
		{Access: plugin.SetupAccess{Label: "Quiet Go"}},
		{Access: plugin.SetupAccess{Label: "ChatGPT"}, Unreported: true},
		{Access: plugin.SetupAccess{Label: "OpenCode Go", Active: true}, Usage: plugin.Usage{Windows: []plugin.UsageWindow{
			{Name: "rolling", UsedPercent: 12, ResetsAt: reset},
			{Name: "weekly", UsedPercent: 34, ResetsAt: reset},
			{Name: "monthly", UsedPercent: 105, ResetsAt: reset},
		}}},
		{Access: plugin.SetupAccess{Label: "DeepSeek"}, Usage: plugin.Usage{Balances: []plugin.UsageBalance{
			{Currency: "USD", Amount: "12.34"}, {Currency: "CNY", Amount: "0.00"},
		}}},
		{Access: plugin.SetupAccess{Label: "DeepSeek spent"}, Usage: plugin.Usage{Insufficient: true}},
	}
	text := usageText(reports)
	at := reset.Local().Format("02 Jan 15:04 MST")
	want := strings.Join([]string{
		"Other Go",
		"  Unavailable: HTTP 401",
		"",
		"Quiet Go",
		"  No quota or balance reported.",
		"",
		"ChatGPT",
		"  Not reported: ouro reads no quota or balance from this provider.",
		"",
		"OpenCode Go · in use",
		"  5 hours: ~88.0% remaining · resets " + at,
		"  weekly: ~66.0% remaining · resets " + at,
		"  monthly: ~0.0% remaining · resets " + at,
		"",
		"DeepSeek",
		"  balance: 12.34 USD",
		"  balance: 0.00 CNY",
		"",
		"DeepSeek spent",
		"  Not enough balance for API calls.",
	}, "\n")
	if text != want {
		t.Errorf("got\n%s\nwant\n%s", text, want)
	}
	if strings.Contains(text, "http://") || strings.Contains(text, "https://") {
		t.Fatal("quota screen must not offer links")
	}
	if text := usageText(nil); text != "No access is set up." {
		t.Fatal(text)
	}
}
