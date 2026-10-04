// Package identity is a node's cryptographic identity: an Ed25519 key pair
// used to sign requests and to verify the peers that contact this node.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
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
