// Package identity is a node's cryptographic identity: an Ed25519 key pair
// used to sign requests, to verify the peers that contact this node and to
// prove over TLS that this node is the one that answers.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"time"
)

// Identity is a node's Ed25519 key pair.
type Identity struct {
	private ed25519.PrivateKey
}

// Generate returns a fresh identity.
func Generate() *Identity {
	seed := make([]byte, ed25519.SeedSize)
	rand.Read(seed) // never fails
	return &Identity{private: ed25519.NewKeyFromSeed(seed)}
}

// FromPrivate restores an identity from its base64 private key.
func FromPrivate(key string) (*Identity, error) {
	b, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, errors.New("identity: invalid private key")
	}
	return &Identity{private: ed25519.PrivateKey(b)}, nil
}

// Private returns the base64 private key for persistence.
func (id *Identity) Private() string {
	return base64.StdEncoding.EncodeToString(id.private)
}

// Public returns the base64 public key shared with peers.
func (id *Identity) Public() string {
	return base64.StdEncoding.EncodeToString(id.public())
}

// ID returns the ID of the node: a short stable fingerprint of the public
// key (12 hex chars). Nobody can claim it without a key that it fits.
func (id *Identity) ID() string { return fingerprint(id.public()) }

// IDOf returns the ID that belongs to publicKey (base64), which is what
// Identity.ID returns for the owner of the key. An invalid key has none: "".
func IDOf(publicKey string) string {
	key, ok := decodePublic(publicKey)
	if !ok {
		return ""
	}
	return fingerprint(key)
}

func fingerprint(key ed25519.PublicKey) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:6])
}

// Sign returns the signature of msg.
func (id *Identity) Sign(msg []byte) []byte { return ed25519.Sign(id.private, msg) }

// Certificate returns the TLS certificate of the node: self-signed, for the
// key of the node and nothing else. No authority vouches for it and its dates
// mean nothing: a peer compares its key (see CertificateKey) with the key it
// knows the node by. It is made from the key whenever it is asked for, so
// nothing but the key is ever stored.
func (id *Identity) Certificate() tls.Certificate {
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: id.ID()},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), // "never" in X.509
	}
	// Cannot fail: the template is fixed and an Ed25519 key signs anything.
	der, _ := x509.CreateCertificate(rand.Reader, template, template, id.public(), id.private)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: id.private}
}

// CertificateKey returns the public key (base64) that cert was made for, which
// is what Identity.Public returns for the node that presents it in a TLS
// handshake. A certificate for any other kind of key is not a node's: "".
func CertificateKey(cert *x509.Certificate) string {
	key, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return ""
	}
	return base64.StdEncoding.EncodeToString(key)
}

func (id *Identity) public() ed25519.PublicKey {
	return id.private.Public().(ed25519.PublicKey)
}

// Verify reports whether sig is a valid signature of msg by publicKey (base64).
func Verify(publicKey string, msg, sig []byte) bool {
	key, ok := decodePublic(publicKey)
	return ok && ed25519.Verify(key, msg, sig)
}

func decodePublic(publicKey string) (ed25519.PublicKey, bool) {
	b, err := base64.StdEncoding.DecodeString(publicKey)
	return b, err == nil && len(b) == ed25519.PublicKeySize
}
