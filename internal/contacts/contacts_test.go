package contacts

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestApproveTrustsAndClearsRequests(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	s.AddIncoming("pk", "gamma", "m", "10.0.0.9:41337")
	if trusted, blocked, _ := s.State("pk"); trusted || blocked {
		t.Fatal("new peer should be neither trusted nor blocked")
	}

	r, ok, _ := s.Approve("pk")
	if !ok || r.Name != "gamma" || r.Addr != "10.0.0.9:41337" {
		t.Fatalf("approved %+v, %v", r, ok)
	}
	if trusted, _, _ := s.State("pk"); !trusted {
		t.Fatal("approved peer should be trusted")
	}
	if d, _ := s.Snapshot(); len(d.Incoming) != 0 {
		t.Fatalf("incoming should be cleared: %+v", d.Incoming)
	}

	// Persisted across a fresh open.
	again := Open(s.path)
	if trusted, _, _ := again.State("pk"); !trusted {
		t.Fatal("trust should persist")
	}
}

func TestRejectBlocks(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	s.AddIncoming("pk", "gamma", "", "")
	if r, ok, _ := s.Reject("pk"); !ok || r.Name != "gamma" {
		t.Fatalf("rejected %+v, %v", r, ok)
	}
	if trusted, blocked, _ := s.State("pk"); !blocked || trusted {
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
	if !hadOutgoing(s, "pk") {
		t.Fatal("should have an outgoing request")
	}
	s.AddOutgoing("pk", "beta", "m", "1.2.3.4:5") // no duplicate
	if d, _ := s.Snapshot(); len(d.Outgoing) != 1 {
		t.Fatalf("got %+v", d.Outgoing)
	}
	s.AddTrusted("pk", "beta", "m")
	if hadOutgoing(s, "pk") {
		t.Fatal("accepting should clear the outgoing request")
	}
	if trusted, _, _ := s.State("pk"); !trusted {
		t.Fatal("should be trusted")
	}
}

// A peer that is trusted is never a request or blocked at the same time.
func TestTrustedReplacesOtherStates(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	s.AddIncoming("pk", "gamma", "", "")
	s.Reject("pk")
	s.AddTrusted("pk", "gamma", "")
	if trusted, blocked, _ := s.State("pk"); !trusted || blocked {
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
	if hadOutgoing(s, "pk") || !hadOutgoing(s, "other") {
		t.Fatal("only the request to pk should be gone")
	}
	if trusted, _, _ := s.State("pk"); trusted {
		t.Fatal("a dropped request must not leave trust behind")
	}
}

// Nobody is trusted or blocked without a request to decide about.
func TestDecisionNeedsRequest(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	if _, ok, _ := s.Approve("pk"); ok {
		t.Fatal("approved a peer that never asked")
	}
	if _, ok, _ := s.Reject("pk"); ok {
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
		// Every change says that it failed, and so does every question.
		for what, err := range map[string]error{
			"AddTrusted":     s.AddTrusted("pk", "gamma", ""),
			"AddIncoming":    s.AddIncoming("pk", "gamma", "", ""),
			"AddOutgoing":    s.AddOutgoing("pk", "gamma", "", ""),
			"RemoveOutgoing": s.RemoveOutgoing("pk"),
		} {
			if err == nil {
				t.Errorf("%s: %s should fail", name, what)
			}
		}
		if r, ok, err := s.Approve("req"); err == nil || ok || r != (Request{}) {
			t.Errorf("%s: Approve = %+v, %v, %v", name, r, ok, err)
		}
		if r, ok, err := s.Reject("req"); err == nil || ok || r != (Request{}) {
			t.Errorf("%s: Reject = %+v, %v, %v", name, r, ok, err)
		}
		if asked, err := s.HadOutgoing("pk"); err == nil || asked {
			t.Errorf("%s: HadOutgoing = %v, %v", name, asked, err)
		}
		if trusted, blocked, err := s.State("pk"); err == nil || trusted || blocked {
			t.Errorf("%s: trusted=%v blocked=%v, %v", name, trusted, blocked, err)
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

	// A directory in the place of the list cannot be read.
	dir := filepath.Join(t.TempDir(), "contacts.json")
	os.Mkdir(dir, 0o700)
	check("directory", Open(dir))

	// A list that cannot be replaced: its directory takes no new files.
	readOnly := t.TempDir()
	unwritable := Open(filepath.Join(readOnly, "contacts.json"))
	unwritable.AddTrusted("other", "beta", "")
	unwritable.AddIncoming("req", "delta", "", "")
	os.Chmod(readOnly, 0o500)
	defer os.Chmod(readOnly, 0o700)
	if os.Getuid() != 0 { // root writes anywhere
		// Reading works here; only the changes fail.
		if err := unwritable.update(func(*Data) error { return nil }); err == nil {
			t.Error("read-only directory: update should fail")
		}
		if _, ok, err := unwritable.Approve("req"); err == nil || ok {
			t.Errorf("read-only directory: Approve = %v, %v", ok, err)
		}
		if _, ok, err := unwritable.Reject("req"); err == nil || ok {
			t.Errorf("read-only directory: Reject = %v, %v", ok, err)
		}
	}
	if trusted, _, err := unwritable.State("other"); !trusted || err != nil {
		t.Errorf("a change that failed should leave the list as it was: %v", err)
	}
	// A decision that was not stored was not made: the request still waits.
	if d, _ := unwritable.Snapshot(); len(d.Incoming) != 1 || len(d.Trusted) != 1 || len(d.Blocked) != 0 {
		t.Errorf("a decision that failed changed the list: %+v", d)
	}

	// A directory in the place of the lock.
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	os.Mkdir(s.path+".lock", 0o700)
	if _, err := s.Snapshot(); err == nil {
		t.Error("without a lock: Snapshot should fail")
	}
	check("without a lock", s)
}

// A change replaces the list instead of writing into it, so that a crash in
// between cannot leave a list that is empty or cut off.
func TestChangeReplacesTheFile(t *testing.T) {
	dir := t.TempDir()
	s := Open(filepath.Join(dir, "contacts.json"))
	s.AddTrusted("pk", "gamma", "")
	old, err := os.Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	before, _ := old.Stat()

	s.AddTrusted("other", "beta", "")
	after, _ := os.Stat(s.path)
	if os.SameFile(before, after) {
		t.Fatal("the list was written in place")
	}
	if after.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 600", after.Mode().Perm())
	}
	if d, err := s.Snapshot(); err != nil || len(d.Trusted) != 2 {
		t.Fatalf("got %+v, %v", d, err)
	}
	// Besides the list there is only its lock: reading writes nothing.
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Fatalf("%d files in the directory, want the list and its lock", len(entries))
	}
	if now, _ := os.Stat(s.path); !os.SameFile(after, now) {
		t.Fatal("reading the list rewrote it")
	}
}

// A list that cannot be encoded is not written.
func TestUpdateUnencodableChange(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	s.AddTrusted("pk", "gamma", "")
	err := s.update(func(d *Data) error {
		d.Trusted = []Contact{{PublicKey: "other", Added: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}
		return nil
	})
	if err == nil {
		t.Fatal("a year JSON cannot hold should fail")
	}
	if trusted, _, _ := s.State("pk"); !trusted {
		t.Fatal("a failed change must not be written")
	}
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
	if trusted, _, _ := s.State("pk"); !trusted {
		t.Fatal("a failed change must not be written")
	}
}

// hadOutgoing reports whether the list says that this node asked publicKey.
func hadOutgoing(s *Store, publicKey string) bool {
	asked, _ := s.HadOutgoing(publicKey)
	return asked
}
