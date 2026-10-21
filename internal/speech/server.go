package speech

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const serverReadyTimeout = 2 * time.Minute

var errClosed = errors.New("speech manager is closed")

type localServer struct {
	cmd    *exec.Cmd
	done   chan struct{}
	model  string
	public string
	base   string
	client *http.Client
}

// Close cancels local work, kills/reaps the managed process and permanently
// closes its local backend. Remote requests and settings/download workers have
// independent lifetimes.
func (m *Manager) Close() error {
	m.closeOnce.Do(func() { close(m.closed) })
	m.localGate <- struct{}{}
	defer func() { <-m.localGate }()
	m.stopServer()
	return nil
}

func (m *Manager) stopServer() {
	if s := m.server; s != nil {
		select {
		case <-s.done:
		default:
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		}
		<-s.done
		s.client.CloseIdleConnections()
		_ = os.RemoveAll(s.public)
		m.server = nil
	}
}

// acquireLocal is cancellable even while another transcription owns the model.
func (m *Manager) acquireLocal(ctx context.Context) (context.Context, func(), error) {
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-m.closed:
		return nil, nil, errClosed
	case m.localGate <- struct{}{}:
	}
	select {
	case <-m.closed:
		<-m.localGate
		return nil, nil, errClosed
	default:
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		select {
		case <-m.closed:
			cancel()
		case <-done:
		}
	}()
	return ctx, func() { close(done); cancel(); <-m.localGate }, nil
}

func (m *Manager) startServer(ctx context.Context, bin, model string) error {
	if m.server != nil {
		select {
		case <-m.server.done:
			m.stopServer()
		default:
			if m.server.model == model {
				return nil
			}
			m.stopServer()
		}
	}
	// Upstream v1.8.2 has no authentication option. A fresh 256-bit request
	// prefix protects all API routes (including /load) against blind localhost
	// and browser requests. It is not a boundary against same-user processes.
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return err
	}
	prefix := "/" + hex.EncodeToString(secret[:])
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("cannot allocate local speech port")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close() // upstream cannot inherit a listener; bind failure is bounded below
	root, err := filepath.Abs(m.root)
	if err != nil {
		return err
	}
	// Never serve upstream's default public folder from the node's working tree.
	public, err := os.MkdirTemp(root, "server-")
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--request-path", prefix, "--public", public, "-m", model)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = cmd.Start(); err != nil {
		_ = os.RemoveAll(public)
		return errors.New("cannot start whisper-server")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 0 // inference may take minutes
	s := &localServer{cmd: cmd, done: make(chan struct{}), model: model, public: public,
		base:   fmt.Sprintf("http://127.0.0.1:%d%s", port, prefix),
		client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	m.server = s
	go func() { _ = cmd.Wait(); close(s.done) }()
	readyCtx, cancel := context.WithTimeout(ctx, serverReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		probeCtx, stop := context.WithTimeout(readyCtx, time.Second)
		req, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, s.base+"/health", nil)
		resp, e := s.client.Do(req)
		ready := false
		if e == nil {
			var health struct {
				Status string `json:"status"`
			}
			ready = resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&health) == nil && health.Status == "ok"
			resp.Body.Close()
		}
		stop()
		if ready {
			return nil
		}
		select {
		case <-readyCtx.Done():
			m.stopServer()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("whisper-server readiness timed out")
		case <-s.done:
			m.stopServer()
			return errors.New("whisper-server exited before readiness")
		case <-ticker.C:
		}
	}
}

func (m *Manager) infer(ctx context.Context, wav, language string) (string, error) {
	audio, err := os.ReadFile(wav)
	if err != nil {
		return "", errors.New("cannot read converted audio")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", err
	}
	if _, err = part.Write(audio); err != nil {
		return "", err
	}
	if language == "" {
		language = "auto"
	}
	if err = writer.WriteField("language", language); err != nil {
		return "", err
	}
	if err = writer.WriteField("response_format", "json"); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.server.base+"/inference", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := m.server.client.Do(req)
	if err != nil {
		// Disconnect does not interrupt upstream inference. Kill/reap before another
		// request can reuse this model; normal successful requests keep it resident.
		m.stopServer()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("local speech request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		m.stopServer()
		return "", fmt.Errorf("local speech returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxText+1))
	if err != nil {
		m.stopServer()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("cannot read local transcript")
	}
	if len(b) > maxText {
		return "", errors.New("local transcript exceeds size limit")
	}
	var result struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(b, &result) != nil || result.Text == nil {
		return "", errors.New("invalid local speech response")
	}
	return strings.TrimSpace(*result.Text), nil
}
