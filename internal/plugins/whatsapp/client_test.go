package whatsapp

import (
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

// fakeNetwork stands in for the network side of whatsmeow while a test runs:
// dial answers with dialErr, the socket is as connected and as logged in as
// the test says, and a send is acknowledged.
type fakeNetwork struct {
	dialErr             error
	dials               atomic.Int32
	connected, loggedIn atomic.Bool
}

func offline(t *testing.T) *fakeNetwork {
	t.Helper()
	n := &fakeNetwork{}
	oldDial, oldConnected, oldLoggedIn, oldSend, oldWait := dial, isConnected, isLoggedIn, sendMessage, qrWait
	t.Cleanup(func() {
		dial, isConnected, isLoggedIn, sendMessage, qrWait = oldDial, oldConnected, oldLoggedIn, oldSend, oldWait
	})
	dial = func(*whatsmeow.Client, context.Context) error { n.dials.Add(1); return n.dialErr }
	isConnected = func(*whatsmeow.Client) bool { return n.connected.Load() }
	isLoggedIn = func(*whatsmeow.Client) bool { return n.loggedIn.Load() }
	sendMessage = func(_ *whatsmeow.Client, _ context.Context, _ types.JID, _ *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
		return whatsmeow.SendResponse{ID: "ack", Timestamp: time.Unix(7, 0)}, nil
	}
	return n
}

// newTestClient is a client with real device storage in a fresh directory.
func newTestClient(t *testing.T) *client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state")
	texts, err := openTextStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(texts.close)
	b, err := newClient(context.Background(), path, texts)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*client)
	t.Cleanup(c.Close)
	return c
}

// dispatch hands evt to the event handlers of the current whatsmeow client,
// as its socket does.
func dispatch(c *client, evt any) {
	//lint:ignore SA1019 the test stands in for the socket, which dispatches events
	c.current().DangerousInternals().DispatchEvent(evt)
}

// waitFor waits until ok holds, read under stateMu.
func waitFor(t *testing.T, c *client, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.stateMu.Lock()
		done := ok()
		c.stateMu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("waited in vain: " + what)
		}
		time.Sleep(time.Millisecond)
	}
}

