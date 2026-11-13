package web

import (
	"strings"
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	hash, err := Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "argon2id$v=19$") || strings.Contains(hash, "horse") {
		t.Fatalf("the hash is not one, and must not hold the password: %q", hash)
	}
	if !Verify(hash, "correct horse battery") {
		t.Fatal("the right password does not verify")
	}
	if Verify(hash, "correct horse batterY") {
		t.Fatal("a wrong password verifies")
	}
	for _, broken := range []string{
		"", "plain", "argon2id$v=19$m=65536,t=3,p=2$AAAA$", "bcrypt$v=19$m=65536,t=3,p=2$AAAA$AAAA",
		"argon2id$v=18$m=65536,t=3,p=2$AAAA$AAAA", "argon2id$v=19$m=99999999,t=3,p=2$AAAA$AAAA",
	} {
		if Verify(broken, "correct horse battery") {
			t.Fatalf("%q verifies", broken)
		}
	}
	if _, err := Hash("short"); err == nil {
		t.Fatal("a short password is taken")
	}
}

func TestSessions(t *testing.T) {
	now := time.Now()
	s := NewSessions(time.Hour)
	s.now = func() time.Time { return now }
	token, err := s.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Valid(token) || s.Valid("") || s.Valid("other") {
		t.Fatal("the session does not hold as one does")
	}
	now = now.Add(61 * time.Minute)
	if s.Valid(token) {
		t.Fatal("a session past its time holds")
	}
	second, _ := s.Issue()
	s.RevokeAll()
	if s.Valid(second) {
		t.Fatal("a session revoked with all holds")
	}
}

func TestThrottle(t *testing.T) {
	now := time.Now()
	th := NewThrottle(3, time.Minute, time.Hour)
	th.now = func() time.Time { return now }
	for range 2 {
		if wait := th.Failed("host"); wait != 0 {
			t.Fatalf("blocked after two failures: %v", wait)
		}
	}
	if !th.Allowed("host") {
		t.Fatal("blocked before the limit")
	}
	if wait := th.Failed("host"); wait != time.Minute {
		t.Fatalf("the third failure blocks for %v", wait)
	}
	if th.Allowed("host") {
		t.Fatal("not blocked at the limit")
	}
	if !th.Allowed("other") {
		t.Fatal("another key is blocked too")
	}
	now = now.Add(time.Minute + time.Second)
	if !th.Allowed("host") {
		t.Fatal("still blocked after the first block")
	}
	if wait := th.Failed("host"); wait != 2*time.Minute {
		t.Fatalf("the next block is %v, not twice the first", wait)
	}
	th.Cleared("host")
	if !th.Allowed("host") {
		t.Fatal("a cleared key is still blocked")
	}
}
