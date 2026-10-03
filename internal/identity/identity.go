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
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err) // crypto/rand cannot fail on supported platforms
	}
	return &Identity{private: priv}
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

// ID returns a short stable fingerprint of the public key.
func (id *Identity) ID() string {
	sum := sha256.Sum256(id.public())
	return hex.EncodeToString(sum[:6])
}

// Sign returns the signature of msg.
func (id *Identity) Sign(msg []byte) []byte { return ed25519.Sign(id.private, msg) }

func (id *Identity) public() ed25519.PublicKey {
	return id.private.Public().(ed25519.PublicKey)
}

// Verify reports whether sig is a valid signature of msg by publicKey (base64).
func Verify(publicKey string, msg, sig []byte) bool {
	b, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(b), msg, sig)
}
