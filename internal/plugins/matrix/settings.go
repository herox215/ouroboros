package matrix

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"ouroboros/internal/safefile"
	"ouroboros/plugin"
)

type loginResult struct{ UserID, Token, DeviceID string }
type loginFunc func(context.Context, string, string, string) (loginResult, error)

func (s store) change(ctx context.Context, change plugin.GatewayChange, login loginFunc) error {
	if change.VerificationAction != "" || change.VerificationTransactionID != "" {
		if err := s.create(); err != nil {
			return errors.New("Matrix verification store is unavailable")
		}
		unlock, err := safefile.Lock(s.path("settings.lock"))
		if err != nil {
			return errors.New("Matrix verification store is unavailable")
		}
		defer unlock()
		cfg, err := s.load()
		if err != nil {
			return errors.New("Matrix verification settings are unavailable")
		}
		return s.queueVerification(cfg, change)
	}
	change, err := normalize(change)
	if err != nil {
		return err
	}
	if err = s.create(); err != nil {
		return err
	}
	unlock, err := safefile.Lock(s.path("settings.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	old, err := s.load()
	if err != nil {
		return err
	}
	cfg := configuration{Enabled: change.Enabled, Homeserver: change.Homeserver, UserID: change.UserID, AllowedUsers: change.AllowedUsers}
	if old.Homeserver == cfg.Homeserver && old.UserID == cfg.UserID {
		cfg.Token = old.Token
		cfg.DeviceID = old.DeviceID
	}
	if change.Logout {
		cfg.Enabled = false
		cfg.Token = ""
		cfg.DeviceID = ""
	} else if change.Password != "" {
		loginCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		result, err := login(loginCtx, cfg.Homeserver, cfg.UserID, change.Password)
		change.Password = ""
		// Neither SDK error bodies nor the submitted password leave this boundary.
		if err != nil {
			return errors.New("the Matrix login failed; check the homeserver, account and password (SSO-only accounts require another login method)")
		}
		if result.UserID != cfg.UserID || result.Token == "" || result.DeviceID == "" {
			return errors.New("the Matrix login returned an unexpected account or incomplete device credentials")
		}
		cfg.Token = result.Token
		cfg.DeviceID = result.DeviceID
	}
	if cfg.Enabled && cfg.Token == "" {
		return errors.New("log in before enabling Matrix")
	}
	var revision [16]byte
	rand.Read(revision[:]) // never fails
	cfg.Revision = hex.EncodeToString(revision[:])
	return s.save(cfg)
}
