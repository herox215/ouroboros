package identity

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
)

// The certificate of a node carries its key, and only the holder of the key
// gets through a handshake with it.
func TestCertificate(t *testing.T) {
	id := Generate()
	cert, err := x509.ParseCertificate(id.Certificate().Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := CertificateKey(cert); got != id.Public() {
		t.Fatalf("CertificateKey = %q, want %q", got, id.Public())
	}
	if cert.Subject.CommonName != id.ID() || cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) != nil {
		t.Fatalf("certificate of %q is not signed by its own key", cert.Subject.CommonName)
	}
	if got := CertificateKey(&x509.Certificate{PublicKey: "no Ed25519 key"}); got != "" {
		t.Fatalf("a certificate for another kind of key has the key %q", got)
	}

	// handshake reports the key the client saw when the server presents cert.
	handshake := func(cert tls.Certificate) (string, error) {
		clientSide, serverSide := net.Pipe()
		defer clientSide.Close()
		go func() {
			defer serverSide.Close()
			tls.Server(serverSide, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}).Handshake()
		}()
		client := tls.Client(clientSide, &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true})
		if err := client.Handshake(); err != nil {
			return "", err
		}
		return CertificateKey(client.ConnectionState().PeerCertificates[0]), nil
	}
	if got, err := handshake(id.Certificate()); err != nil || got != id.Public() {
		t.Fatalf("handshake: %q, %v", got, err)
	}
	// The certificate of id without its key proves nothing.
	stolen := id.Certificate()
	stolen.PrivateKey = Generate().private
	if got, err := handshake(stolen); err == nil {
		t.Fatalf("a node without the key passed for %q", got)
	}
}

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

// Whoever knows the public key can tell the ID, and nobody else has it.
func TestIDOf(t *testing.T) {
	id := Generate()
	if got := IDOf(id.Public()); got != id.ID() {
		t.Fatalf("IDOf = %q, want %q", got, id.ID())
	}
	if IDOf(Generate().Public()) == id.ID() {
		t.Fatal("another key must not have the same ID")
	}
	for _, key := range []string{"", "not-base64", "AAAA"} {
		if got := IDOf(key); got != "" {
			t.Fatalf("invalid key %q has the ID %q", key, got)
		}
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
