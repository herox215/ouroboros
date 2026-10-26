package whatsapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	connectMu     sync.Mutex         // serializes Connect, which may wait for a QR
	cli           *whatsmeow.Client  // swapped under stateMu, see current
	stopSession   context.CancelFunc // of the last connected session
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
	c.watch()
	return c, nil
}

// watch takes the events of the current whatsmeow client. Each handler
// keeps its own client, so an event of a replaced one is parsed with the
// device store it came from. stateMu is not held.
func (c *client) watch() {
	cli := c.current()
	cli.InitialAutoReconnect = false
	handler := cli.AddEventHandler(func(raw any) { c.eventOf(cli, raw) })
	c.stateMu.Lock()
	c.handler = handler
	c.stateMu.Unlock()
}

// current is the whatsmeow client in use.
func (c *client) current() *whatsmeow.Client {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.cli
}
func (c *client) Status() status {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	st := status{Connected: c.cli.IsConnected() && c.cli.IsLoggedIn(), Paired: c.paired.Load(), Note: c.note}
	if time.Now().Before(c.qrExpires) {
		st.QRPath = c.qrPath
	}
	if c.storageFailed.Load() {
		st.Note = strings.TrimSpace(st.Note + " Some received/history text could not be persisted; local history is incomplete.")
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
	c.connectMu.Lock()
	defer c.connectMu.Unlock()
	if err := c.ctx.Err(); err != nil {
		return status{}, err
	}
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
		old := c.current()
		old.Disconnect()
		c.stateMu.Lock()
		old.RemoveEventHandler(c.handler)
		c.cli = whatsmeow.NewClient(c.devices.NewDevice(), nil)
		c.stateMu.Unlock()
		c.watch()
	}
	cli := c.current()
	if !c.paired.Load() {
		// A QR channel must be installed before connecting. Its context outlives
		// the tool call so codes can rotate while the owner scans locally.
		qrCtx, cancel := context.WithCancel(c.ctx)
		c.qrCancel = cancel
		ch, err := cli.GetQRChannel(qrCtx)
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
		if err = c.connect(ctx, cli); err != nil {
			cancel()
			cli.Disconnect()
			c.wg.Wait()
			return status{}, err
		}
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		select {
		case <-ready:
		case <-ctx.Done():
			cancel()
			cli.Disconnect()
			c.wg.Wait()
			return status{}, ctx.Err()
		case <-c.ctx.Done(): // Close waits for Connect
		case <-timer.C:
		}
		return c.Status(), nil
	}
	if cli.IsConnected() {
		// Right after pairing, or while whatsmeow reconnects, the socket is
		// open but not logged in yet.
		c.stateMu.Lock()
		c.note = "Connected; authentication is still in progress."
		c.stateMu.Unlock()
		return c.Status(), nil
	}
	if err := c.connect(ctx, cli); err != nil {
		return status{}, err
	}
	c.stateMu.Lock()
	c.note = "Connection requested; authentication may still be in progress."
	c.stateMu.Unlock()
	return c.Status(), nil
}

// connect connects cli in a session of its own, see connectSession, and
// ends the session of the last connect, whose socket is gone. A socket that
// opened meanwhile counts as connected. connectMu is held.
func (c *client) connect(ctx context.Context, cli *whatsmeow.Client) error {
	stop, err := connectSession(ctx, c.ctx, cli.ConnectContext)
	if errors.Is(err, whatsmeow.ErrAlreadyConnected) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.stopSession != nil {
		c.stopSession()
	}
	c.stopSession = stop
	return nil
}

// connectSession lets the caller cancel the initial dial without tying the
// authenticated socket and whatsmeow's reconnect loop to a finished tool call.
// A successful session remains a child of the client's lifetime until the
// returned function ends it, or Close does.
func connectSession(ctx, lifetime context.Context, connect func(context.Context) error) (context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session, cancel := context.WithCancel(lifetime)
	stop := context.AfterFunc(ctx, cancel)
	err := connect(session)
	stopped := stop()
	if err != nil {
		cancel()
		return nil, err
	}
	if !stopped {
		cancel()
		return nil, ctx.Err()
	}
	return cancel, nil
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
func (c *client) event(raw any) { c.eventOf(c.current(), raw) }

// eventOf handles raw, an event of cli.
func (c *client) eventOf(cli *whatsmeow.Client, raw any) {
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
		if m, ok := retained(e); ok {
			c.save(m)
		}
	case *events.HistorySync:
		// One transaction for the whole sync: it can bring thousands of
		// messages, and blocks whatsmeow's event dispatch while it is saved.
		var all []message
		for _, conv := range e.Data.GetConversations() {
			jid, err := types.ParseJID(conv.GetID())
			if err != nil {
				continue
			}
			for _, entry := range conv.GetMessages() {
				parsed, err := cli.ParseWebMessage(jid, entry.GetMessage())
				if err != nil || parsed == nil {
					continue
				}
				if m, ok := retained(parsed); ok {
					all = append(all, m)
				}
			}
		}
		c.save(all...)
	}
}

// retained is the text of e that is kept, if any.
func retained(e *events.Message) (message, bool) {
	// Do not retain disappearing/view-once content in a permanent text store.
	if e.IsEphemeral || e.IsViewOnce || e.IsViewOnceV2 || e.IsViewOnceV2Extension {
		return message{}, false
	}
	text := e.Message.GetConversation()
	if text == "" {
		text = e.Message.GetExtendedTextMessage().GetText()
	}
	if text == "" {
		return message{}, false
	}
	return message{Chat: e.Info.Chat.String(), ID: e.Info.ID, Sender: e.Info.Sender.String(), Text: text, Time: e.Info.Timestamp.Unix(), FromMe: e.Info.IsFromMe}, true
}
func (c *client) save(messages ...message) {
	if len(messages) == 0 {
		return
	}
	if err := c.texts.put(messages...); err != nil {
		c.storageFailed.Store(true)
	}
}
func (c *client) Send(ctx context.Context, jid, text string) (message, error) {
	to, err := types.ParseJID(jid)
	if err != nil {
		return message{}, errors.New("invalid JID")
	}
	resp, err := c.current().SendMessage(ctx, to, &waE2E.Message{Conversation: proto.String(text)})
	if err != nil {
		return message{}, err
	}
	return message{Chat: jid, ID: resp.ID, Text: text, Time: resp.Timestamp.Unix(), FromMe: true}, nil
}
func (c *client) Close() {
	c.cancel()
	c.connectMu.Lock() // a Connect ends once the lifetime does
	defer c.connectMu.Unlock()
	if c.qrCancel != nil {
		c.qrCancel()
	}
	cli := c.current()
	cli.Disconnect()
	c.wg.Wait()
	c.stateMu.Lock()
	cli.RemoveEventHandler(c.handler)
	c.stateMu.Unlock()
	c.clearQR()
	_ = c.devices.Close()
}
