package matrix

import (
	"context"
	"errors"
	"sync"
	"time"
)

// VerificationSnapshot is an immutable copy of the current (or last) verification.
// Stage is idle, requested, ready, exchanging, comparing, confirmed, done, cancelled or failed.
// Only comparing permits ConfirmVerification. Done requires both devices.
type VerificationSnapshot struct {
	TransactionID string
	UserID        string
	DeviceID      string
	Emojis        []VerificationEmoji
	Stage         string
	Error         string
}

type VerificationEmoji struct{ Symbol, Description string }

type verificationBackend interface {
	start(context.Context) (string, error)
	confirm(context.Context, string) error
	cancel(context.Context, string) error
	stop()
}

type verificationState struct {
	op       sync.Mutex // serializes helper calls, event handling and shutdown
	mu       sync.Mutex // callbacks only update snapshots; never call the helper
	life     context.Context
	backend  verificationBackend
	snapshot VerificationSnapshot
	stopped  bool
}

func (v *verificationState) snapshotCopy() VerificationSnapshot {
	v.mu.Lock()
	defer v.mu.Unlock()
	s := v.snapshot
	s.Emojis = append([]VerificationEmoji(nil), s.Emojis...)
	return s
}

// VerificationStatus returns a snapshot; no verification is resumed after restart.
func (c *Client) VerificationStatus() VerificationSnapshot {
	c.mu.RLock()
	v := c.verification
	c.mu.RUnlock()
	if v == nil {
		return VerificationSnapshot{Stage: "idle"}
	}
	return v.snapshotCopy()
}

func (c *Client) verificationState() (*verificationState, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.running || c.verification == nil {
		return nil, errors.New("Matrix verification is not running")
	}
	return c.verification, nil
}

// StartVerification explicitly requests emoji verification of another device of
// this account. Requests from remote devices are never accepted automatically.
// Only one verification may be active. The first ready self-device is selected.
func (c *Client) StartVerification(ctx context.Context) (VerificationSnapshot, error) {
	v, err := c.verificationState()
	if err != nil {
		return c.VerificationStatus(), err
	}
	v.op.Lock()
	defer v.op.Unlock()
	s := v.snapshotCopy()
	if v.stopped {
		return s, errors.New("Matrix verification stopped")
	}
	switch s.Stage {
	case "requested", "ready", "exchanging", "comparing", "confirmed":
		return s, errors.New("Matrix verification already active")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if v.life != nil {
		stop := context.AfterFunc(v.life, cancel)
		defer stop()
		if v.life.Err() != nil {
			cancel()
		}
	}
	txn, err := v.backend.start(ctx)
	v.mu.Lock()
	v.snapshot = VerificationSnapshot{TransactionID: txn, UserID: c.cfg.UserID, Stage: "requested"}
	if err != nil {
		v.snapshot.Stage = "failed"
		v.snapshot.Error = "could not start Matrix verification"
	}
	v.mu.Unlock()
	return v.snapshotCopy(), err
}

// ConfirmVerification must be called only after the owner compared all seven
// emojis on both devices. A transaction ID prevents stale confirmations.
func (c *Client) ConfirmVerification(ctx context.Context, transactionID string) error {
	return c.changeVerification(ctx, transactionID, true)
}

// CancelVerification rejects the current comparison (including mismatched SAS).
func (c *Client) CancelVerification(ctx context.Context, transactionID string) error {
	return c.changeVerification(ctx, transactionID, false)
}

func (c *Client) changeVerification(ctx context.Context, txn string, confirm bool) error {
	v, err := c.verificationState()
	if err != nil {
		return err
	}
	v.op.Lock()
	defer v.op.Unlock()
	s := v.snapshotCopy()
	if v.stopped || txn == "" || s.TransactionID != txn {
		return errors.New("Matrix verification transaction is not active")
	}
	if confirm && (s.Stage != "comparing" || len(s.Emojis) != 7) {
		return errors.New("Matrix emojis are not awaiting local confirmation")
	}
	if !confirm && s.Stage != "requested" && s.Stage != "ready" && s.Stage != "exchanging" && s.Stage != "comparing" && s.Stage != "confirmed" {
		return errors.New("Matrix verification is not active")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if v.life != nil {
		stop := context.AfterFunc(v.life, cancel)
		defer stop()
		if v.life.Err() != nil {
			cancel()
		}
	}
	if confirm {
		err = v.backend.confirm(ctx, txn)
		v.mu.Lock()
		if err == nil {
			v.snapshot.Stage = "confirmed"
		} else {
			v.snapshot.Error = "could not confirm Matrix verification"
		}
		v.mu.Unlock()
	} else {
		err = v.backend.cancel(ctx, txn)
	}
	return err
}

func (c *Client) stopVerification() {
	c.mu.RLock()
	v := c.verification
	c.mu.RUnlock()
	if v == nil {
		return
	}
	v.op.Lock()
	defer v.op.Unlock()
	if v.stopped {
		return
	}
	v.stopped = true
	v.backend.stop()
	v.mu.Lock()
	defer v.mu.Unlock()
	switch v.snapshot.Stage {
	case "requested", "ready", "exchanging", "comparing", "confirmed":
		v.snapshot.Stage = "cancelled"
		v.snapshot.Error = "Matrix transport stopped"
	}
}
