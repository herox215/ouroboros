package contacts

import (
	"path/filepath"
	"testing"
)

func TestApproveTrustsAndClearsRequests(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	s.AddIncoming("pk", "gamma", "m", "10.0.0.9:41337")
	if trusted, blocked := s.State("pk"); trusted || blocked {
		t.Fatal("new peer should be neither trusted nor blocked")
	}

	r, ok := s.Approve("pk")
	if !ok || r.Name != "gamma" || r.Addr != "10.0.0.9:41337" {
		t.Fatalf("approved %+v, %v", r, ok)
	}
	if trusted, _ := s.State("pk"); !trusted {
		t.Fatal("approved peer should be trusted")
	}
	if d, _ := s.Snapshot(); len(d.Incoming) != 0 {
		t.Fatalf("incoming should be cleared: %+v", d.Incoming)
	}

	// Persisted across a fresh open.
	again := Open(s.path)
	if trusted, _ := again.State("pk"); !trusted {
		t.Fatal("trust should persist")
	}
}

func TestRejectBlocks(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	s.AddIncoming("pk", "gamma", "", "")
	if r, ok := s.Reject("pk"); !ok || r.Name != "gamma" {
		t.Fatalf("rejected %+v, %v", r, ok)
	}
	if trusted, blocked := s.State("pk"); !blocked || trusted {
		t.Fatalf("rejected peer should be blocked: trusted=%v blocked=%v", trusted, blocked)
	}
	// A blocked peer is not recorded again.
	s.AddIncoming("pk", "gamma", "", "")
	if d, _ := s.Snapshot(); len(d.Incoming) != 0 {
		t.Fatalf("blocked peer must not create requests: %+v", d.Incoming)
	}
}

func TestIncomingIsUpserted(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	s.AddIncoming("pk", "old", "", "a")
	s.AddIncoming("pk", "new", "", "b")
	d, _ := s.Snapshot()
	if len(d.Incoming) != 1 || d.Incoming[0].Name != "new" || d.Incoming[0].Addr != "b" {
		t.Fatalf("got %+v", d.Incoming)
	}
}

func TestOutgoing(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	s.AddOutgoing("pk", "beta", "m", "1.2.3.4:5")
	if !s.HadOutgoing("pk") {
		t.Fatal("should have an outgoing request")
	}
	s.AddOutgoing("pk", "beta", "m", "1.2.3.4:5") // no duplicate
	if d, _ := s.Snapshot(); len(d.Outgoing) != 1 {
		t.Fatalf("got %+v", d.Outgoing)
	}
	s.AddTrusted("pk", "beta", "m")
	if s.HadOutgoing("pk") {
		t.Fatal("accepting should clear the outgoing request")
	}
	if trusted, _ := s.State("pk"); !trusted {
		t.Fatal("should be trusted")
	}
}
