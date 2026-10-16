package desktop

import (
	"context"
	"errors"
	"testing"

	"ouroboros/plugin"
)

type panelSessions struct {
	namedSessions
	calls int
}

func (s *panelSessions) RunningSessions(ctx context.Context) ([]string, error) {
	s.calls++
	if _, ok := ctx.Deadline(); !ok {
		panic("panel must bound live reads")
	}
	return s.names, s.namesErr
}

func TestPanel(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		activity              plugin.Activity
		activityErr, namesErr error
		names                 []string
		state                 string
		wantNames             []any
		calls                 int
	}{
		{name: "working", activity: plugin.Activity{Running: 2}, names: []string{"Private A", "Private B"}, state: "working", wantNames: []any{"Private A", "Private B"}, calls: 1},
		{name: "waiting", activity: plugin.Activity{Running: 1, Waiting: 1}, names: []string{"Private A"}, state: "waiting", wantNames: []any{"Private A"}, calls: 1},
		{name: "idle", state: "idle", wantNames: []any{}, calls: 1},
		{name: "offline", activityErr: plugin.ErrNotRunning, names: []string{"stale"}, state: "offline", wantNames: []any{}},
		{name: "activity error", activityErr: errors.New("private error"), state: "error", wantNames: []any{}},
		{name: "names error", activity: plugin.Activity{Running: 1}, names: []string{"stale"}, namesErr: errors.New("private error"), state: "error", wantNames: []any{}, calls: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := &panelSessions{namedSessions: namedSessions{testSessions: testSessions{activity: tt.activity, err: tt.activityErr}, names: tt.names, namesErr: tt.namesErr}}
			command, out := desktopCommand(t, s)
			if s.calls != 0 || s.testSessions.calls != 0 {
				t.Fatal("startup sampled sessions")
			}
			if err := command.Run(context.Background(), []string{"panel"}); err != nil {
				t.Fatal(err)
			}
			checkJSON(t, out, map[string]any{"version": float64(1), "state": tt.state, "running": float64(tt.activity.Running), "waiting": float64(tt.activity.Waiting), "sessions": tt.wantNames})
			if s.calls != tt.calls || s.testSessions.calls != 1 {
				t.Fatalf("names calls = %d, activity calls = %d", s.calls, s.testSessions.calls)
			}
		})
	}
}

func TestPanelOptionalCapabilities(t *testing.T) {
	for _, tt := range []struct {
		name     string
		sessions plugin.Sessions
		state    string
	}{
		{"no names", &testSessions{}, "idle"},
		{"no activity", struct{ plugin.Sessions }{}, "error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			command, out := desktopCommand(t, tt.sessions)
			if err := command.Run(context.Background(), []string{"panel"}); err != nil {
				t.Fatal(err)
			}
			checkJSON(t, out, map[string]any{"version": float64(1), "state": tt.state, "running": float64(0), "waiting": float64(0), "sessions": []any{}})
		})
	}
}

func TestStatusAndWaybarDoNotReadNames(t *testing.T) {
	s := &panelSessions{namedSessions: namedSessions{names: []string{"Private title"}}}
	command, out := desktopCommand(t, s)
	for _, mode := range []string{"status", "waybar"} {
		out.Reset()
		if err := command.Run(context.Background(), []string{mode}); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"version": float64(1), "state": "idle", "running": float64(0), "waiting": float64(0)}
		if mode == "waybar" {
			want = map[string]any{"text": "◉", "tooltip": "Ouroboros — ready", "class": "idle", "alt": "idle"}
		}
		checkJSON(t, out, want)
	}
	if s.calls != 0 {
		t.Fatalf("privacy-safe commands read names %d times", s.calls)
	}
}
