// Package speech manages profile-local speech settings and explicitly requested
// whisper.cpp downloads. Constructing a Manager never accesses the network.
package speech

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"ouroboros/internal/safefile"
)

type Config struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"api_key,omitempty"`
	Language string `json:"language"`
}

type Model struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Bytes int64  `json:"bytes"`
}

type Snapshot struct {
	Config       Config   `json:"config"`
	HasKey       bool     `json:"has_key"`
	Models       []Model  `json:"models"`
	Status       string   `json:"status"`
	RuntimeReady bool     `json:"runtime_ready"`
	ModelReady   bool     `json:"model_ready"`
	Download     Progress `json:"download"`
}

type Progress struct {
	Active   bool   `json:"active"`
	Phase    string `json:"phase"`
	Received int64  `json:"received"`
	Total    int64  `json:"total"`
	Error    string `json:"error,omitempty"`
}

type modelSpec struct {
	Model
	sha string
}

// Hashes and sizes are the upstream Hugging Face LFS SHA-256 metadata.
var catalog = []modelSpec{
	{Model{"large-v3-turbo-q5_0", "Large v3 Turbo (Q5_0)", 574041195}, "394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2"},
	{Model{"small", "Small", 487601967}, "1be3a9b2063867b937e64e2ec7483364a79917e157fa98c5d94b5c1fffea987b"},
	{Model{"medium", "Medium", 1533763059}, "6c14d5adee5f86394037b4e4e8b59f1673b6cee10e3cf0b11bbdbee79c156208"},
}

// Manager is safe to use concurrently, including from different processes for
// the same root. Filesystem locks coordinate settings, progress and cancellation.
type Manager struct {
	root      string
	client    *http.Client
	models    []modelSpec
	modelBase string
	sourceURL string

	localGate chan struct{} // serializes conversion, requests and process replacement
	closed    chan struct{}
	closeOnce sync.Once
	server    *localServer // guarded by localGate
}

func New(root string) *Manager {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &Manager{root: root, localGate: make(chan struct{}, 1), closed: make(chan struct{}), client: &http.Client{Timeout: 30 * time.Minute, Transport: transport},
		models:    append([]modelSpec(nil), catalog...),
		modelBase: "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-",
		// whisper.cpp v1.8.2, pinned to the release commit rather than a moving tag.
		sourceURL: "https://codeload.github.com/ggml-org/whisper.cpp/tar.gz/4979e04f5dcaccb36057e059bbaed8a2f5288315"}
}

func (m *Manager) path(name string) string { return filepath.Join(m.root, name) }
func (m *Manager) lock() (func(), error) {
	if err := os.MkdirAll(m.root, 0700); err != nil {
		return nil, err
	}
	return safefile.Lock(m.path("state.lock"))
}
func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return safefile.Write(path, b, 0600)
}
func readJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, f, 64<<10)).Decode(v)
}
func (m *Manager) config() (Config, error) {
	c := Config{Provider: "disabled", Model: catalog[0].ID, Endpoint: "https://api.openai.com/v1", Language: "auto"}
	err := readJSON(m.path("config.json"), &c)
	if os.IsNotExist(err) {
		err = nil
	}
	return c, err
}
func (m *Manager) spec(id string) (modelSpec, bool) {
	for _, s := range m.models {
		if s.ID == id {
			return s, true
		}
	}
	return modelSpec{}, false
}
func validEndpoint(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}
func (m *Manager) Save(c Config) error {
	if c.Provider != "disabled" && c.Provider != "local" && c.Provider != "remote" {
		return errors.New("invalid speech provider")
	}
	if c.Model == "" {
		c.Model = catalog[0].ID
	}
	if c.Provider != "remote" {
		if _, ok := m.spec(c.Model); !ok {
			return errors.New("unknown local speech model")
		}
	}
	if len(c.Model) > 200 || strings.ContainsAny(c.Model, "\r\n") {
		return errors.New("invalid speech model")
	}
	if len(c.Endpoint) > 4096 || (c.Endpoint != "" && !validEndpoint(c.Endpoint)) {
		return errors.New("invalid speech endpoint: use an HTTP(S) base URL without credentials or query")
	}
	if c.Provider == "remote" && c.Endpoint == "" {
		return errors.New("speech endpoint is required")
	}
	if len(c.Language) > 32 || strings.ContainsAny(c.Language, "\r\n") {
		return errors.New("invalid speech language")
	}
	if len(c.APIKey) > 8192 || strings.ContainsAny(c.APIKey, "\r\n") {
		return errors.New("invalid speech API key")
	}
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	old, err := m.config()
	if err != nil {
		return err
	}
	if c.APIKey == "" {
		c.APIKey = old.APIKey
	}
	return writeJSON(m.path("config.json"), c)
}

// downloadLock must be acquired only while state.lock is held. It is never
// unlinked: the descriptor remains locked until the worker finishes or dies.
func (m *Manager) downloadLock() (*os.File, error) {
	f, err := os.OpenFile(m.path("download.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func (m *Manager) progress() (Progress, error) {
	var p Progress
	err := readJSON(m.path("download.json"), &p)
	if os.IsNotExist(err) {
		err = nil
	}
	return p, err
}
func lookCommand(name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("local speech requires %s on PATH", name)
	}
	return p, nil
}

func atomicExecutable(path string, data []byte) error { return safefile.Write(path, data, 0700) }

func (m *Manager) runtime() string       { return m.executable("whisper-cli") }
func (m *Manager) serverRuntime() string { return m.executable("whisper-server") }
func (m *Manager) executable(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	p := m.path("runtime/" + name)
	if s, err := os.Stat(p); err == nil && s.Mode().IsRegular() && s.Mode().Perm()&0111 != 0 {
		return p
	}
	return ""
}
func (m *Manager) modelReady(s modelSpec) bool {
	st, err := os.Stat(m.path("ggml-" + s.ID + ".bin"))
	return err == nil && st.Mode().IsRegular() && st.Size() == s.Bytes
}
func (m *Manager) Snapshot() (Snapshot, error) {
	unlock, err := m.lock()
	if err != nil {
		return Snapshot{}, err
	}
	defer unlock()
	c, err := m.config()
	if err != nil {
		return Snapshot{}, err
	}
	p, err := m.progress()
	if err != nil {
		return Snapshot{}, err
	}
	if p.Active {
		f, e := m.downloadLock()
		if e == nil {
			f.Close()
			p.Active = false
			p.Error = "speech preparation interrupted"
			p.Phase = "error"
		} else if !errors.Is(e, syscall.EWOULDBLOCK) {
			return Snapshot{}, e
		}
	}
	s := Snapshot{Config: c, HasKey: c.APIKey != "", Download: p}
	s.Config.APIKey = ""
	for _, v := range m.models {
		s.Models = append(s.Models, v.Model)
	}
	spec, ok := m.spec(c.Model)
	s.ModelReady = ok && m.modelReady(spec)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtime := m.runtime()
	s.RuntimeReady = runtime != "" && m.serverRuntime() != "" && command(ctx, runtime, "--help") == nil && command(ctx, m.serverRuntime(), "--help") == nil
	ff, err := exec.LookPath("ffmpeg")
	ffReady := err == nil && command(ctx, ff, "-version") == nil
	s.RuntimeReady = s.RuntimeReady && ffReady
	switch {
	case c.Provider == "disabled":
		s.Status = "disabled"
	case c.Provider == "remote":
		s.Status = "remote" // configured, not a connectivity claim
	case p.Active:
		s.Status = "downloading"
	case p.Error != "":
		s.Status = "error"
	case !ffReady:
		s.Status = "missing_ffmpeg"
	case !s.RuntimeReady:
		s.Status = "missing_runtime"
	case !s.ModelReady:
		s.Status = "missing_model"
	default:
		s.Status = "ready"
	}
	return s, nil
}

func (m *Manager) StartDownload() error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	c, err := m.config()
	if err != nil {
		return err
	}
	if c.Provider != "local" {
		return errors.New("speech setup requires the local provider")
	}
	spec, ok := m.spec(c.Model)
	if !ok {
		return errors.New("unknown local speech model")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return errors.New("local speech requires ffmpeg on PATH")
	}
	if m.runtime() == "" || m.serverRuntime() == "" {
		if _, err := exec.LookPath("cmake"); err != nil {
			return errors.New("building whisper.cpp requires cmake on PATH")
		}
		if _, err := exec.LookPath("c++"); err != nil {
			return errors.New("building whisper.cpp requires a C++ compiler (c++) on PATH")
		}
		if _, err := exec.LookPath("cc"); err != nil {
			return errors.New("building whisper.cpp requires a C compiler (cc) on PATH")
		}
	}
	f, err := m.downloadLock()
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errors.New("speech download already active")
	}
	if err != nil {
		return err
	}
	if err = os.Remove(m.path("cancel")); err != nil && !os.IsNotExist(err) {
		f.Close()
		return err
	}
	if err = writeJSON(m.path("download.json"), Progress{Active: true, Phase: "starting"}); err != nil {
		f.Close()
		return err
	}
	go m.prepare(f, spec)
	return nil
}
func (m *Manager) CancelDownload() error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	f, err := m.downloadLock()
	if err == nil {
		f.Close()
		return nil
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		return err
	}
	return safefile.Write(m.path("cancel"), []byte("cancel\n"), 0600)
}

func (m *Manager) setProgress(p Progress) error {
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return writeJSON(m.path("download.json"), p)
}

// command kills the complete process group on cancellation, including compiler
// and ffmpeg children. Command output is deliberately discarded: it can contain
// filenames, server responses or credentials and must not become public errors.
func command(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("speech command %s failed", filepath.Base(name))
	}
	return nil
}
