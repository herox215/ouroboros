package whatsapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type client struct {
	cli           *whatsmeow.Client
	devices       *sqlstore.Container
	texts         *textStore
	path          string
	ctx           context.Context
	cancel        context.CancelFunc
	qrCancel      context.CancelFunc
	wg            sync.WaitGroup
	stateMu       sync.Mutex
	qrPath        string
	qrExpires     time.Time
	note          string
	pairing       bool
	paired        atomic.Bool
	storageFailed atomic.Bool
	resetSession  atomic.Bool
	handler       uint32
}

func newClient(ctx context.Context, path string, texts *textStore) (backend, error) {
	db, err := privateDB(filepath.Join(path, "session.db"))
	if err != nil {
		return nil, err
	}
	devices := sqlstore.NewWithDB(db, "sqlite3", nil)
	if err = devices.Upgrade(ctx); err != nil {
		_ = devices.Close()
		return nil, err
	}
	device, err := devices.GetFirstDevice(ctx)
	if err != nil {
		_ = devices.Close()
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	c := &client{cli: whatsmeow.NewClient(device, nil), devices: devices, texts: texts, path: path, ctx: lifetime, cancel: cancel}
	c.paired.Store(device.ID != nil)
	c.cli.InitialAutoReconnect = false
	c.handler = c.cli.AddEventHandler(c.event)
	return c, nil
}
func (c *client) Status() status {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	st := status{Connected: c.cli.IsConnected() && c.cli.IsLoggedIn(), Paired: c.paired.Load(), Note: c.note}
	if time.Now().Before(c.qrExpires) {
		st.QRPath = c.qrPath
	}
	if c.storageFailed.Load() {
		st.Note = "Some received/history text could not be persisted; local history is incomplete."
	}
	return st
}
func (c *client) clearQR() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	_ = os.Remove(filepath.Join(c.path, "pairing.png"))
	c.qrPath = ""
	c.qrExpires = time.Time{}
}
func (c *client) Connect(ctx context.Context) (status, error) {
	if c.Status().Connected {
		return c.Status(), nil
	}
	c.stateMu.Lock()
	pairing := c.pairing
	c.stateMu.Unlock()
	if pairing {
		return c.Status(), nil
	}
	if c.qrCancel != nil {
		c.qrCancel()
		c.wg.Wait()
	}
	c.clearQR()
	if c.resetSession.Swap(false) {
		c.cli.Disconnect()
		c.cli.RemoveEventHandler(c.handler)
		c.cli = whatsmeow.NewClient(c.devices.NewDevice(), nil)
		c.cli.InitialAutoReconnect = false
		c.handler = c.cli.AddEventHandler(c.event)
	}
	if !c.paired.Load() {
		// A QR channel must be installed before connecting. Its context outlives
		// the tool call so codes can rotate while the owner scans locally.
		qrCtx, cancel := context.WithCancel(c.ctx)
		c.qrCancel = cancel
		ch, err := c.cli.GetQRChannel(qrCtx)
		if err != nil {
			cancel()
			return status{}, err
		}
		ready := make(chan struct{}, 1)
		c.stateMu.Lock()
		c.pairing = true
		c.note = "Pairing requested; waiting for QR."
		c.stateMu.Unlock()
		c.wg.Add(1)
		go c.consumeQR(qrCtx, ch, ready)
		if err = connectSession(ctx, c.ctx, c.cli.ConnectContext); err != nil {
			cancel()
			c.cli.Disconnect()
			c.wg.Wait()
			return status{}, err
		}
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		select {
		case <-ready:
		case <-ctx.Done():
			cancel()
			c.cli.Disconnect()
			c.wg.Wait()
			return status{}, ctx.Err()
		case <-timer.C:
		}
		return c.Status(), nil
	}
	if err := connectSession(ctx, c.ctx, c.cli.ConnectContext); err != nil {
		return status{}, err
	}
	c.stateMu.Lock()
	c.note = "Connection requested; authentication may still be in progress."
	c.stateMu.Unlock()
	return c.Status(), nil
}

// connectSession lets the caller cancel the initial dial without tying the
// authenticated socket and whatsmeow's reconnect loop to a finished tool call.
// Successful sessions remain children of the client's lifetime until Close.
func connectSession(ctx, lifetime context.Context, connect func(context.Context) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	session, cancel := context.WithCancel(lifetime)
	stop := context.AfterFunc(ctx, cancel)
	err := connect(session)
	stopped := stop()
	if err != nil {
		cancel()
		return err
	}
	if !stopped {
		cancel()
		return ctx.Err()
	}
	return nil
}

