//go:build goolm

package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/signatures"
	"maunium.net/go/mautrix/crypto/verificationhelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type remoteSAS struct {
	txn    id.VerificationTransactionID
	emojis []VerificationEmoji
	done   bool
}

func (r *remoteSAS) VerificationRequested(_ context.Context, txn id.VerificationTransactionID, _ id.UserID, _ id.DeviceID) {
	r.txn = txn
}
func (*remoteSAS) VerificationReady(context.Context, id.VerificationTransactionID, id.DeviceID, bool, bool, *verificationhelper.QRCode) {
}
func (*remoteSAS) VerificationCancelled(context.Context, id.VerificationTransactionID, event.VerificationCancelCode, string) {
}
func (r *remoteSAS) VerificationDone(context.Context, id.VerificationTransactionID, event.VerificationMethod) {
	r.done = true
}
func (r *remoteSAS) ShowSAS(_ context.Context, _ id.VerificationTransactionID, emojis []rune, desc []string, _ []int) {
	for i, e := range emojis {
		r.emojis = append(r.emojis, VerificationEmoji{string(e), desc[i]})
	}
}

// verificationMatrix serves the account's public identity, not its private keys.
// All fields, including captured uploads, are protected by mockMatrix.mu.
type verificationMatrix struct {
	*mockMatrix
	identity mautrix.UploadCrossSigningKeysReq[any]
	uploads  []mautrix.ReqUploadSignatures
}

func (m *verificationMatrix) serveVerification(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/keys/device_signing/upload"):
		m.mu.Lock()
		defer m.mu.Unlock()
		if err := json.NewDecoder(r.Body).Decode(&m.identity); err != nil {
			m.t.Error(err)
			http.Error(w, "invalid identity", http.StatusBadRequest)
			return
		}
	case strings.HasSuffix(r.URL.Path, "/keys/query"):
		m.mu.Lock()
		defer m.mu.Unlock()
		var req mautrix.ReqQueryKeys
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			m.t.Error(err)
			http.Error(w, "invalid query", http.StatusBadRequest)
			return
		}
		resp := mautrix.RespQueryKeys{
			DeviceKeys:      make(map[id.UserID]map[id.DeviceID]mautrix.DeviceKeys),
			MasterKeys:      make(map[id.UserID]mautrix.CrossSigningKeys),
			SelfSigningKeys: make(map[id.UserID]mautrix.CrossSigningKeys),
			UserSigningKeys: make(map[id.UserID]mautrix.CrossSigningKeys),
		}
		for user := range req.DeviceKeys {
			resp.DeviceKeys[user] = m.keys[user]
			if user == m.identity.Master.UserID {
				resp.MasterKeys[user] = m.identity.Master
				resp.SelfSigningKeys[user] = m.identity.SelfSigning
				resp.UserSigningKeys[user] = m.identity.UserSigning
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			m.t.Error(err)
		}
		return
	case strings.HasSuffix(r.URL.Path, "/keys/signatures/upload"):
		m.mu.Lock()
		defer m.mu.Unlock()
		var req mautrix.ReqUploadSignatures
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			m.t.Error(err)
			http.Error(w, "invalid signatures", http.StatusBadRequest)
			return
		}
		for user, objects := range req {
			for target, obj := range objects {
				if obj.UserID != user || len(obj.Signatures) == 0 {
					m.t.Errorf("invalid signed object for %s/%s", user, target)
					http.Error(w, "invalid signed object", http.StatusBadRequest)
					return
				}
				for signer, sigs := range obj.Signatures {
					for keyID := range sigs {
						_, name := keyID.Parse()
						key := m.keys[signer][id.DeviceID(name)].Keys.GetEd25519(id.DeviceID(name))
						if signer == m.identity.Master.UserID {
							for _, public := range []mautrix.CrossSigningKeys{m.identity.Master, m.identity.SelfSigning, m.identity.UserSigning} {
								if public.Keys[keyID] != "" {
									key = public.Keys[keyID]
								}
							}
						}
						if ok, err := signatures.VerifySignatureJSON(obj, signer, name, key); err != nil || !ok {
							m.t.Errorf("invalid uploaded signature for %s/%s from %s/%s: %v", user, target, signer, keyID, err)
							http.Error(w, "invalid signature", http.StatusBadRequest)
							return
						}
					}
				}
			}
		}
		m.uploads = append(m.uploads, req)
	default:
		m.serve(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}