// Session storage that cannot be opened, migrated or read makes no client.
func TestNewClientFailures(t *testing.T) {
	path := t.TempDir()
	if err := os.Mkdir(filepath.Join(path, "session.db"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := newClient(context.Background(), path, nil); err == nil {
		t.Fatal("a directory as session database")
	}
	path = t.TempDir()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newClient(canceled, path, nil); err == nil {
		t.Fatal("migrated without time to do it")
	}
	b, err := newClient(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	db, err := privateDB(filepath.Join(path, "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA foreign_keys=OFF; DROP TABLE whatsmeow_device`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := newClient(context.Background(), path, nil); err == nil {
		t.Fatal("read a device from a migrated database without devices")
	}
}

// Pairing from the start: Connect waits for the first QR code, a second
// Connect does not pair again, and success pairs. Then Connect
// connects with the saved pairing, says so while authentication is open, and
// does nothing once connected.
func TestConnectPairsThenConnects(t *testing.T) {
	n := offline(t)
	c := newTestClient(t)
	pairing := func() bool { return c.pairing }
	type result struct {
		st  status
		err error
	}
	first := make(chan result, 1)
	go func() { st, err := c.Connect(context.Background()); first <- result{st, err} }()
	waitFor(t, c, "pairing", pairing)
	code := func() {
		c.stateMu.Lock()
		before := c.qrExpires
		c.stateMu.Unlock()
		dispatch(c, &events.QR{Codes: []string{"harmless-fixture-not-a-pairing-token"}})
		waitFor(t, c, "a new code", func() bool { return c.qrExpires.After(before) })
	}
	code()
	r := <-first
	if r.err != nil || r.st.QRPath != filepath.Join(c.path, "pairing.png") || r.st.Paired || r.st.Connected {
		t.Fatalf("first connect: %+v %v", r.st, r.err)
	}
	if st, err := c.Connect(context.Background()); err != nil || !strings.Contains(st.Note, "Scan the private local PNG") {
		t.Fatalf("connect while pairing: %+v %v", st, err)
	}
	if n.dials.Load() != 1 {
		t.Fatalf("dialed %d times while pairing", n.dials.Load())
	}
	dispatch(c, &events.PairSuccess{})
	waitFor(t, c, "the end of pairing", func() bool { return !c.pairing })
	if !c.paired.Load() || c.qrPath != "" {
		t.Fatalf("paired %v, QR %q", c.paired.Load(), c.qrPath)
	}
	st, err := c.Connect(context.Background())
	if err != nil || !strings.Contains(st.Note, "Connection requested") || n.dials.Load() != 2 {
		t.Fatalf("connect when paired: %+v %v", st, err)
	}
	n.dialErr = whatsmeow.ErrAlreadyConnected
	if st, err = c.Connect(context.Background()); err != nil || !strings.Contains(st.Note, "Connection requested") {
		t.Fatalf("connect when the socket opened meanwhile: %+v %v", st, err)
	}
	n.dialErr = errors.New("fixture dial failure")
	if _, err = c.Connect(context.Background()); !errors.Is(err, n.dialErr) {
		t.Fatalf("failed dial: %v", err)
	}
	n.connected.Store(true)
	if st, err = c.Connect(context.Background()); err != nil || st.Connected || !strings.Contains(st.Note, "authentication is still in progress") {
		t.Fatalf("connect while authenticating: %+v %v", st, err)
	}
	n.loggedIn.Store(true)
	dials := n.dials.Load()
	if st, err = c.Connect(context.Background()); err != nil || !st.Connected || n.dials.Load() != dials {
		t.Fatalf("connect when connected: %+v %v", st, err)
	}
	c.Close()
	if _, err = c.Connect(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("connect after close: %v", err)
	}
}

// Connect for pairing ends when the tool call does, when the client closes,
// when no code comes in time, and when the dial fails. After a logout it
// pairs again with a new device.
func TestConnectForPairingEnds(t *testing.T) {
	n := offline(t)
	t.Run("tool call ends", func(t *testing.T) {
		c := newTestClient(t)
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { _, err := c.Connect(ctx); result <- err }()
		waitFor(t, c, "pairing", func() bool { return c.pairing })
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if c.pairing {
			t.Fatal("still pairing")
		}
	})
	t.Run("client closes", func(t *testing.T) {
		c := newTestClient(t)
		result := make(chan error, 1)
		go func() { _, err := c.Connect(context.Background()); result <- err }()
		waitFor(t, c, "pairing", func() bool { return c.pairing })
		c.Close()
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	})
	t.Run("no code in time, after a logout", func(t *testing.T) {
		qrWait = time.Millisecond
		c := newTestClient(t)
		old := c.current()
		c.event(&events.LoggedOut{})
		st, err := c.Connect(context.Background())
		if err != nil || st.QRPath != "" || !strings.Contains(st.Note, "waiting for QR") {
			t.Fatalf("%+v %v", st, err)
		}
		if c.current() == old || c.resetSession.Load() {
			t.Fatal("paired again with the device that logged out")
		}
	})
	t.Run("dial fails", func(t *testing.T) {
		n.dialErr = errors.New("fixture dial failure")
		defer func() { n.dialErr = nil }()
		c := newTestClient(t)
		if _, err := c.Connect(context.Background()); !errors.Is(err, n.dialErr) {
			t.Fatal(err)
		}
		if c.pairing {
			t.Fatal("still pairing")
		}
	})
	t.Run("device has an ID already", func(t *testing.T) {
		c := newTestClient(t)
		id := types.NewJID("1", types.DefaultUserServer)
		c.current().Store.ID = &id
		if _, err := c.Connect(context.Background()); !errors.Is(err, whatsmeow.ErrQRStoreContainsID) {
			t.Fatal(err)
		}
	})
}

// A dial that succeeds although the tool call ended in the meantime counts
// as canceled: the session ends with it.
func TestConnectSessionCanceledAfterDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var session context.Context
	_, err := connectSession(ctx, context.Background(), func(s context.Context) error {
		session = s
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || session.Err() == nil {
		t.Fatalf("err=%v session=%v", err, session.Err())
	}
}

// Codes and the last word of pairing do not wait for a Connect that is no
// longer waiting.
func TestQRDoesNotWaitForConnect(t *testing.T) {
	c := &client{path: t.TempDir()}
	ch := make(chan whatsmeow.QRChannelItem, 2)
	ready := make(chan struct{}, 1)
	ready <- struct{}{}
	ch <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: "harmless-fixture", Timeout: time.Minute}
	ch <- whatsmeow.QRChannelSuccess
	c.wg.Add(1)
	c.consumeQR(context.Background(), ch, ready)
	if !c.paired.Load() || len(ready) != 1 {
		t.Fatalf("paired %v, ready %d", c.paired.Load(), len(ready))
	}
}

// A QR channel that closes without a last word ends pairing. A code that
// cannot be made into a PNG, or not written, says so in the note.
func TestQRChannelClosesAndCodeFails(t *testing.T) {
	c := &client{path: t.TempDir()}
	ch := make(chan whatsmeow.QRChannelItem, 1)
	ready := make(chan struct{}, 1)
	c.pairing = true
	c.wg.Add(1)
	close(ch)
	c.consumeQR(context.Background(), ch, ready)
	if c.pairing {
		t.Fatal("still pairing")
	}
	for name, path := range map[string]string{"too long": c.path, "no directory": filepath.Join(c.path, "missing")} {
		c := &client{path: path}
		ctx, cancel := context.WithCancel(context.Background())
		ch := make(chan whatsmeow.QRChannelItem)
		c.wg.Add(1)
		go c.consumeQR(ctx, ch, ready)
		code := "harmless-fixture"
		if name == "too long" {
			code = strings.Repeat("x", 10000)
		}
		ch <- whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: code, Timeout: time.Minute}
		<-ready
		cancel()
		c.wg.Wait()
		if c.note != "Could not write private pairing PNG." || c.qrPath != "" {
			t.Fatalf("%s: %q %q", name, c.note, c.qrPath)
		}
	}
}

// Events of the socket set the note; history keeps only what can be read and
// is text; texts that cannot be kept make the status say so.
func TestEventsAndHistoryEdges(t *testing.T) {
	c := newTestClient(t)
	dispatch(c, &events.Connected{})
	if !c.paired.Load() || c.Status().Note != "Authenticated connection established." {
		t.Fatal(c.Status())
	}
	jid := types.NewJID("123", types.DefaultUserServer)
	group := types.NewJID("1-2", types.GroupServer)
	web := func(id string, fromMe bool, m *waE2E.Message) *waHistorySync.HistorySyncMsg {
		return &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{ID: proto.String(id), FromMe: proto.Bool(fromMe)}, MessageTimestamp: proto.Uint64(5), Message: m}}
	}
	text := &waE2E.Message{Conversation: proto.String("kept")}
	c.event(&events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{
		{ID: proto.String("1.2.3@s.whatsapp.net"), Messages: []*waHistorySync.HistorySyncMsg{web("bad chat", false, text)}},
		{ID: proto.String(group.String()), Messages: []*waHistorySync.HistorySyncMsg{web("no sender", false, text)}},
		{ID: proto.String(jid.String()), Messages: []*waHistorySync.HistorySyncMsg{web("mine, not logged in", true, text), web("no text", false, &waE2E.Message{})}},
	}}})
	for _, chat := range []string{jid.String(), group.String()} {
		if got, err := c.texts.read(chat, 10); err != nil || len(got) != 0 {
			t.Fatal(chat, got, err)
		}
	}
	c.texts.close()
	c.event(&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: jid, Sender: jid}, ID: "lost"}, Message: text})
	if !strings.Contains(c.Status().Note, "could not be persisted") {
		t.Fatal(c.Status())
	}
}

// Send takes an exact JID, sends with the current client and returns the
// acknowledged message; a failed send is an error.
func TestSend(t *testing.T) {
	c := newTestClient(t)
	if _, err := c.Send(context.Background(), "1.2.3@s.whatsapp.net", "hi"); err == nil {
		t.Fatal("sent to an invalid JID")
	}
	if _, err := c.Send(context.Background(), "123@s.whatsapp.net", "hi"); !errors.Is(err, whatsmeow.ErrNotLoggedIn) {
		t.Fatalf("sent without login: %v", err)
	}
	offline(t)
	m, err := c.Send(context.Background(), "123@s.whatsapp.net", "hi")
	if err != nil || m != (message{Chat: "123@s.whatsapp.net", ID: "ack", Text: "hi", Time: 7, FromMe: true}) {
		t.Fatalf("%+v %v", m, err)
	}
}
