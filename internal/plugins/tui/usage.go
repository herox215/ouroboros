package tui

import (
	"context"
	"fmt"
	"strings"

	"ouroboros/plugin"
)

// usageScreen shows server-side quotas only. The usual plugin-screen refresh
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

func usageText(reports []plugin.AccessUsage) string {
	if len(reports) == 0 {
		return "No configured provider exposes remaining quota through its API."
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
		lines = append(lines, label)
		if r.Err != "" {
			lines = append(lines, "  Unavailable: "+r.Err)
			continue
		}
		if len(r.Windows) == 0 {
			lines = append(lines, "  No quota windows reported.")
			continue
		}
		for _, w := range r.Windows {
			remaining := max(0.0, min(100.0, 100-w.UsedPercent))
			name := w.Name
			if name == "rolling" {
				name = "5 hours"
			}
			lines = append(lines, fmt.Sprintf("  %s: ~%.1f%% remaining · resets %s", name, remaining, w.ResetsAt.Local().Format("02 Jan 15:04 MST")))
		}
	}
	return strings.Join(lines, "\n")
}
