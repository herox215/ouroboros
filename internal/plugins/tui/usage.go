package tui

import (
	"context"

	"ouroboros/plugin"
)

// usageScreen shows what the APIs of the providers tell of the accesses the
// sessions and the thread are answered through now: quotas and balances,
// never an estimate. The usual plugin-screen refresh asks again while it is
// open, without spending a model request.
func (u *ui) usageScreen() plugin.Screen {
	return plugin.Screen{Command: "usage", Title: "Remaining usage", Body: func(ctx context.Context) (string, error) {
		reports, err := u.Owner.CurrentUsage(ctx, u.providers())
		if err != nil {
			return "", err
		}
		return plugin.UsageText(reports), nil
	}}
}

// usageText is plugin.UsageText, kept for the tests of this plugin.
func usageText(reports []plugin.AccessUsage) string { return plugin.UsageText(reports) }
