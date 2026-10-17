//go:build goolm

package matrix

import (
	"context"
	"errors"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// The SDK's detached ten-minute expiry uses Background and unbounded network
// I/O. Suppress that lookup: our own timer serializes expiry with shutdown.
type verificationStore struct {
	verificationhelper.VerificationStore
}

func (s verificationStore) GetVerificationTransaction(ctx context.Context, txn id.VerificationTransactionID) (verificationhelper.VerificationTransaction, error) {
	if ctx.Done() == nil {
		return verificationhelper.VerificationTransaction{}, verificationhelper.ErrUnknownVerificationTransaction
	}
	return s.VerificationStore.GetVerificationTransaction(ctx, txn)
}

type sasVerification struct {
	state  *verificationState
	helper *verificationhelper.VerificationHelper
	store  verificationhelper.VerificationStore
	client *Client
	crypto *sdkCrypto // initialized before the verification state is published
	life   context.Context
	timer  *time.Timer
}

type verificationSyncer struct {
	*mautrix.DefaultSyncer
	sas *sasVerification
}

func (s verificationSyncer) OnEventType(t event.Type, handler mautrix.EventHandler) {
	s.DefaultSyncer.OnEventType(t, func(_ context.Context, e *event.Event) {
		v := s.sas
		v.state.op.Lock()
		defer v.state.op.Unlock()
		snap := v.state.snapshotCopy()
		// Do not let the helper see unsolicited requests, other accounts, in-room
		// verification or stale transactions. In particular never AcceptVerification.
		if v.state.stopped || v.life.Err() != nil || e.Sender != v.client.cli.UserID || e.RoomID != "" || t == event.ToDeviceVerificationRequest || t == event.EventMessage {
			return
		}
		txn, ok := e.Content.Parsed.(event.VerificationTransactionable)
		if !ok || string(txn.GetTransactionID()) != snap.TransactionID || snap.TransactionID == "" {
			return
		}
		switch snap.Stage {
		case "requested", "ready", "exchanging", "comparing", "confirmed":
		default:
			return
		}
		// The SDK's MAC handler assumes key exchange has finished. A same-account
		// device can otherwise panic the sync loop with an early matching MAC.
		if t == event.ToDeviceVerificationMAC && snap.Stage != "comparing" && snap.Stage != "confirmed" {
			return
		}
		// The first ready device is pinned. Device-bearing later events cannot switch it.
		if ready, ok := e.Content.Parsed.(*event.VerificationReadyEventContent); ok {
			if ready.FromDevice == "" || ready.FromDevice == v.client.cli.DeviceID || snap.DeviceID != "" {
				return
			}
			devices, err := v.crypto.helper.Machine().CryptoStore.GetDevices(v.life, e.Sender)
			if err != nil || devices[ready.FromDevice] == nil {
				return
			}
		}
		if start, ok := e.Content.Parsed.(*event.VerificationStartEventContent); ok {
			if snap.DeviceID == "" || start.FromDevice != id.DeviceID(snap.DeviceID) {
				return
			}
		}
		ctx, cancel := context.WithTimeout(v.life, 30*time.Second)
		defer cancel()
		handler(ctx, e)
		if v.state.snapshotCopy().Stage == "ready" {
			v.state.mu.Lock()
			v.state.snapshot.Stage = "exchanging"
			v.state.mu.Unlock()
			if err := v.helper.StartSAS(ctx, id.VerificationTransactionID(snap.TransactionID)); err != nil {
				v.fail("could not start emoji comparison")
			}
		}
	})
}

func (s *sdkCrypto) initVerification(ctx context.Context, c *Client) error {
	c.cli.Crypto = s.helper
	state := &verificationState{life: ctx, snapshot: VerificationSnapshot{Stage: "idle"}}
	v := &sasVerification{state: state, client: c, crypto: s, life: ctx, store: verificationStore{verificationhelper.NewInMemoryVerificationStore()}}
	state.backend = v
	v.helper = verificationhelper.NewVerificationHelper(c.cli, s.helper.Machine(), v.store, v, false, false, true)
	original := c.cli.Syncer
	c.cli.Syncer = verificationSyncer{original.(*mautrix.DefaultSyncer), v}
	err := v.helper.Init(ctx)
	c.cli.Syncer = original
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.verification = state
	c.mu.Unlock()
	return nil
}

func (v *sasVerification) start(ctx context.Context) (string, error) {
	if v.life.Err() != nil {
		return "", errors.New("Matrix transport stopped")
	}
	if v.timer != nil {
		v.timer.Stop()
	}
	_ = v.store.DeleteVerification(ctx, id.VerificationTransactionID(v.state.snapshotCopy().TransactionID))
	// Always refresh self devices before broadcasting the explicitly initiated request.
	if _, err := v.crypto.helper.Machine().FetchKeys(ctx, []id.UserID{v.client.cli.UserID}, true); err != nil {
		return "", err
	}
	devices, err := v.crypto.helper.Machine().CryptoStore.GetDevices(ctx, v.client.cli.UserID)
	if err != nil {
		return "", err
	}
	if len(devices) < 2 {
		return "", errors.New("no other Matrix device to verify")
	}
	txn, err := v.helper.StartVerification(ctx, v.client.cli.UserID)
	if err == nil {
		if v.timer != nil {
			v.timer.Stop()
		}
		v.timer = time.AfterFunc(10*time.Minute, func() {
			v.state.op.Lock()
			defer v.state.op.Unlock()
			snap := v.state.snapshotCopy()
			if v.state.stopped || snap.TransactionID != string(txn) {
				return
			}
			switch snap.Stage {
			case "requested", "ready", "exchanging", "comparing", "confirmed":
			default:
				return
			}
			ctx, cancel := context.WithTimeout(v.life, 30*time.Second)
			defer cancel()
			_ = v.cancel(ctx, string(txn))
		})
	}
	return string(txn), err
}
func (v *sasVerification) confirm(ctx context.Context, txn string) error {
	return v.helper.ConfirmSAS(ctx, id.VerificationTransactionID(txn))
}
func (v *sasVerification) cancel(ctx context.Context, txn string) error {
	err := v.helper.CancelVerification(ctx, id.VerificationTransactionID(txn), event.VerificationCancelCodeUser, "Cancelled locally")
	// SDK cancellation returns nil on success; always remove even on network error.
	_ = v.store.DeleteVerification(ctx, id.VerificationTransactionID(txn))
	v.state.mu.Lock()
	v.state.snapshot.Stage = "cancelled"
	v.state.snapshot.Error = ""
	if err != nil {
		v.state.snapshot.Error = "could not notify remote device of cancellation"
	}
	v.state.mu.Unlock()
	if v.timer != nil {
		v.timer.Stop()
	}
	return err
}
func (v *sasVerification) stop() {
	if v.timer != nil {
		v.timer.Stop()
	}
	_ = v.store.DeleteVerification(context.Background(), id.VerificationTransactionID(v.state.snapshotCopy().TransactionID))
}
func (v *sasVerification) fail(reason string) {
	v.state.mu.Lock()
	defer v.state.mu.Unlock()
	v.state.snapshot.Stage = "failed"
	v.state.snapshot.Error = reason
}
func (v *sasVerification) VerificationRequested(context.Context, id.VerificationTransactionID, id.UserID, id.DeviceID) {
}
func (v *sasVerification) VerificationReady(_ context.Context, _ id.VerificationTransactionID, device id.DeviceID, sas, _ bool, _ *verificationhelper.QRCode) {
	if !sas {
		v.fail("remote device does not support emoji comparison")
		return
	}
	v.state.mu.Lock()
	defer v.state.mu.Unlock()
	v.state.snapshot.DeviceID = string(device)
	v.state.snapshot.Stage = "ready"
}
func (v *sasVerification) VerificationCancelled(_ context.Context, txn id.VerificationTransactionID, _ event.VerificationCancelCode, _ string) {
	v.state.mu.Lock()
	defer v.state.mu.Unlock()
	if v.state.snapshot.TransactionID == string(txn) {
		v.state.snapshot.Stage = "cancelled"
		v.state.snapshot.Error = "verification cancelled"
	}
}
func (v *sasVerification) VerificationDone(_ context.Context, _ id.VerificationTransactionID, _ event.VerificationMethod) {
	v.state.mu.Lock()
	defer v.state.mu.Unlock()
	v.state.snapshot.Stage = "done"
}
func (v *sasVerification) ShowSAS(_ context.Context, _ id.VerificationTransactionID, emojis []rune, descriptions []string, _ []int) {
	if len(emojis) != 7 || len(descriptions) != 7 {
		v.fail("remote device did not negotiate seven emojis")
		return
	}
	v.state.mu.Lock()
	defer v.state.mu.Unlock()
	v.state.snapshot.Emojis = make([]VerificationEmoji, 7)
	for i, emoji := range emojis {
		v.state.snapshot.Emojis[i] = VerificationEmoji{string(emoji), descriptions[i]}
	}
	v.state.snapshot.Stage = "comparing"
}
