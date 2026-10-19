//go:build goolm

package matrix

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"maunium.net/go/mautrix/crypto/ssss"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"ouroboros/internal/safefile"
)

// Neither errors of the SDK nor answers of the homeserver leave crossSign:
// its error is shown to the owner as part of the status.
var (
	errCrossSigning        = errors.New("this device could not be cross-signed, so Matrix clients warn about it")
	errUnknownCrossSigning = errors.New("the account has cross-signing keys whose recovery key is not here, so Matrix clients warn about this device")
)

// recoveryKeyPath is where the recovery key of the secret storage of the
// account is kept: beside the crypto directories, for every device of it.
func recoveryKeyPath(cfg Config) string {
	sum := sha256.Sum256([]byte(cfg.Homeserver + "\x00" + cfg.UserID))
	return filepath.Join(cfg.StateDir, fmt.Sprintf("%x.recovery", sum[:16]))
}

// crossSign makes the account vouch for this device, so that the clients of
// the allowed users do not warn that it is not verified by its owner. An
// account without cross-signing keys gets new ones. A later device of the
// account is signed with the keys in the secret storage of the account,
// which the recovery key kept here unlocks.
func (s *sdkCrypto) crossSign(ctx context.Context, c *Client) error {
	mach := s.helper.Machine()
	own := c.cli.UserID
	// Fetching stores the public keys of the account and who signed its devices.
	if _, err := mach.FetchKeys(ctx, []id.UserID{own}, true); err != nil {
		return errCrossSigning
	}
	keys, err := mach.GetCrossSigningPublicKeys(ctx, own)
	if err != nil {
		return errCrossSigning
	}
	path := recoveryKeyPath(c.cfg)
	if keys == nil {
		return s.newCrossSigning(ctx, path)
	}
	if signed, err := mach.CryptoStore.IsKeySignedBy(ctx, own, mach.OwnIdentity().SigningKey, own, keys.SelfSigningKey); err == nil && signed {
		return nil
	}
	recovery, err := os.ReadFile(path)
	if err != nil {
		return errUnknownCrossSigning
	}
	if err := mach.VerifyWithRecoveryKey(ctx, strings.TrimSpace(string(recovery))); err != nil {
		return errCrossSigning
	}
	return nil
}

// newCrossSigning gives an account without cross-signing keys new ones and
// signs this device with them. Each step stands on the ones before it, so a
// start after a failed one takes up where it failed: new keys while none are
// published, the secret storage once they are.
func (s *sdkCrypto) newCrossSigning(ctx context.Context, path string) error {
	mach := s.helper.Machine()
	// Both only read crypto/rand, which never fails.
	keys, _ := mach.GenerateCrossSigningKeys()
	storage, _ := ssss.NewKey("")
	if err := safefile.Write(path, []byte(storage.RecoveryKey()+"\n"), 0600); err != nil {
		return errCrossSigning
	}
	seeds := keys.Seeds()
	steps := []func() error{
		func() error { return mach.SSSS.SetKeyData(ctx, storage.ID, storage.Metadata) },
		func() error {
			return mach.SSSS.SetEncryptedAccountData(ctx, event.AccountDataCrossSigningMaster, seeds.MasterKey, storage)
		},
		func() error {
			return mach.SSSS.SetEncryptedAccountData(ctx, event.AccountDataCrossSigningSelf, seeds.SelfSigningKey, storage)
		},
		func() error {
			return mach.SSSS.SetEncryptedAccountData(ctx, event.AccountDataCrossSigningUser, seeds.UserSigningKey, storage)
		},
		func() error { return mach.SSSS.SetDefaultKeyID(ctx, storage.ID) },
		// The first upload of the keys of an account needs no password.
		func() error { return mach.PublishCrossSigningKeys(ctx, keys, nil) },
		func() error { return mach.SignOwnDevice(ctx, mach.OwnIdentity()) },
		func() error { return mach.SignOwnMasterKey(ctx) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return errCrossSigning
		}
	}
	return nil
}
