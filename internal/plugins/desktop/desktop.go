// Package desktop contributes desktop status, panel, and open commands.
package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ouroboros/internal/desktop"
	"ouroboros/plugin"
)

// Options connect the desktop adapter to profile wiring and the node log.
type Options struct {
	Profile func(*Profile)
	Log     func(error) // must be safe for background goroutines
}

// Plugin exposes desktop commands and a tray while the node serves.
func Plugin(options ...Options) plugin.Plugin {
	var o Options
	if len(options) > 0 {
		o = options[0]
	}
	return plugin.New("desktop", func(ctx *plugin.Context, n struct {
		Console  *plugin.Console
		Sessions plugin.Sessions
	}) error {
		p := trayProfile(ctx, n.Sessions, o.Log)
		if o.Profile != nil {
			o.Profile(p)
		}
		plugin.Add(ctx, plugin.Command{Name: "desktop", Args: "status|waybar|panel|open", Help: "privacy-safe status (JSON), Waybar output, panel JSON with session titles, or open the graphical UI", Run: func(call context.Context, args []string) error {
			if len(args) != 1 {
				return errors.New("usage: ouro [--profile PROFILE] desktop status|waybar|panel|open")
			}
			switch args[0] {
			case "open":
				for _, command := range plugin.All[plugin.Command](ctx) {
					if command.Name == "gui" {
						return command.Run(call, nil)
					}
				}
				return errors.New("the graphical UI plugin is disabled; enable gui to open it from the desktop")
			case "status", "waybar", "panel":
				call, cancel := context.WithTimeout(call, 2*time.Second)
				defer cancel()
				source, ok := n.Sessions.(plugin.ActivitySource)
				status := desktop.Status{Version: 1, State: "error"}
				if ok {
					status = desktop.Read(call, source)
				}
				var out any = status
				if args[0] == "waybar" {
					out = status.ForWaybar()
				} else if args[0] == "panel" {
					out = readPanel(call, n.Sessions, status)
				}
				return json.NewEncoder(n.Console.Out).Encode(out)
			default:
				return errors.New("usage: ouro [--profile PROFILE] desktop status|waybar|panel|open")
			}
		}})
		return nil
	})
}
