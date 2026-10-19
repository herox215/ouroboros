//go:build goolm

package matrix

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix/id"
)

// trustOf is how much the client watcher trusts the device of user, as a
// client of an allowed user decides whether to warn about its messages.
func trustOf(t *testing.T, watcher *sdkCrypto, user id.UserID, device id.DeviceID) id.TrustState {
	t.Helper()
	ctx := context.Background()
	mach := watcher.helper.Machine()
	if _, err := mach.FetchKeys(ctx, []id.UserID{user}, true); err != nil {
		t.Fatal(err)
	}
	d, err := mach.CryptoStore.GetDevice(ctx, user, device)
	if err != nil || d == nil {
		t.Fatalf("device %s of %s: %v", device, user, err)
	}
	trust, err := mach.ResolveTrustContext(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	return trust
}

// deviceOf starts the encryption of another device of the user of c, which
// keeps its state in dir.
func (m *mockMatrix) deviceOf(t *testing.T, c *Client, device, dir string) (*Client, *sdkCrypto) {
	t.Helper()
	cfg := c.cfg
	cfg.DeviceID, cfg.StateDir = device, dir
	other, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return other, setupCrypto(t, other)
}

func crossSignContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestCrossSignGivesTheAccountKeysAndSignsTheDevice(t *testing.T) {
	m := newMock(t)
	ctx := crossSignContext(t)
	bot := m.client(t, "@bot:test", "BOT")
	crypt := setupCrypto(t, bot)
	watcher := setupCrypto(t, m.client(t, "@alice:test", "ALICE"))
	if trust := trustOf(t, watcher, "@bot:test", "BOT"); trust != id.TrustStateUnset {
		t.Fatalf("trust before cross-signing: %s", trust)
	}
	if err := crypt.crossSign(ctx, bot); err != nil {
		t.Fatal(err)
	}
	// Clients see the device signed by its owner and do not warn about it.
	if trust := trustOf(t, watcher, "@bot:test", "BOT"); trust != id.TrustStateCrossSignedTOFU {
		t.Fatalf("trust after cross-signing: %s", trust)
	}
	m.mu.Lock()
	keys := m.crossSigning["@bot:test"]
	data := m.accountData["@bot:test"]
	m.mu.Unlock()
	master := keys.Master.FirstKey().String()
	if _, ok := keys.Master.Signatures["@bot:test"][id.NewKeyID(id.KeyAlgorithmEd25519, "BOT")]; !ok {
		t.Fatalf("the device did not sign the master key %s: %v", master, keys.Master.Signatures)
	}
	for _, name := range []string{"m.secret_storage.default_key", "m.cross_signing.master", "m.cross_signing.self_signing", "m.cross_signing.user_signing"} {
		if _, ok := data[name]; !ok {
			t.Fatalf("no %s in the account data: the keys cannot be recovered", name)
		}
	}
	info, err := os.Stat(recoveryKeyPath(bot.cfg))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("recovery key: %v", err)
	}
	// Starting again finds the device signed and changes nothing.
	m.mu.Lock()
	signatures := m.signatures
	m.mu.Unlock()
	if err := crypt.crossSign(ctx, bot); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys = m.crossSigning["@bot:test"]
	if m.uploads != 1 || m.signatures != signatures || keys.Master.FirstKey().String() != master {
		t.Fatalf("keys uploaded %d times, signatures %d times instead of %d", m.uploads, m.signatures, signatures)
	}
}

