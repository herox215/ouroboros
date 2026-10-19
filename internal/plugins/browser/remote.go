package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsPipe adapts message-framed CDP to the existing zero-terminated pipe
// transport. The writer is serialized by conn; reads have one goroutine.
type wsPipe struct {
	ws   *websocket.Conn
	rest []byte
}

func (w *wsPipe) Read(p []byte) (int, error) {
	if len(w.rest) == 0 {
		_, data, err := w.ws.ReadMessage()
		if err != nil {
			return 0, err
		}
		w.rest = append(data, 0)
	}
	n := copy(p, w.rest)
	w.rest = w.rest[n:]
	return n, nil
}
func (w *wsPipe) Write(p []byte) (int, error) {
	if err := w.ws.WriteMessage(websocket.TextMessage, p[:len(p)-1]); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (w *wsPipe) Close() error { return w.ws.Close() }

func connectCDP(ctx context.Context, s settings) (*process, error) {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	endpoint, _ := url.Parse(s.Endpoint) // validated settings
	headers := http.Header{}
	if s.Token != "" {
		headers.Set("Authorization", "Bearer "+s.Token)
	}
	if endpoint.Scheme == "http" || endpoint.Scheme == "https" {
		discovery := *endpoint
		if !strings.HasSuffix(discovery.Path, "/json/version") {
			discovery.Path = strings.TrimRight(discovery.Path, "/") + "/json/version"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, discovery.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header = headers.Clone()
		resp, err := remoteHTTP().Do(req)
		if err != nil {
			return nil, errors.New("CDP HTTP discovery failed (check endpoint and connectivity)")
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		resp.Body.Close()
		if err != nil || len(data) > 1<<20 {
			return nil, errors.New("invalid or oversized CDP discovery response")
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("CDP discovery returned HTTP %d", resp.StatusCode)
		}
		var found struct {
			WebSocket string `json:"webSocketDebuggerUrl"`
		}
		if json.Unmarshal(data, &found) != nil || found.WebSocket == "" {
			return nil, errors.New("CDP discovery lacks webSocketDebuggerUrl; Camofox needs provider camofox, not cdp")
		}
		ws, err := url.Parse(found.WebSocket)
		if err != nil || ws.Host == "" || ws.User != nil || ws.Fragment != "" || (ws.Scheme != "ws" && ws.Scheme != "wss") {
			return nil, errors.New("invalid CDP webSocketDebuggerUrl")
		}
		// Discovery must stay at the configured host. In particular, do not use
		// a loopback/internal hostname advertised by a container or hosted service.
		// Operators can supply the external ws/wss browser URL directly instead.
		if !strings.EqualFold(ws.Host, endpoint.Host) {
			return nil, errors.New("CDP discovery advertised a different host; configure its reachable ws/wss browser endpoint explicitly")
		}
		if endpoint.Scheme == "https" && ws.Scheme != "wss" {
			return nil, errors.New("CDP discovery would downgrade TLS")
		}
		endpoint = ws
	}
	d := websocket.Dialer{HandshakeTimeout: startTimeout}
	ws, resp, err := d.DialContext(ctx, endpoint.String(), headers)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return nil, errors.New("CDP WebSocket connection failed (check browser endpoint and authentication)")
	}
	ws.SetReadLimit(maxResponse)
	pipe := &wsPipe{ws: ws}
	c := newConn(pipe, pipe)
	// Each write has the command's deadline and cancellation closes the
	// transport, unblocking writes without leaving an ambiguous live command.
	c.write = func(ctx context.Context, data []byte) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			deadline = time.Now().Add(requestTimeout)
		}
		ws.SetWriteDeadline(deadline)
		stop := context.AfterFunc(ctx, func() { ws.Close() })
		defer stop()
		_, err := pipe.Write(data)
		return err
	}
	p := &process{conn: c, remote: true}
	var once sync.Once
	p.end = func() {
		once.Do(func() {
			cleanup, cancel := context.WithTimeout(context.Background(), closeTimeout)
			defer cancel()
			p.ownedMu.Lock()
			for id := range p.owned {
				p.call(cleanup, "", "Target.disposeBrowserContext", map[string]any{"browserContextId": id}, nil)
			}
			p.ownedMu.Unlock()
			// No Browser.close: this process and all pre-existing targets are hosted.
			ws.Close()
		})
	}
	var version struct {
		Product string `json:"product"`
	}
	if err := p.call(ctx, "", "Browser.getVersion", nil, &version); err != nil {
		p.end()
		return nil, fmt.Errorf("remote Chromium CDP handshake failed (Firefox/Camofox is not CDP compatible): %w", err)
	}
	if strings.Contains(strings.ToLower(version.Product), "firefox") {
		p.end()
		return nil, errors.New("the Firefox browser is not a supported CDP backend; use the native Camofox REST API")
	}
	return p, nil
}
