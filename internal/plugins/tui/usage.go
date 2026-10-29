package tui

import (
	"context"
	"fmt"
	"strings"

	"ouroboros/plugin"
)

// usageScreen shows what the APIs of the providers tell of the accesses:
// quotas and balances, never an estimate. The usual plugin-screen refresh
// asks again while it is open, without spending a model request.
func (u *ui) usageScreen() plugin.Screen {
	return plugin.Screen{Command: "usage", Title: "Remaining usage", Body: func(ctx context.Context) (string, error) {
		reports, err := u.Owner.Usage(ctx, u.providers())
		if err != nil {
			return "", err
		}
		return usageText(reports), nil
	}}
}

// usageText lists every access of the node with what its provider told of
// it, or why it told nothing.
func usageText(reports []plugin.AccessUsage) string {
	if len(reports) == 0 {
		return "No access is set up."
	}
	var lines []string
	for _, r := range reports {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		label := r.Access.Label
		if r.Access.Active {
			label += " · in use"
		}
		lines = append(append(lines, label), reported(r)...)
	}
	return strings.Join(lines, "\n")
}

// reported are the lines under the label of the access of r.
func reported(r plugin.AccessUsage) []string {
	switch {
	case r.Err != "":
		return []string{"  Unavailable: " + r.Err}
	case r.Unreported:
		return []string{"  Not reported: ouro reads no quota or balance from this provider."}
	case len(r.Windows) == 0 && len(r.Balances) == 0 && !r.Insufficient:
		return []string{"  No quota or balance reported."}
	}
	var lines []string
	for _, w := range r.Windows {
		remaining := max(0.0, min(100.0, 100-w.UsedPercent))
		name := w.Name
		if name == "rolling" {
			name = "5 hours"
		}
		lines = append(lines, fmt.Sprintf("  %s: ~%.1f%% remaining · resets %s", name, remaining, w.ResetsAt.Local().Format("02 Jan 15:04 MST")))
	}
	for _, b := range r.Balances {
		lines = append(lines, fmt.Sprintf("  balance: %s %s", b.Amount, b.Currency))
	}
	if r.Insufficient {
		lines = append(lines, "  Not enough balance for API calls.")
	}
	return lines
}
