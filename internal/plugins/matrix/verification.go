package matrix

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"ouroboros/internal/safefile"
	"ouroboros/plugin"
)

// verificationConnection is deliberately optional: transports without interactive
// verification remain usable. Only this local owner mailbox invokes its actions;
// Matrix messages and session tools never enter this path.
type verificationConnection interface {
	VerificationStatus(context.Context) (plugin.GatewayVerification, error)
	VerificationAction(context.Context, string, string) error
}

type verificationStatus struct {
	Revision     string
	Session      string
	At           time.Time
	Verification plugin.GatewayVerification
}

type verificationRequest struct {
	Revision      string
	Session       string
	At            time.Time
	Action        string
	TransactionID string
}

func freshVerification(at time.Time) bool {
	age := time.Since(at)
	return age >= 0 && age < 15*time.Second
}

func (s store) readVerification(c configuration) (verificationStatus, bool) {
	var st verificationStatus
	data, err := os.ReadFile(s.path("verification.json"))
	ok := err == nil && json.Unmarshal(data, &st) == nil && c.Enabled && c.Token != "" &&
		st.Revision == c.Revision && st.Session != "" && freshVerification(st.At)
	return st, ok
}

func verificationAllowed(action, txn string, v plugin.GatewayVerification) bool {
	switch action {
	case "start":
		if txn != "" {
			return false
		}
		switch v.Stage {
		case "idle", "done", "cancelled", "failed":
			return true
		}
	case "confirm":
		return txn != "" && txn == v.TransactionID && v.Stage == "comparing" && len(v.Emojis) == 7
	case "cancel":
		if txn != "" && txn == v.TransactionID {
			switch v.Stage {
			case "requested", "ready", "comparing", "confirmed":
				return true
			}
		}
	}
	return false
}

// queueVerification is called with settings.lock held, including by frontends in
// other processes. A single-slot mailbox refuses overwrites rather than losing an
// owner decision. Neither credentials nor settings/revision are rewritten.
func (s store) queueVerification(c configuration, change plugin.GatewayChange) error {
	action := strings.ToLower(change.VerificationAction)
	if action != "start" && action != "confirm" && action != "cancel" {
		return errors.New("unknown Matrix verification action")
	}
	if change.Enabled || change.Password != "" || change.Logout || change.Homeserver != "" || change.UserID != "" || len(change.AllowedUsers) != 0 || len(change.Values) != 0 || len(change.Lists) != 0 {
		return errors.New("save Matrix settings separately from verification actions")
	}
	st, ok := s.readVerification(c)
	if !ok || !verificationAllowed(action, change.VerificationTransactionID, st.Verification) {
		return errors.New("Matrix verification changed or is unavailable; refresh local settings")
	}
	var pending verificationRequest
	data, err := os.ReadFile(s.path("verification-action.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("Matrix verification mailbox is unavailable")
	}
	if json.Unmarshal(data, &pending) == nil && pending.Action != "" && pending.Revision == c.Revision && pending.Session == st.Session && freshVerification(pending.At) {
		return errors.New("a Matrix verification action is already pending")
	}
	request := verificationRequest{Revision: c.Revision, Session: st.Session, At: time.Now(), Action: action, TransactionID: change.VerificationTransactionID}
	data, _ = json.Marshal(request)
	if err := safefile.Write(s.path("verification-action.json"), data, 0600); err != nil {
		return errors.New("Matrix verification action could not be queued")
	}
	return nil
}

// verificationLoop belongs to one live connection, not merely a settings
// revision. Its random session invalidates queued confirmations on reconnect and
// process restart, even if a backend reuses a transaction ID.
func (s store) verificationLoop(ctx context.Context, revision string, conn connection) {
	var id [16]byte
	rand.Read(id[:])
	session := hex.EncodeToString(id[:])
	defer s.invalidateVerification(session)
	consumed := map[string]bool{}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		s.verificationTick(ctx, revision, session, conn, consumed)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Invalidate immediately on disconnect; never clear a newer connection's file.
func (s store) invalidateVerification(session string) {
	unlock, err := safefile.Lock(s.path("settings.lock"))
	if err != nil {
		return
	}
	defer unlock()
	var st verificationStatus
	data, err := os.ReadFile(s.path("verification.json"))
	if err == nil && json.Unmarshal(data, &st) == nil && st.Session == session {
		_ = safefile.Write(s.path("verification.json"), []byte("{}"), 0600)
	}
}

func (s store) verificationTick(ctx context.Context, revision, session string, conn connection, consumed map[string]bool) {
	unlock, err := safefile.Lock(s.path("settings.lock"))
	if err != nil {
		return
	}
	defer unlock()
	cfg, err := s.load()
	if err != nil || !cfg.Enabled || cfg.Token == "" || cfg.Revision != revision || ctx.Err() != nil {
		return
	}
	backend, supported := conn.(verificationConnection)
	v := plugin.GatewayVerification{Stage: "unavailable", Message: "Interactive verification is unavailable on this connection."}
	readStatus := func() {
		callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		state, err := backend.VerificationStatus(callCtx)
		if err != nil {
			v = plugin.GatewayVerification{Stage: "unavailable", Message: "Matrix verification status could not be read."}
			return
		}
		v = state
	}
	if supported {
		readStatus()
	}
	var request verificationRequest
	data, readErr := os.ReadFile(s.path("verification-action.json"))
	if readErr == nil && json.Unmarshal(data, &request) == nil && request.Action != "" {
		// Persist consumption BEFORE calling the backend. If consumption cannot be
		// recorded, fail closed. A crash can lose an action, but cannot replay it.
		if err := safefile.Write(s.path("verification-action.json"), []byte("{}\n"), 0600); err != nil {
			return
		}
		key := request.Action + "\x00" + request.TransactionID
		valid := supported && request.Revision == revision && request.Session == session && freshVerification(request.At) &&
			verificationAllowed(request.Action, request.TransactionID, v) && !consumed[key]
		if valid {
			if request.Action != "start" {
				consumed[key] = true
			}
			callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := backend.VerificationAction(callCtx, request.Action, request.TransactionID)
			cancel()
			if err != nil {
				v = plugin.GatewayVerification{Stage: "failed", Message: "Matrix verification action failed; refresh settings and try again."}
			} else {
				readStatus()
			}
		}
	}
	if ctx.Err() != nil {
		return
	}
	st := verificationStatus{Revision: revision, Session: session, At: time.Now(), Verification: v}
	data, _ = json.Marshal(st)
	_ = safefile.Write(s.path("verification.json"), data, 0600)
}

func (s *sessionConnection) VerificationStatus(ctx context.Context) (plugin.GatewayVerification, error) {
	if backend, ok := s.connection.(verificationConnection); ok {
		return backend.VerificationStatus(ctx)
	}
	return plugin.GatewayVerification{Stage: "unavailable", Message: "Interactive verification is unavailable on this connection."}, nil
}
func (s *sessionConnection) VerificationAction(ctx context.Context, action, txn string) error {
	if backend, ok := s.connection.(verificationConnection); ok {
		return backend.VerificationAction(ctx, action, txn)
	}
	return errors.New("Matrix verification is unavailable")
}
