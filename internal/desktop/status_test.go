package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"ouroboros/plugin"
)

type source struct {
	activity plugin.Activity
	err      error
}

func (s source) Activity(context.Context) (plugin.Activity, error) { return s.activity, s.err }

func TestRead(t *testing.T) {
	for _, test := range []struct {
		name   string
		source source
		state  string
	}{
		{"idle", source{}, "idle"},
		{"working", source{activity: plugin.Activity{Running: 2}}, "working"},
		{"waiting wins", source{activity: plugin.Activity{Running: 2, Waiting: 1}}, "waiting"},
		{"offline", source{err: plugin.ErrNotRunning}, "offline"},
		{"unavailable", source{err: errors.New("secret error")}, "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Read(context.Background(), test.source)
			if got.Version != 1 || got.State != test.state {
				t.Fatalf("%+v", got)
			}
			bar := got.ForWaybar()
			if bar.Class != test.state || bar.Alt != test.state || bar.Tooltip != got.Label() || bar.Text == "" {
				t.Fatalf("%+v", bar)
			}
			encoded, err := json.Marshal(bar)
			if err != nil || !json.Valid(encoded) {
				t.Fatalf("%s %v", encoded, err)
			}
		})
	}
}
