package contacts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// The requests that wait are limited: when the list is full, the one that
// waited longest makes room. Peers that are trusted are not touched.
func TestIncomingIsCapped(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	s.AddTrusted("friend", "beta", "")
	for i := range MaxIncoming {
		s.AddIncoming(fmt.Sprint("pk", i), "gamma", "", "")
	}
	has := func(publicKey string) bool {
		t.Helper()
		d, _ := s.Snapshot()
		if len(d.Incoming) != MaxIncoming || len(d.Trusted) != 1 || d.Trusted[0].PublicKey != "friend" {
			t.Fatalf("%d requests, trusted %+v", len(d.Incoming), d.Trusted)
		}
		return slices.ContainsFunc(d.Incoming, match(publicKey))
	}
	// The oldest is the one that waited longest, wherever it is in the list.
	s.update(func(d *Data) error {
		d.Incoming[5].At = d.Incoming[0].At.Add(-time.Hour)
		return nil
	})

	// A request that is there already needs no room.
	s.AddIncoming("pk0", "renamed", "", "")
	if !has("pk5") || !has("pk0") {
		t.Fatal("refreshing a request dropped one")
	}
	s.AddIncoming("new", "delta", "", "")
	if has("pk5") || !has("pk0") || !has("new") {
		t.Fatal("the oldest request should have made room for the new one")
	}
	s.AddIncoming("newer", "delta", "", "")
	if has("pk0") || !has("pk1") || !has("new") || !has("newer") {
		t.Fatal("the next oldest request should have made room")
	}

	// A trusted peer that asks again takes nobody's place.
	s.AddIncoming("friend", "beta", "", "")
	if !has("pk1") {
		t.Fatal("a trusted peer dropped a request")
	}

	// A list from before there was a limit is cut down to it.
	s.update(func(d *Data) error {
		for i := range 3 {
			d.Incoming = append(d.Incoming, Request{PublicKey: fmt.Sprint("extra", i), At: time.Now()})
		}
		return nil
	})
	s.AddIncoming("last", "delta", "", "")
	if !has("last") || !has("extra2") {
		t.Fatal("the newest requests should be kept")
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

// What the agent of the node proposes waits for the owner like a request, but
// is none of a peer: accepting it asks the peer, declining it drops it.
func TestProposal(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	if err := s.Propose("pk", "gamma", "m", "10.0.0.9:41337"); err != nil {
		t.Fatal(err)
	}
	at := func() time.Time { d, _ := s.Snapshot(); return d.Incoming[0].At }
	first := at()
	// Proposed again, it stays the one that waits.
	if err := s.Propose("pk", "another name", "", ""); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Snapshot(); len(d.Incoming) != 1 || !d.Incoming[0].Proposed || d.Incoming[0].Name != "gamma" || !at().Equal(first) {
		t.Fatalf("incoming: %+v", d.Incoming)
	}

	r, ok, err := s.Approve("pk")
	if err != nil || !ok || !r.Proposed || r.Addr != "10.0.0.9:41337" {
		t.Fatalf("approved %+v, %v, %v", r, ok, err)
	}
	d, _ := s.Snapshot()
	if len(d.Incoming) != 0 || len(d.Trusted) != 0 || len(d.Outgoing) != 1 {
		t.Fatalf("an accepted proposal is a request to the peer, nothing else: %+v", d)
	}
	if out := d.Outgoing[0]; out.PublicKey != "pk" || out.Name != "gamma" || out.Model != "m" || out.Addr != "10.0.0.9:41337" || out.Proposed || out.At.IsZero() {
		t.Fatalf("outgoing: %+v", out)
	}

	// Declined, it leaves nothing: the peer is not blocked.
	s.Propose("pk2", "delta", "", "")
	if r, ok, err := s.Reject("pk2"); err != nil || !ok || !r.Proposed {
		t.Fatalf("rejected %+v, %v, %v", r, ok, err)
	}
	if d, _ := s.Snapshot(); len(d.Incoming) != 0 || len(d.Blocked) != 0 {
		t.Fatalf("a declined proposal left something: %+v", d)
	}
}

// A peer that asks itself is a request of that peer, whatever was proposed,
// and a request of a peer is never replaced by a proposal.
func TestProposalAndRequestOfTheSamePeer(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	s.Propose("pk", "gamma", "", "10.0.0.9:41337")
	s.AddIncoming("pk", "gamma", "", "10.0.0.9:5")
	s.Propose("pk", "gamma", "", "10.0.0.9:41337")
	d, _ := s.Snapshot()
	if len(d.Incoming) != 1 || d.Incoming[0].Proposed || d.Incoming[0].Addr != "10.0.0.9:5" {
		t.Fatalf("incoming: %+v", d.Incoming)
	}
	if _, ok, _ := s.Approve("pk"); !ok {
		t.Fatal("no request to approve")
	}
	if trusted, _, _ := s.State("pk"); !trusted {
		t.Fatal("the request of a peer that is approved makes it trusted")
	}
}

// A proposal makes no room for itself: the requests of peers stay.
func TestProposalsDoNotDropRequests(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "contacts.json"))
	for i := range MaxIncoming - 1 {
		s.AddIncoming(fmt.Sprintf("pk%d", i), "peer", "", "")
	}
	if err := s.Propose("last", "gamma", "", ""); err != nil {
		t.Fatalf("the last free place: %v", err)
	}
	if err := s.Propose("one too many", "delta", "", ""); !errors.Is(err, ErrFull) {
		t.Fatalf("got %v", err)
	}
	// One that waits already is no further one.
	if err := s.Propose("last", "gamma", "", ""); err != nil {
		t.Fatalf("proposed again: %v", err)
	}
	if d, _ := s.Snapshot(); len(d.Incoming) != MaxIncoming || d.Incoming[0].PublicKey != "pk0" {
		t.Fatalf("%d requests, the first is %+v", len(d.Incoming), d.Incoming[0])
	}
}

// hadOutgoing reports whether the list says that this node asked publicKey.
func hadOutgoing(s *Store, publicKey string) bool {
	asked, _ := s.HadOutgoing(publicKey)
	return asked
}
