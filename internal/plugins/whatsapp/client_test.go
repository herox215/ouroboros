package whatsapp

import (
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// This initializes and migrates real device SQLite storage, but never connects.
func TestOfflineSessionAndReceivedHistoryText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	texts, err := openTextStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer texts.close()
	b, err := newClient(context.Background(), path, texts)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*client)
	if c.Status().Connected || c.Status().Paired {
		t.Fatal(c.Status())
	}
	jid := types.NewJID("123", types.DefaultUserServer)
	c.event(&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: jid, Sender: jid}, ID: "received", Timestamp: time.Unix(10, 0)}, Message: &waE2E.Message{Conversation: proto.String("received text")}})
	c.event(&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: jid, Sender: jid}, ID: "private"}, IsEphemeral: true, Message: &waE2E.Message{Conversation: proto.String("do not keep")}})
	c.event(&events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{{ID: proto.String(jid.String()), Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String("history"), RemoteJID: proto.String(jid.String())}, MessageTimestamp: proto.Uint64(5), Message: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("history text")}}}}}}}}})
	got, err := texts.read(jid.String(), 10)
	if err != nil || len(got) != 2 || got[0].Text != "history text" || got[1].Text != "received text" {
		t.Fatal(got, err)
	}
	c.Close()
	b, err = newClient(context.Background(), path, texts)
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	entries, _ := os.ReadDir(path)
	for _, e := range entries {
		fi, _ := e.Info()
		if fi.Mode().Perm() != 0600 {
			t.Fatal(e.Name(), fi.Mode())
		}
	}
}
func TestQRPNGLocalRotationExpiryAndCleanup(t *testing.T) {
	path := t.TempDir()
	c := &client{path: path}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan whatsmeow.QRChannelItem)
	ready := make(chan struct{}, 1)
	c.wg.Add(1)
	go c.consumeQR(ctx, ch, ready)
	ch <- whatsmeow.QRChannelItem{Event: "code", Code: "harmless-fixture-not-a-pairing-token", Timeout: time.Minute}
	<-ready
	c.stateMu.Lock()
	qr := c.qrPath
	c.stateMu.Unlock()
	if qr != filepath.Join(path, "pairing.png") {
		t.Fatal(qr)
	}
	f, err := os.Open(qr)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(f)
	_ = f.Close()
	if err != nil || img.Bounds().Dx() != 512 {
		t.Fatal(err)
	}
	fi, _ := os.Stat(qr)
	if fi.Mode().Perm() != 0600 {
		t.Fatal(fi.Mode())
	}
	ch <- whatsmeow.QRChannelItem{Event: "code", Code: "second-harmless-fixture", Timeout: 30 * time.Millisecond}
	<-ready
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err = os.Stat(qr)
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expired PNG remains")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ch <- whatsmeow.QRChannelItem{Event: "error", Error: os.ErrPermission, Code: "never expose"}
	<-ready
	c.wg.Wait()
	if strings.Contains(c.note, "never expose") || strings.Contains(c.note, "permission") {
		t.Fatal(c.note)
	}
	if _, err = os.Stat(qr); !os.IsNotExist(err) {
		t.Fatal("QR not cleaned up")
	}
}

func TestConnectSessionSurvivesToolCallAndStopsWithClient(t *testing.T) {
	lifetime, closeClient := context.WithCancel(context.Background())
	defer closeClient()
	call, finishCall := context.WithCancel(context.Background())
	defer finishCall()
	var session context.Context
	_, err := connectSession(call, lifetime, func(ctx context.Context) error {
		session = ctx
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	finishCall()
	if session.Err() != nil {
		t.Fatalf("tool completion canceled the reconnect context: %v", session.Err())
	}
	// A reconnect operation using the retained context must still be runnable.
	operation, cancel := context.WithTimeout(session, time.Second)
	defer cancel()
	if operation.Err() != nil {
		t.Fatal(operation.Err())
	}
	closeClient()
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("client shutdown did not cancel the session")
	}
}

func TestConnectSessionCancellationAndFailure(t *testing.T) {
	t.Run("canceled before dial", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := connectSession(ctx, context.Background(), func(context.Context) error {
			t.Fatal("dial called after cancellation")
			return nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("canceled during dial", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := connectSession(ctx, context.Background(), func(session context.Context) error {
			cancel()
			select {
			case <-session.Done():
				return session.Err()
			case <-time.After(time.Second):
				t.Fatal("initial dial ignored tool cancellation")
				return nil
			}
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("dial failure", func(t *testing.T) {
		var session context.Context
		want := errors.New("fixture dial failure")
		_, err := connectSession(context.Background(), context.Background(), func(ctx context.Context) error {
			session = ctx
			return want
		})
		if !errors.Is(err, want) || !errors.Is(session.Err(), context.Canceled) {
			t.Fatalf("err=%v session=%v", err, session.Err())
		}
	})
}

func TestDisconnectKeepsPairingButLogoutRequiresExplicitPairing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &client{ctx: ctx, path: t.TempDir()}
	c.paired.Store(true)
	c.event(&events.Disconnected{})
	if !c.paired.Load() || c.resetSession.Load() || !strings.Contains(c.note, "automatically reconnecting") || c.qrPath != "" {
		t.Fatalf("disconnect lost pairing or requested QR: %s", c.note)
	}
	c.event(&events.LoggedOut{})
	c.event(&events.Disconnected{})
	if c.paired.Load() || !c.resetSession.Load() || !strings.Contains(c.note, "explicit connect") || c.qrPath != "" {
		t.Fatalf("logout must not reconnect or pair automatically: %s", c.note)
	}
}