func (c *client) consumeQR(ctx context.Context, ch <-chan whatsmeow.QRChannelItem, ready chan<- struct{}) {
	defer c.wg.Done()
	defer func() { c.clearQR(); c.stateMu.Lock(); c.pairing = false; c.stateMu.Unlock() }()
	var timer *time.Timer
	var expiry <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry:
			c.clearQR()
			expiry = nil
		case item, ok := <-ch:
			if !ok {
				return
			}
			if item.Event == whatsmeow.QRChannelEventCode {
				c.clearQR()
				png, err := qrcode.Encode(item.Code, qrcode.Medium, 512)
				if err == nil {
					err = c.writeQR(png)
				}
				c.stateMu.Lock()
				if err == nil {
					c.qrPath = filepath.Join(c.path, "pairing.png")
					c.qrExpires = time.Now().Add(item.Timeout)
					c.note = "Scan the private local PNG with WhatsApp Linked devices. QR rotates and expires; never share it."
				} else {
					c.note = "Could not write private pairing PNG."
				}
				c.stateMu.Unlock()
				if timer != nil {
					timer.Stop()
				}
				timer = time.NewTimer(item.Timeout)
				expiry = timer.C
				select {
				case ready <- struct{}{}:
				default:
				}
			} else {
				c.stateMu.Lock()
				if item.Event == "success" {
					c.paired.Store(true)
					c.note = "Paired; waiting for authenticated connection."
				} else {
					c.note = "Pairing ended without success; retry explicit connect."
				}
				c.stateMu.Unlock()
				select {
				case ready <- struct{}{}:
				default:
				}
				return
			}
		}
	}
}
func (c *client) writeQR(png []byte) error {
	f, err := os.CreateTemp(c.path, ".pairing-*.png")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(png); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(c.path, "pairing.png"))
}
func (c *client) event(raw any) {
	switch e := raw.(type) {
	case *events.Connected:
		c.paired.Store(true)
		c.clearQR()
		c.stateMu.Lock()
		c.note = "Authenticated connection established."
		c.stateMu.Unlock()
	case *events.LoggedOut:
		c.resetSession.Store(true)
		c.paired.Store(false)
		c.clearQR()
		c.stateMu.Lock()
		c.note = "Logged out; explicit connect is needed to pair again."
		c.stateMu.Unlock()
	case *events.Disconnected:
		c.stateMu.Lock()
		if c.paired.Load() && c.ctx.Err() == nil {
			c.note = "Connection lost; automatically reconnecting with the saved pairing."
		} else {
			c.note = "Disconnected; explicit connect is needed to pair again."
		}
		c.stateMu.Unlock()
	case *events.Message:
		c.save(e)
	case *events.HistorySync:
		for _, conv := range e.Data.GetConversations() {
			jid, err := types.ParseJID(conv.GetID())
			if err != nil {
				continue
			}
			for _, entry := range conv.GetMessages() {
				m, err := c.cli.ParseWebMessage(jid, entry.GetMessage())
				if err == nil && m != nil {
					c.save(m)
				}
			}
		}
	}
}
func (c *client) save(e *events.Message) {
	// Do not retain disappearing/view-once content in a permanent text store.
	if e.IsEphemeral || e.IsViewOnce || e.IsViewOnceV2 || e.IsViewOnceV2Extension {
		return
	}
	text := e.Message.GetConversation()
	if text == "" {
		text = e.Message.GetExtendedTextMessage().GetText()
	}
	if text == "" {
		return
	}
	m := message{Chat: e.Info.Chat.String(), ID: e.Info.ID, Sender: e.Info.Sender.String(), Text: text, Time: e.Info.Timestamp.Unix(), FromMe: e.Info.IsFromMe}
	if err := c.texts.put(m); err != nil {
		c.storageFailed.Store(true)
	}
}
func (c *client) Send(ctx context.Context, jid, text string) (message, error) {
	to, err := types.ParseJID(jid)
	if err != nil {
		return message{}, errors.New("invalid JID")
	}
	resp, err := c.cli.SendMessage(ctx, to, &waE2E.Message{Conversation: proto.String(text)})
	if err != nil {
		return message{}, err
	}
	return message{Chat: jid, ID: resp.ID, Text: text, Time: resp.Timestamp.Unix(), FromMe: true}, nil
}
func (c *client) Close() {
	c.cancel()
	if c.qrCancel != nil {
		c.qrCancel()
	}
	c.cli.Disconnect()
	c.wg.Wait()
	c.cli.RemoveEventHandler(c.handler)
	c.clearQR()
	_ = c.devices.Close()
}
