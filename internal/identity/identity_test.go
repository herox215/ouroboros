package identity

import "testing"

func TestRoundTrip(t *testing.T) {
	id := Generate()
	got, err := FromPrivate(id.Private())
	if err != nil {
		t.Fatal(err)
	}
	if got.Public() != id.Public() || got.ID() != id.ID() {
		t.Fatalf("restored %s/%s, want %s/%s", got.ID(), got.Public(), id.ID(), id.Public())
	}
	if len(id.ID()) != 12 {
		t.Fatalf("id %q should be 12 hex chars", id.ID())
	}
}

func TestSignVerify(t *testing.T) {
	id := Generate()
	msg := []byte("hello")
	sig := id.Sign(msg)
	if !Verify(id.Public(), msg, sig) {
		t.Fatal("own signature should verify")
	}
	if Verify(id.Public(), []byte("other"), sig) || Verify(Generate().Public(), msg, sig) {
		t.Fatal("bad message or key must not verify")
	}
	if Verify("not-base64", msg, sig) {
		t.Fatal("garbage key must not verify")
	}
}

func TestFromPrivateRejectsBadKey(t *testing.T) {
	for _, key := range []string{"", "nope", "AAAA"} {
		if _, err := FromPrivate(key); err == nil {
			t.Fatalf("%q should be rejected", key)
		}
	}
}

func TestGenerateIsRandom(t *testing.T) {
	if a, b := Generate(), Generate(); a.Private() == b.Private() || a.Public() == b.Public() {
		t.Fatal("two generated identities should differ")
	}
}
