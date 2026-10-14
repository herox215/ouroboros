package computer

import (
	"encoding/json"
	"testing"
)

// An old allow list must not silently turn into a deny list or be ignored.
func TestSettingsRejectAllowList(t *testing.T) {
	tool := (&computer{}).tool()
	if _, err := tool.Configure(json.RawMessage(`{"apps": ["firefox"]}`)); err == nil {
		t.Fatal("accepted the former apps allow list")
	}
	for _, raw := range []string{`{}`, `{"blocked_apps": []}`, `{"blocked_apps": ["firefox"]}`, `{"blocked_apps": ["*"]}`} {
		if _, err := tool.Configure(json.RawMessage(raw)); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
}