func TestCrossSignSignsALaterDeviceWithTheRecoveryKey(t *testing.T) {
	m := newMock(t)
	ctx := crossSignContext(t)
	bot := m.client(t, "@bot:test", "BOT")
	if err := setupCrypto(t, bot).crossSign(ctx, bot); err != nil {
		t.Fatal(err)
	}
	watcher := setupCrypto(t, m.client(t, "@alice:test", "ALICE"))
	// A new login of the account keeps its state beside that of the first.
	later, crypt := m.deviceOf(t, bot, "LATER", bot.cfg.StateDir)
	if err := crypt.crossSign(ctx, later); err != nil {
		t.Fatal(err)
	}
	if trust := trustOf(t, watcher, "@bot:test", "LATER"); trust != id.TrustStateCrossSignedTOFU {
		t.Fatalf("trust of the later device: %s", trust)
	}
	// Elsewhere the recovery key is missing, and the keys of the account stay.
	elsewhere, crypt := m.deviceOf(t, bot, "ELSEWHERE", t.TempDir())
	if err := crypt.crossSign(ctx, elsewhere); !errors.Is(err, errUnknownCrossSigning) {
		t.Fatalf("cross-signing without the recovery key: %v", err)
	}
	if trust := trustOf(t, watcher, "@bot:test", "ELSEWHERE"); trust != id.TrustStateUnset {
		t.Fatalf("trust of the device without the recovery key: %s", trust)
	}
	// A recovery key that does not unlock the secret storage signs nothing.
	wrong, crypt := m.deviceOf(t, bot, "WRONG", t.TempDir())
	if err := os.WriteFile(recoveryKeyPath(wrong.cfg), []byte("not a recovery key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := crypt.crossSign(ctx, wrong); !errors.Is(err, errCrossSigning) {
		t.Fatalf("cross-signing with a wrong recovery key: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uploads != 1 {
		t.Fatalf("keys uploaded %d times", m.uploads)
	}
}

// A start after a failed one takes up where it failed and ends with the
// device signed, whichever request failed.
func TestCrossSignRecoversFromAFailedStart(t *testing.T) {
	path := func(part string) func(*http.Request) bool {
		return func(r *http.Request) bool { return strings.Contains(r.URL.Path, part) }
	}
	// nth fails only the nth request that contains part.
	nth := func(part string, n int) func(*http.Request) bool {
		seen := 0
		return func(r *http.Request) bool {
			if !strings.Contains(r.URL.Path, part) {
				return false
			}
			seen++
			return seen == n
		}
	}
	for _, tc := range []struct {
		name    string
		refuse  func(*http.Request) bool
		uploads int // how often the keys were uploaded when the start failed
	}{
		{"device list", path("/keys/query"), 0},
		{"public keys", nth("/keys/query", 2), 0},
		{"secret storage key", path("m.secret_storage.key."), 0},
		{"master key secret", path("m.cross_signing.master"), 0},
		{"self-signing key secret", path("m.cross_signing.self_signing"), 0},
		{"user-signing key secret", path("m.cross_signing.user_signing"), 0},
		{"default secret storage key", path("m.secret_storage.default_key"), 0},
		{"publishing", path("/keys/device_signing/upload"), 0},
		{"signing the device", nth("/keys/signatures/upload", 1), 1},
		{"signing the master key", nth("/keys/signatures/upload", 2), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMock(t)
			ctx := crossSignContext(t)
			bot := m.client(t, "@bot:test", "BOT")
			crypt := setupCrypto(t, bot)
			m.mu.Lock()
			m.refuse = tc.refuse
			m.mu.Unlock()
			if err := crypt.crossSign(ctx, bot); !errors.Is(err, errCrossSigning) {
				t.Fatalf("failed start: %v", err)
			}
			m.mu.Lock()
			m.refuse = nil
			uploads := m.uploads
			m.mu.Unlock()
			if uploads != tc.uploads {
				t.Fatalf("keys uploaded %d times by the failed start", uploads)
			}
			// The failed start left no state behind that keeps the next one
			// from signing: a new client starts on the state of the device.
			bot.running = false
			if err := bot.Close(); err != nil {
				t.Fatal(err)
			}
			again, err := New(bot.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := setupCrypto(t, again).crossSign(ctx, again); err != nil {
				t.Fatal(err)
			}
			watcher := setupCrypto(t, m.client(t, "@alice:test", "ALICE"))
			if trust := trustOf(t, watcher, "@bot:test", "BOT"); trust != id.TrustStateCrossSignedTOFU {
				t.Fatalf("trust after the next start: %s", trust)
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.uploads != 1 {
				t.Fatalf("keys uploaded %d times", m.uploads)
			}
		})
	}
}

// Without a place for the recovery key nothing is uploaded: keys that could
// not be recovered would keep a later device from being signed.
func TestCrossSignKeepsTheRecoveryKeyFirst(t *testing.T) {
	m := newMock(t)
	bot := m.client(t, "@bot:test", "BOT")
	crypt := setupCrypto(t, bot)
	if err := os.Mkdir(recoveryKeyPath(bot.cfg), 0700); err != nil {
		t.Fatal(err)
	}
	if err := crypt.crossSign(crossSignContext(t), bot); !errors.Is(err, errCrossSigning) {
		t.Fatalf("cross-signing without a place for the recovery key: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uploads != 0 || len(m.accountData["@bot:test"]) != 0 {
		t.Fatalf("uploaded keys %d times and account data %v", m.uploads, m.accountData["@bot:test"])
	}
}