// Check the observable homeserver upload, independently of the local trust flag.
func (m *verificationMatrix) checkLocalCrossSignature(t *testing.T, local *Client, want bool) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	found := false
	for _, upload := range m.uploads {
		obj, ok := upload[local.cli.UserID][local.cli.DeviceID.String()]
		if !ok {
			continue
		}
		found = true
		device := m.keys[local.cli.UserID][local.cli.DeviceID]
		if obj.UserID != device.UserID || obj.DeviceID != device.DeviceID || !reflect.DeepEqual(obj.Algorithms, device.Algorithms) || len(obj.Keys) != len(device.Keys) {
			t.Fatalf("uploaded signature is not on the local device: %+v", obj)
		}
		for keyID, key := range device.Keys {
			if obj.Keys[id.KeyID(keyID)] != key {
				t.Fatalf("signed local device key %s differs from published device", keyID)
			}
		}
		key := m.identity.SelfSigning.FirstKey()
		if ok, err := signatures.VerifySignatureJSON(obj, local.cli.UserID, key.String(), key); err != nil || !ok {
			t.Fatalf("local device lacks a valid account self-signing signature: %v", err)
		}
	}
	if found != want {
		t.Fatalf("uploaded local device cross-signature = %v, want %v", found, want)
	}
}

func verificationFixture(t *testing.T) (*verificationMatrix, *Client, *Client, *verificationhelper.VerificationHelper, *remoteSAS, context.Context) {
	t.Helper()
	m := &verificationMatrix{mockMatrix: newMock(t)}
	m.server.Config.Handler = http.HandlerFunc(m.serveVerification)
	local := m.client(t, "@bot:test", "BOT")
	remote := m.client(t, "@bot:test", "OTHER")
	lc, rc := setupCrypto(t, local), setupCrypto(t, remote)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	keys, err := rc.helper.Machine().GenerateCrossSigningKeys()
	if err != nil {
		t.Fatal(err)
	}
	// Only the already-trusted remote device has private cross-signing keys.
	// Publish a real signed public identity, which local must learn via keys/query.
	if err := rc.helper.Machine().PublishCrossSigningKeys(ctx, keys, nil); err != nil {
		t.Fatal(err)
	}
	if err := rc.helper.Machine().SignOwnDevice(ctx, rc.helper.Machine().OwnIdentity()); err != nil {
		t.Fatal(err)
	}
	if lc.helper.Machine().CrossSigningKeys != nil {
		t.Fatal("new local device unexpectedly has private cross-signing keys")
	}
	if err := lc.initVerification(ctx, local); err != nil {
		t.Fatal(err)
	}
	local.running = true
	remote.cli.Crypto = rc.helper
	callbacks := &remoteSAS{}
	helper := verificationhelper.NewVerificationHelper(remote.cli, rc.helper.Machine(), nil, callbacks, false, false, true)
	if err := helper.Init(ctx); err != nil {
		t.Fatal(err)
	}
	return m, local, remote, helper, callbacks, ctx
}

func deliverVerification(t *testing.T, m *verificationMatrix, to *Client, ctx context.Context) {
	t.Helper()
	m.mu.Lock()
	events := m.inbox[to.cli.UserID]
	delete(m.inbox, to.cli.UserID)
	m.mu.Unlock()
	for _, e := range events {
		e.Type.Class = event.ToDeviceEventType
		if err := e.Content.ParseRaw(e.Type); err != nil {
			t.Fatal(err)
		}
		to.cli.Syncer.(*mautrix.DefaultSyncer).Dispatch(ctx, e)
	}
}

