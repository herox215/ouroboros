package desktop

import (
	"context"

	"ouroboros/internal/desktop"
	"ouroboros/plugin"
)

// panelSnapshot explicitly opts into titles; Status and Waybar remain content-free.
type panelSnapshot struct {
	desktop.Status
	Sessions []string `json:"sessions"`
}

func readPanel(ctx context.Context, sessions plugin.Sessions, status desktop.Status) panelSnapshot {
	out := panelSnapshot{Status: status, Sessions: []string{}}
	if status.State == "offline" || status.State == "error" {
		return out
	}
	if source, ok := sessions.(interface {
		RunningSessions(context.Context) ([]string, error)
	}); ok {
		names, err := source.RunningSessions(ctx)
		if err != nil {
			out.State = "error"
		} else if names != nil {
			out.Sessions = names
		}
	}
	return out
}
