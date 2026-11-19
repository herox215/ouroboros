package matrix

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"ouroboros/internal/safefile"
	"ouroboros/plugin"
)

type loginResult struct{ UserID, Token, DeviceID string }
type loginFunc func(ctx context.Context, server, httpAllowed, user, password string) (loginResult, error)

func (s store) change(ctx context.Context, change plugin.GatewayChange, login loginFunc) error {
	change, err := normalize(change)
	if err != nil {
		return err
	}
	if err = s.create(); err != nil {
		return plugin.GatewayRefusal{Reason: "the Matrix settings cannot be written on this node"}
	}
	unlock, err := safefile.Lock(s.path("settings.lock"))
	if err != nil {
		return plugin.GatewayRefusal{Reason: "another change of the Matrix settings is running; try again"}
	}
	defer unlock()
	old, err := s.load()
	if err != nil {
		return plugin.GatewayRefusal{Reason: "the saved Matrix settings cannot be read; repair them before saving"}
	}
	cfg := configuration{Enabled: change.Enabled, Homeserver: change.Homeserver, UserID: change.UserID, AllowedUsers: change.AllowedUsers}
	// The allowance only means something for plain HTTP; HTTPS saves none.
	if strings.HasPrefix(cfg.Homeserver, "http://") {
		cfg.HTTPAllowed = httpAllowance(change)
	}
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
		result, err := login(loginCtx, cfg.Homeserver, cfg.HTTPAllowed, cfg.UserID, change.Password)
		change.Password = ""
		// Neither SDK error bodies nor the submitted password leave this boundary.
		if err != nil {
			return plugin.GatewayRefusal{Reason: "the Matrix login failed; check the homeserver, account and password (SSO-only accounts require another login method)"}
		}
		if result.UserID != cfg.UserID || result.Token == "" || result.DeviceID == "" {
			return plugin.GatewayRefusal{Reason: "the Matrix login returned an unexpected account or incomplete device credentials"}
		}
		cfg.Token = result.Token
		cfg.DeviceID = result.DeviceID
	}
	if cfg.Enabled && cfg.Token == "" {
		return plugin.GatewayRefusal{Reason: "log in before enabling Matrix"}
	}
	var revision [16]byte
	rand.Read(revision[:]) // never fails
	cfg.Revision = hex.EncodeToString(revision[:])
	if err := s.save(cfg); err != nil {
		return plugin.GatewayRefusal{Reason: "the Matrix settings cannot be written on this node"}
	}
	return nil
}
