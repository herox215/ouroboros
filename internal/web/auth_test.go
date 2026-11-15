package web

import (
	"fmt"
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
		// > 255 costs, narrowed to a uint8 it would pass as 4.
		"argon2id$v=19$m=65536,t=260,p=2$AAAA$AAAA",
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
	// try reserves an attempt and reports it as failed, as the login does.
	try := func(key string) time.Duration {
		t.Helper()
		if wait, ok := th.Start(key); !ok {
			t.Fatalf("%s is held up for %v before the limit", key, wait)
		}
		return th.Failed(key)
	}
	for range 2 {
		if wait := try("host"); wait != 0 {
			t.Fatalf("blocked after two failures: %v", wait)
		}
	}
	if wait, ok := th.Start("host"); !ok || wait != 0 {
		t.Fatalf("blocked before the limit: %v, %v", wait, ok)
	}
	if wait := th.Failed("host"); wait != time.Minute {
		t.Fatalf("the third failure blocks for %v", wait)
	}
	if wait, ok := th.Start("host"); ok || wait <= 0 {
		t.Fatalf("not blocked at the limit: %v, %v", wait, ok)
	}
	if _, ok := th.Start("other"); !ok {
		t.Fatal("another key is blocked too")
	}
	now = now.Add(time.Minute + time.Second)
	if _, ok := th.Start("host"); !ok {
		t.Fatal("still blocked after the first block")
	}
	if wait := th.Failed("host"); wait != 2*time.Minute {
		t.Fatalf("the next block is %v, not twice the first", wait)
	}
	th.Cleared("host")
	if _, ok := th.Start("host"); !ok {
		t.Fatal("a cleared key is still blocked")
	}
}

// Guesses that are on their way hold their place in the limit: a handful of
// parallel posts must not all get through while none of them has failed yet.
func TestThrottleReservesParallelAttempts(t *testing.T) {
	th := NewThrottle(2, time.Minute, time.Hour)
	for i := range 2 {
		if wait, ok := th.Start("host"); !ok {
			t.Fatalf("guess %d is held up for %v", i, wait)
		}
	}
	if wait, ok := th.Start("host"); ok || wait != time.Minute {
		t.Fatalf("a third guess goes ahead while two are on their way: %v, %v", wait, ok)
	}
	th.Cleared("host")
	if _, ok := th.Start("host"); !ok {
		t.Fatal("a cleared key is still held")
	}
}

// A key that is blocked has failed as often as it may: once the block is over,
// one attempt at a time gets through, not a burst of them.
func TestThrottleLetsOneGuessThroughAfterABlock(t *testing.T) {
	now := time.Now()
	th := NewThrottle(2, time.Minute, time.Hour)
	th.now = func() time.Time { return now }
	for range 2 {
		if _, ok := th.Start("host"); !ok {
			t.Fatal("held up before the limit")
		}
		th.Failed("host")
	}
	now = now.Add(time.Minute + time.Second)
	if _, ok := th.Start("host"); !ok {
		t.Fatal("still blocked after the block")
	}
	if wait, ok := th.Start("host"); ok || wait <= 0 {
		t.Fatalf("a second guess goes ahead beside the first: %v, %v", wait, ok)
	}
	if wait := th.Failed("host"); wait != 2*time.Minute {
		t.Fatalf("the next block is %v, not twice the first", wait)
	}
}

// A reservation that is taken back counts as neither an attempt nor a failure.
func TestThrottleCancelReleasesAReservation(t *testing.T) {
	th := NewThrottle(2, time.Minute, time.Hour)
	th.Start("host")
	th.Start("host")
	if _, ok := th.Start("host"); ok {
		t.Fatal("a third reservation goes ahead")
	}
	th.Cancel("host")
	if _, ok := th.Start("host"); !ok {
		t.Fatal("a cancelled reservation still counts")
	}
	th.Cancel("host")
	th.Cancel("host")
	if len(th.byKey) != 0 {
		t.Fatalf("a key that never failed is kept: %+v", th.byKey)
	}
}

// A spray of names must not be able to reset the count of the key it targets:
// only a key nobody tried for a while is forgotten.
func TestThrottlePruneKeepsCounts(t *testing.T) {
	now := time.Now()
	th := NewThrottle(3, time.Minute, time.Hour)
	th.now = func() time.Time { return now }
	for range 2 {
		th.Start("victim")
		th.Failed("victim")
	}
	for i := range 300 {
		key := fmt.Sprintf("junk-%d", i)
		th.Start(key)
		th.Failed(key)
	}
	// The spray is in the map, and the victim is still one failure from its
	// block: its count was not reset by any of them.
	if len(th.byKey) < 256 {
		t.Fatalf("the spray is pruned at once: %d keys", len(th.byKey))
	}
	if wait := th.Failed("victim"); wait != time.Minute {
		t.Fatalf("the victim's failures were forgotten: %v", wait)
	}
	// A key nobody tried for a while is forgotten.
	now = now.Add(2 * th.blockMax)
	th.Start("other")
	if _, ok := th.byKey["junk-0"]; ok {
		t.Fatal("a key nobody tried for hours is kept")
	}
}
