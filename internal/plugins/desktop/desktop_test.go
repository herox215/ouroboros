package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"ouroboros/internal/kernel"
	"ouroboros/plugin"
)

type testSessions struct {
	plugin.Sessions
	activity plugin.Activity
	err      error
	calls    int
}

func (s *testSessions) Activity(context.Context) (plugin.Activity, error) {
	s.calls++
	return s.activity, s.err
}

func desktopCommand(t *testing.T, sessions plugin.Sessions, extra ...plugin.Plugin) (plugin.Command, *bytes.Buffer) {
	t.Helper()
	out := new(bytes.Buffer)
	services := plugin.NewService("services", func(*plugin.Context, struct{}) (struct {
		Console  *plugin.Console
		Sessions plugin.Sessions
	}, error) {
		return struct {
			Console  *plugin.Console
			Sessions plugin.Sessions
		}{&plugin.Console{Out: out}, sessions}, nil
	})
	var ctx *plugin.Context
	spy := plugin.New("spy", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	})
	k := kernel.New()
	plugins := append([]plugin.Plugin{services, Plugin(), spy}, extra...)
	if err := k.Start(plugins...); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.Stop)
	var commands []plugin.Command
	for _, command := range plugin.All[plugin.Command](ctx) {
		if command.Name == "desktop" {
			commands = append(commands, command)
		}
	}
	if len(commands) != 1 || commands[0].Run == nil {
		t.Fatalf("desktop commands: %+v", commands)
	}
	return commands[0], out
}

func TestStatusAndWaybar(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		activity             plugin.Activity
		err                  error
		state, text, tooltip string
	}{
		{"idle", plugin.Activity{}, nil, "idle", "◉", "Ouroboros — ready"},
		{"working", plugin.Activity{Running: 2}, nil, "working", "◉ …", "Ouroboros — working (2)"},
		{"waiting", plugin.Activity{Running: 2, Waiting: 1}, nil, "waiting", "◉ ?", "Ouroboros — waiting for you (1)"},
		{"offline", plugin.Activity{}, plugin.ErrNotRunning, "offline", "◎", "Ouroboros — offline"},
		{"error", plugin.Activity{}, errors.New("private failure details"), "error", "◉ !", "Ouroboros — status unavailable"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &testSessions{activity: tt.activity, err: tt.err}
			command, out := desktopCommand(t, sessions)
			if sessions.calls != 0 {
				t.Fatal("startup sampled activity")
			}
			for _, mode := range []string{"status", "waybar"} {
				out.Reset()
				if err := command.Run(context.Background(), []string{mode}); err != nil {
					t.Fatal(err)
				}
				want := map[string]any{"version": float64(1), "state": tt.state, "running": float64(tt.activity.Running), "waiting": float64(tt.activity.Waiting)}
				if mode == "waybar" {
					want = map[string]any{"text": tt.text, "tooltip": tt.tooltip, "class": tt.state, "alt": tt.state}
				}
				checkJSON(t, out, want)
			}
			if sessions.calls != 2 {
				t.Fatalf("activity calls = %d, want 2", sessions.calls)
			}
		})
	}
}

func checkJSON(t *testing.T, out *bytes.Buffer, want map[string]any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON = %#v, want %#v", got, want)
	}
}

func TestActivitySourceIsOptional(t *testing.T) {
	// Embedding only Sessions deliberately does not implement ActivitySource.
	sessions := struct{ plugin.Sessions }{}
	command, out := desktopCommand(t, sessions)
	for _, mode := range []string{"status", "waybar"} {
		out.Reset()
		if err := command.Run(context.Background(), []string{mode}); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"version": float64(1), "state": "error", "running": float64(0), "waiting": float64(0)}
		if mode == "waybar" {
			want = map[string]any{"text": "◉ !", "tooltip": "Ouroboros — status unavailable", "class": "error", "alt": "error"}
		}
		checkJSON(t, out, want)
	}
}

func TestOpenForwardsGUICommand(t *testing.T) {
	sessions := &testSessions{}
	sentinel := errors.New("window error")
	call, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	gui := plugin.New("gui", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.Command{Name: "gui", Run: func(ctx context.Context, args []string) error {
			calls++
			if ctx != call || len(args) != 0 {
				t.Fatalf("forwarded context = %v, args = %v", ctx, args)
			}
			return sentinel
		}})
		return nil
	})
	command, out := desktopCommand(t, sessions, gui)
	if err := command.Run(call, []string{"open"}); !errors.Is(err, sentinel) {
		t.Fatalf("open error = %v, want %v", err, sentinel)
	}
	if calls != 1 || sessions.calls != 0 || out.Len() != 0 {
		t.Fatalf("gui calls = %d, activity calls = %d, output = %q", calls, sessions.calls, out.String())
	}
}

func TestOpenWithoutGUI(t *testing.T) {
	sessions := &testSessions{}
	command, out := desktopCommand(t, sessions)
	err := command.Run(context.Background(), []string{"open"})
	if err == nil || !strings.Contains(err.Error(), "graphical UI plugin is disabled") || !strings.Contains(err.Error(), "enable gui") {
		t.Fatalf("open error = %v, want actionable disabled-GUI error", err)
	}
	if sessions.calls != 0 || out.Len() != 0 {
		t.Fatalf("activity calls = %d, output = %q", sessions.calls, out.String())
	}
}

func TestBadArgs(t *testing.T) {
	sessions := &testSessions{}
	guiCalls := 0
	gui := plugin.New("gui", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.Command{Name: "gui", Run: func(context.Context, []string) error {
			guiCalls++
			return nil
		}})
		return nil
	})
	command, out := desktopCommand(t, sessions, gui)
	for _, args := range [][]string{nil, {"unknown"}, {"status", "extra"}, {"waybar", "extra"}, {"panel", "extra"}, {"open", "extra"}} {
		err := command.Run(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "usage:") || !strings.Contains(err.Error(), "status|waybar|panel|open") {
			t.Errorf("args %v: error = %v, want usage", args, err)
		}
	}
	if sessions.calls != 0 || guiCalls != 0 || out.Len() != 0 {
		t.Fatalf("activity calls = %d, gui calls = %d, output = %q", sessions.calls, guiCalls, out.String())
	}
}