func TestVerificationRealSASRequiresLocalConfirmation(t *testing.T) {
	for _, mode := range []string{"remote first", "local first", "reject comparison", "malformed MAC before key"} {
		t.Run(mode, func(t *testing.T) {
			remoteFirst := mode != "local first"
			m, local, remote, helper, callbacks, ctx := verificationFixture(t)
			s, err := local.StartVerification(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if s.TransactionID == "" || s.Stage != "requested" || s.UserID != "@bot:test" {
				t.Fatalf("start: %+v", s)
			}
			if _, err := local.StartVerification(ctx); err == nil {
				t.Fatal("parallel transaction allowed")
			}
			if err := local.ConfirmVerification(ctx, s.TransactionID); err == nil {
				t.Fatal("confirmed before SAS")
			}
			injectEarlyMAC := func(wantStage string) {
				t.Helper()
				if mode != "malformed MAC before key" {
					return
				}
				// Matching sender/transaction, but no MAC map and no shared SAS key yet.
				// Dispatch through the real sync handlers: a panic fails this test.
				local.cli.Syncer.(*mautrix.DefaultSyncer).Dispatch(ctx, &event.Event{
					Type: event.ToDeviceVerificationMAC, Sender: local.cli.UserID,
					Content: event.Content{Parsed: &event.VerificationMACEventContent{
						ToDeviceVerificationEvent: event.ToDeviceVerificationEvent{TransactionID: id.VerificationTransactionID(s.TransactionID)},
						Keys:                      []byte{1},
					}},
				})
				if got := local.VerificationStatus().Stage; got != wantStage {
					t.Fatalf("early MAC changed stage: got %s, want %s", got, wantStage)
				}
			}
			injectEarlyMAC("requested")
			m.checkLocalCrossSignature(t, local, false)
			deliverVerification(t, m, remote, ctx)
			if callbacks.txn != id.VerificationTransactionID(s.TransactionID) {
				t.Fatal("request not delivered")
			}
			if err := helper.AcceptVerification(ctx, callbacks.txn); err != nil {
				t.Fatal(err)
			}
			deliverVerification(t, m, local, ctx) // ready -> start
			injectEarlyMAC("exchanging")
			deliverVerification(t, m, remote, ctx) // start -> accept
			deliverVerification(t, m, local, ctx)  // accept -> key
			deliverVerification(t, m, remote, ctx) // key -> key and SAS
			deliverVerification(t, m, local, ctx)  // key -> SAS
			sas := local.VerificationStatus()
			if sas.Stage != "comparing" || sas.DeviceID != "OTHER" || len(sas.Emojis) != 7 || !reflect.DeepEqual(sas.Emojis, callbacks.emojis) {
				t.Fatalf("SAS mismatch: local=%+v remote=%+v", sas, callbacks.emojis)
			}
			sas.Emojis[0].Symbol = "changed"
			if local.VerificationStatus().Emojis[0].Symbol == "changed" {
				t.Fatal("snapshot aliases internal emojis")
			}
			if err := local.ConfirmVerification(ctx, "stale"); err == nil {
				t.Fatal("stale transaction accepted")
			}
			checkUntrusted := func() {
				m.checkLocalCrossSignature(t, local, false)
				d, err := local.crypto.(*sdkCrypto).helper.Machine().CryptoStore.GetDevice(ctx, local.cli.UserID, "OTHER")
				if err != nil || d == nil || d.Trust == id.TrustStateVerified {
					t.Fatalf("device trusted before local confirmation: %+v %v", d, err)
				}
			}
			checkUntrusted()
			if remoteFirst {
				if err := helper.ConfirmSAS(ctx, callbacks.txn); err != nil {
					t.Fatal(err)
				}
				deliverVerification(t, m, local, ctx)
				checkUntrusted()
				if local.VerificationStatus().Stage != "comparing" {
					t.Fatal("remote confirmation bypassed local gate")
				}
			}
			if mode == "reject comparison" {
				if err := local.CancelVerification(ctx, s.TransactionID); err != nil {
					t.Fatal(err)
				}
				checkUntrusted()
				if err := local.ConfirmVerification(ctx, s.TransactionID); err == nil {
					t.Fatal("confirmed rejected comparison")
				}
				return
			}
			if err := local.ConfirmVerification(ctx, s.TransactionID); err != nil {
				t.Fatal(err)
			}
			if err := local.ConfirmVerification(ctx, s.TransactionID); err == nil {
				t.Fatal("duplicate confirmation accepted")
			}
			deliverVerification(t, m, remote, ctx)
			if !remoteFirst {
				if err := helper.ConfirmSAS(ctx, callbacks.txn); err != nil {
					t.Fatal(err)
				}
			}
			deliverVerification(t, m, local, ctx)
			deliverVerification(t, m, remote, ctx)
			deliverVerification(t, m, local, ctx)
			if local.VerificationStatus().Stage != "done" || !callbacks.done {
				t.Fatalf("not done: %+v remote=%v", local.VerificationStatus(), callbacks.done)
			}
			m.checkLocalCrossSignature(t, local, true)
			if local.crypto.(*sdkCrypto).helper.Machine().CrossSigningKeys != nil {
				t.Fatal("SAS unexpectedly installed private cross-signing keys locally")
			}
			d, err := local.crypto.(*sdkCrypto).helper.Machine().CryptoStore.GetDevice(ctx, local.cli.UserID, "OTHER")
			if err != nil || d.Trust != id.TrustStateVerified {
				t.Fatalf("not trusted after verified exchange: %+v %v", d, err)
			}
		})
	}
}

func TestVerificationShutdownCancelsPendingRequest(t *testing.T) {
	m := newMock(t)
	local := m.client(t, "@bot:test", "BOT")
	crypt := setupCrypto(t, local)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := crypt.initVerification(ctx, local); err != nil {
		t.Fatal(err)
	}
	local.running = true
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	m.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/keys/query") {
			close(entered)
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		m.serve(w, r)
	})
	result := make(chan error, 1)
	go func() { _, err := local.StartVerification(context.Background()); result <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request never reached server")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled operation succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run cancellation did not bound verification I/O")
	}
	local.stopVerification()
	if _, err := local.StartVerification(context.Background()); err == nil {
		t.Fatal("started after shutdown")
	}
}

