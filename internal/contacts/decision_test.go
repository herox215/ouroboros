package contacts

import (
	"os"
	"path/filepath"
	"testing"
)

// An approved incoming connection becomes a trusted contact.
func TestQuestionDecisionTrustsAnApprovedIncoming(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	if err := s.AddIncoming("key", "peer", "model", "addr"); err != nil {
		t.Fatal(err)
	}
	data, err := s.Snapshot()
	if err != nil || len(data.Incoming) != 1 {
		t.Fatalf("request: %+v, %v", data, err)
	}
	if _, ok, err := s.DecideRequest(data.Incoming[0], true); err != nil || !ok {
		t.Fatalf("approval: %v, %v", ok, err)
	}
	data, err = s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Trusted) != 1 || data.Trusted[0].PublicKey != "key" || len(data.Incoming) != 0 {
		t.Fatalf("trusted: %+v", data)
	}
}

// A decision on an unreadable contact list fails and changes nothing, rather
// than silently replacing the list.
func TestQuestionDecisionReportsAnUnreadableStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "contacts.json")
	s := Open(path)
	if err := s.AddIncoming("key", "peer", "model", "addr"); err != nil {
		t.Fatal(err)
	}
	data, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.DecideRequest(data.Incoming[0], true); err == nil || ok {
		t.Fatalf("decision on an unreadable store: %v, %v", ok, err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "{not json" {
		t.Fatalf("store changed: %q, %v", b, err)
	}
}

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
