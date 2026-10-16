//go:build goolm

package matrix

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	_ "modernc.org/sqlite"
)

// CryptoHelper's default encrypted-event handler starts detached key-request
// goroutines. Keep event processing within Run's context instead.
type helperSyncer struct{ *mautrix.DefaultSyncer }

func (s helperSyncer) OnEventType(t event.Type, handler mautrix.EventHandler) {
	if t != event.EventEncrypted {
		s.DefaultSyncer.OnEventType(t, handler)
	}
}

type sdkCrypto struct {
	helper *cryptohelper.CryptoHelper
	lock   *os.File
}

func cryptoStateDir(cfg Config) string {
	sum := sha256.Sum256([]byte(cfg.Homeserver + "\x00" + cfg.UserID + "\x00" + cfg.DeviceID))
	return filepath.Join(cfg.StateDir, fmt.Sprintf("%x", sum[:16]))
}

func newCrypto(ctx context.Context, c *Client) (cryptography, error) {
	if err := os.MkdirAll(c.cfg.StateDir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(c.cfg.StateDir, 0700); err != nil {
		return nil, err
	}
	stateDir := cryptoStateDir(c.cfg)
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(stateDir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(stateDir, "crypto.lock"), os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("the Matrix crypto state is already in use")
	}
	success := false
	defer func() {
		if !success {
			lock.Close()
		}
	}()
	keyPath := filepath.Join(stateDir, "pickle.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		file, openErr := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr != nil {
			return nil, openErr
		}
		_, err = file.Write(key)
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid Matrix crypto pickle key")
	}
	if err = os.Chmod(keyPath, 0600); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(stateDir, "crypto.db")
	f, err := os.OpenFile(dbPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Chmod(dbPath, 0600); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: dbPath}
	q := u.Query()
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	wrapped, err := dbutil.NewWithDB(db, "sqlite3")
	if err != nil {
		db.Close()
		return nil, err
	}
	helper, err := cryptohelper.NewCryptoHelper(c.cli, key, wrapped)
	if err != nil {
		db.Close()
		return nil, err
	}
	helper.DBAccountID = c.cfg.UserID
	originalSyncer := c.cli.Syncer
	c.cli.Syncer = helperSyncer{originalSyncer.(*mautrix.DefaultSyncer)}
	defer func() { c.cli.Syncer = originalSyncer }()
	if err = helper.Init(ctx); err != nil {
		helper.Close()
		return nil, fmt.Errorf("initialize Matrix crypto: %w", err)
	}
	// Never redistribute historical room keys, even to another device of ourselves.
	helper.Machine().AllowKeyShare = func(context.Context, *id.Device, event.RequestedKeyInfo) *crypto.KeyShareRejection {
		return &crypto.KeyShareRejectNoResponse
	}
	success = true
	return &sdkCrypto{helper: helper, lock: lock}, nil
}
func (s *sdkCrypto) decrypt(ctx context.Context, e *event.Event) (*event.Event, error) {
	return s.helper.Decrypt(ctx, e)
}
func (s *sdkCrypto) encrypt(ctx context.Context, room id.RoomID, peer id.UserID, text string) (*event.EncryptedEventContent, error) {
	mach := s.helper.Machine()
	// Start a fresh session for each reply, so former room members can never
	// decrypt new replies using a previously shared outbound session.
	if err := mach.CryptoStore.RemoveOutboundGroupSession(ctx, room); err != nil {
		return nil, err
	}
	if err := mach.ShareGroupSession(ctx, room, []id.UserID{mach.Client.UserID, peer}); err != nil {
		return nil, err
	}
	return mach.EncryptMegolmEvent(ctx, room, event.EventMessage, &event.MessageEventContent{MsgType: event.MsgText, Body: text})
}
func (s *sdkCrypto) close() error {
	s.helper.Machine().Destroy()
	err := s.helper.Close()
	if lockErr := s.lock.Close(); err == nil {
		err = lockErr
	}
	return err
}