func TestVerificationRejectsUnsolicitedAndOtherAccounts(t *testing.T) {
	m, local, _, _, _, ctx := verificationFixture(t)
	dispatch := func(e *event.Event) { local.cli.Syncer.(*mautrix.DefaultSyncer).Dispatch(ctx, e) }
	dispatch(&event.Event{Type: event.ToDeviceVerificationRequest, Sender: local.cli.UserID, Content: event.Content{Parsed: &event.VerificationRequestEventContent{FromDevice: "OTHER"}}})
	if local.VerificationStatus().Stage != "idle" {
		t.Fatal("accepted unsolicited self request")
	}
	s, err := local.StartVerification(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	delete(m.inbox, local.cli.UserID)
	m.mu.Unlock()
	for _, sender := range []id.UserID{"@alice:test", "@eve:test"} {
		dispatch(&event.Event{Type: event.ToDeviceVerificationReady, Sender: sender, Content: event.Content{Parsed: &event.VerificationReadyEventContent{ToDeviceVerificationEvent: event.ToDeviceVerificationEvent{TransactionID: id.VerificationTransactionID(s.TransactionID)}, FromDevice: "OTHER", Methods: []event.VerificationMethod{event.VerificationMethodSAS}}}})
	}
	if local.VerificationStatus().Stage != "requested" {
		t.Fatal("accepted another account")
	}
	if err := local.CancelVerification(ctx, s.TransactionID); err != nil {
		t.Fatal(err)
	}
	if local.VerificationStatus().Stage != "cancelled" {
		t.Fatal("not cancelled")
	}
	if err := local.ConfirmVerification(ctx, s.TransactionID); err == nil {
		t.Fatal("confirmed cancelled transaction")
	}
	local.stopVerification()
	if _, err := local.StartVerification(ctx); err == nil {
		t.Fatal("started after shutdown")
	}
}
