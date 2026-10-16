//go:build !goolm

package matrix

import (
	"context"
	"errors"
)

// CryptoAvailable reports whether the pure Go Matrix crypto backend was built.
const CryptoAvailable = false

func newCrypto(context.Context, *Client) (cryptography, error) {
	return nil, errors.New("Matrix E2EE requires a build with -tags=goolm; plaintext fallback is disabled")
}
