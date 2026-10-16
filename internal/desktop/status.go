// Package desktop provides a privacy-safe status model for desktop adapters.
// It knows neither Waybar nor any operating system's tray API.
package desktop

import (
	"context"
	"errors"
	"fmt"

	"ouroboros/plugin"
)

// Source reads metadata only; polling must never start the node or load chats.
type Source = plugin.ActivitySource

// Status is versioned so external desktop adapters can consume it independently.
// CLI snapshots contain no conversation titles, messages, paths or error details.
// Sessions is an in-process opt-in for the local tray menu, never JSON.
type Status struct {
	Version  int      `json:"version"`
	State    string   `json:"state"`
	Running  int      `json:"running"`
	Waiting  int      `json:"waiting"`
	Sessions []string `json:"-"`
}

// Read samples one profile. Waiting takes precedence over working.
func Read(ctx context.Context, source Source) Status {
	s := Status{Version: 1, State: "idle"}
	activity, err := source.Activity(ctx)
	if err != nil {
		s.State = "error"
		if errors.Is(err, plugin.ErrNotRunning) {
			s.State = "offline"
		}
		return s
	}
	s.Running, s.Waiting = activity.Running, activity.Waiting
	if s.Running > 0 {
		s.State = "working"
	}
	if s.Waiting > 0 {
		s.State = "waiting"
	}
	return s
}

// Label is suitable for a tooltip or a native menu's disabled status item.
func (s Status) Label() string {
	switch s.State {
	case "offline":
		return "Ouroboros — offline"
	case "error":
		return "Ouroboros — status unavailable"
	case "waiting":
		return fmt.Sprintf("Ouroboros — waiting for you (%d)", s.Waiting)
	case "working":
		return fmt.Sprintf("Ouroboros — working (%d)", s.Running)
	default:
		return "Ouroboros — ready"
	}
}

// Waybar is the JSON protocol of a Waybar custom module.
type Waybar struct {
	Text    string `json:"text"`
	Tooltip string `json:"tooltip"`
	Class   string `json:"class"`
	Alt     string `json:"alt"`
}

// ForWaybar renders an adapter without putting Waybar fields in Status.
func (s Status) ForWaybar() Waybar {
	text := "◉"
	switch s.State {
	case "working":
		text = "◉ …"
	case "waiting":
		text = "◉ ?"
	case "offline":
		text = "◎"
	case "error":
		text = "◉ !"
	}
	return Waybar{Text: text, Tooltip: s.Label(), Class: s.State, Alt: s.State}
}
