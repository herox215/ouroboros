package chat

import (
	"encoding/json"
	"testing"
)

// Metadata without fields is written as null, also when it holds an empty
// object; metadata that is no object fails to be written.
func TestGatewayMetadataJSON(t *testing.T) {
	for _, tc := range []struct {
		m    GatewayMetadata
		want string
	}{
		{"", "null"},
		{"{}", "null"},
		{`{"b":1, "a":2}`, `{"a":2,"b":1}`},
	} {
		if got, err := json.Marshal(tc.m); err != nil || string(got) != tc.want {
			t.Errorf("%q: %s, %v; want %s", tc.m, got, err, tc.want)
		}
	}
	if got, err := json.Marshal(GatewayMetadata("[1]")); err == nil {
		t.Errorf("an array was written: %s", got)
	}
}
