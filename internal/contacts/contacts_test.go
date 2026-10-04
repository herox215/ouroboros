package contacts

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
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

// A peer that is trusted is never a request or blocked at the same time.
func TestTrustedReplacesOtherStates(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	s.AddIncoming("pk", "gamma", "", "")
	s.Reject("pk")
	s.AddTrusted("pk", "gamma", "")
	if trusted, blocked := s.State("pk"); !trusted || blocked {
		t.Fatalf("trusted=%v blocked=%v", trusted, blocked)
	}
	// A trusted peer leaves no request, and trusting it twice no second entry.
	s.AddIncoming("pk", "gamma", "", "")
	s.AddTrusted("pk", "gamma", "")
	if d, _ := s.Snapshot(); len(d.Incoming) != 0 || len(d.Trusted) != 1 || len(d.Blocked) != 0 {
		t.Fatalf("got %+v", d)
	}
}

func TestRemoveOutgoing(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	s.AddOutgoing("pk", "beta", "", "")
	s.AddOutgoing("other", "gamma", "", "")
	s.RemoveOutgoing("pk")
	if s.HadOutgoing("pk") || !s.HadOutgoing("other") {
		t.Fatal("only the request to pk should be gone")
	}
	if trusted, _ := s.State("pk"); trusted {
		t.Fatal("a dropped request must not leave trust behind")
	}
}

// Nobody is trusted or blocked without a request to decide about.
func TestDecisionNeedsRequest(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	if _, ok := s.Approve("pk"); ok {
		t.Fatal("approved a peer that never asked")
	}
	if _, ok := s.Reject("pk"); ok {
		t.Fatal("rejected a peer that never asked")
	}
	if d, _ := s.Snapshot(); len(d.Trusted) != 0 || len(d.Blocked) != 0 {
		t.Fatalf("got %+v", d)
	}
}

// A contact list that cannot be read or written trusts nobody.
func TestBrokenStoreTrustsNobody(t *testing.T) {
	check := func(name string, s *Store) {
		t.Helper()
		if err := s.update(func(*Data) error { return nil }); err == nil {
			t.Errorf("%s: update should fail", name)
		}
		s.AddTrusted("pk", "gamma", "")
		if trusted, blocked := s.State("pk"); trusted || blocked {
			t.Errorf("%s: trusted=%v blocked=%v", name, trusted, blocked)
		}
	}

	check("missing directory", Open(filepath.Join(t.TempDir(), "missing", "contacts.json")))

	garbage := filepath.Join(t.TempDir(), "contacts.json")
	os.WriteFile(garbage, []byte("nope"), 0o600)
	check("garbage", Open(garbage))
	if _, err := Open(garbage).Snapshot(); err == nil {
		t.Error("Snapshot should report a file that is not a contact list")
	}
	if b, _ := os.ReadFile(garbage); string(b) != "nope" {
		t.Errorf("an unreadable file should be left alone, got %q", b)
	}

	// Reads and writes of a pipe work, going back to its start does not.
	fifo := filepath.Join(t.TempDir(), "contacts.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	pipe, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	pipe.WriteString("{}\n")
	if err := Open(fifo).update(func(*Data) error { return nil }); !errors.Is(err, syscall.ESPIPE) {
		t.Errorf("pipe: got %v", err)
	}

	// The null device reads as an empty list and cannot be truncated.
	if err := Open(os.DevNull).update(func(*Data) error { return nil }); err == nil {
		t.Error("null device: update should fail")
	}

	locked := errors.New("no lock")
	flock = func(int, int) error { return locked }
	defer func() { flock = syscall.Flock }()
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	if _, err := s.Snapshot(); !errors.Is(err, locked) {
		t.Errorf("without a lock: got %v", err)
	}
	check("without a lock", s)
}

// update hands on the error of a change that failed and writes nothing.
func TestUpdateFailedChange(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	s.AddTrusted("pk", "gamma", "")
	boom := errors.New("boom")
	err := s.update(func(d *Data) error {
		d.Trusted = nil
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if trusted, _ := s.State("pk"); !trusted {
		t.Fatal("a failed change must not be written")
	}
}
