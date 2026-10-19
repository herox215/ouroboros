package contacts

import (
	"path/filepath"
	"testing"
)

func TestQuestionDecisionRejectsStaleRequest(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	if err := s.Propose("key", "peer", "model", "addr"); err != nil {
		t.Fatal(err)
	}
	data, _ := s.Snapshot()
	req := data.Incoming[0]
	if err := s.AddIncoming("key", "peer", "model", "addr"); err != nil {
		t.Fatal(err)
	}
	for _, approve := range []bool{true, false} {
		if _, ok, err := s.DecideRequest(req, approve); err != nil || ok {
			t.Fatalf("stale decision: %v %v", ok, err)
		}
	}
	data, _ = s.Snapshot()
	if len(data.Trusted) != 0 || len(data.Blocked) != 0 || len(data.Outgoing) != 0 || len(data.Incoming) != 1 {
		t.Fatalf("%+v", data)
	}
}
