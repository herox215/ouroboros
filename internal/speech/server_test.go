package speech

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real subprocess/HTTP fixture for the pinned server's argument and multipart
// contract. Recognition is intentionally stubbed, not a production model test.
func TestWhisperServerProcess(t *testing.T) {
	args := os.Args
	index := -1
	for i, arg := range args {
		if arg == "--" {
			index = i + 1
			break
		}
	}
	if index < 0 {
		return
	}
	values := map[string]string{}
	for i := index; i+1 < len(args); i += 2 {
		values[args[i]] = args[i+1]
	}
	if values["--host"] != "127.0.0.1" || len(values["--request-path"]) != 65 || values["-m"] == "" {
		os.Exit(2)
	}
	if marker := os.Getenv("SPEECH_SERVER_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte("called"), 0600)
	}
	mux := http.NewServeMux()
	prefix := values["--request-path"]
	mux.HandleFunc(prefix+"/health", func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("SPEECH_SERVER_LOADING") != "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.HandleFunc(prefix+"/inference", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, h, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(400)
			return
		}
		defer file.Close()
		b, _ := io.ReadAll(file)
		if len(b) < 12 || string(b[:4]) != "RIFF" || h.Filename != "audio.wav" || r.FormValue("language") == "" || r.FormValue("response_format") != "json" {
			w.WriteHeader(400)
			return
		}
		if marker := os.Getenv("SPEECH_INFER_MARKER"); marker != "" {
			_ = os.WriteFile(marker, []byte("called"), 0600)
		}
		if os.Getenv("SPEECH_SERVER_BLOCK") != "" {
			time.Sleep(time.Minute)
		}
		text := os.Getenv("SPEECH_SERVER_TEXT")
		if text == "" {
			text = " fixture transcript \n"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": text})
	})
	listener, err := net.Listen("tcp4", net.JoinHostPort(values["--host"], values["--port"]))
	if err != nil {
		os.Exit(3)
	}
	_ = http.Serve(listener, mux)
	os.Exit(0)
}

func localFixture(t *testing.T) *Manager {
	t.Helper()
	commands(t, true)
	m := New(t.TempDir())
	b := fixture(m)
	saveLocal(t, m)
	if err := os.WriteFile(m.path("ggml-"+m.models[0].ID+".bin"), b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestPersistentServerReuseAndModelSwitch(t *testing.T) {
	m := localFixture(t)
	transcribe := func() {
		t.Helper()
		text, err := m.Transcribe(context.Background(), "a.ogg", "audio/ogg", []byte("fixture"))
		if err != nil || text != "fixture transcript" {
			t.Fatalf("transcribe %q: %v", text, err)
		}
	}
	transcribe()
	first := m.server
	transcribe()
	if m.server != first {
		t.Fatal("server not reused")
	}
	// Several callers must all finish with the same process/model.
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() {
			text, err := m.Transcribe(context.Background(), "a", "audio/ogg", []byte("fixture"))
			if err == nil && text != "fixture transcript" {
				err = fmt.Errorf("text %q", text)
			}
			errs <- err
		}()
	}
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if m.server != first {
		t.Fatal("concurrent calls replaced process")
	}
	next := m.models[0]
	next.ID = "small"
	m.models = append(m.models, next)
	b, _ := os.ReadFile(m.path("ggml-" + m.models[0].ID + ".bin"))
	if err := os.WriteFile(m.path("ggml-small.bin"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(Config{Provider: "local", Model: "small"}); err != nil {
		t.Fatal(err)
	}
	transcribe()
	if m.server == first || !strings.HasSuffix(m.server.model, "ggml-small.bin") {
		t.Fatal("model not replaced")
	}
	select {
	case <-first.done:
	default:
		t.Fatal("old process not reaped")
	}
	last := m.server
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-last.done:
	default:
		t.Fatal("Close did not reap server")
	}
	if _, err := m.Transcribe(context.Background(), "a", "audio/ogg", []byte("fixture")); !errors.Is(err, errClosed) {
		t.Fatalf("closed accepted work: %v", err)
	}
}

func TestServerCancellationAndClose(t *testing.T) {
	for _, mode := range []string{"cancel", "close"} {
		t.Run(mode, func(t *testing.T) {
			m := localFixture(t)
			marker := filepath.Join(t.TempDir(), "inference")
			t.Setenv("SPEECH_INFER_MARKER", marker)
			t.Setenv("SPEECH_SERVER_BLOCK", "1")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := m.Transcribe(ctx, "a", "audio/ogg", []byte("fixture")); result <- err }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("inference not started")
				}
				time.Sleep(10 * time.Millisecond)
			}
			queued, cancelQueued := context.WithCancel(context.Background())
			cancelQueued()
			if _, err := m.Transcribe(queued, "a", "audio/ogg", []byte("fixture")); !errors.Is(err, context.Canceled) {
				t.Fatalf("queue cancellation: %v", err)
			}
			if mode == "cancel" {
				cancel()
			} else {
				if err := m.Close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("active cancellation: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("work not canceled")
			}
			if m.server != nil {
				t.Fatal("canceled inference left server alive")
			}
		})
	}
}

func TestServerStartupCancellation(t *testing.T) {
	m := localFixture(t)
	t.Setenv("SPEECH_SERVER_LOADING", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := m.Transcribe(ctx, "a", "audio/ogg", []byte("fixture"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup cancellation: %v", err)
	}
	if m.server != nil {
		t.Fatal("startup left a process")
	}
	entries, _ := os.ReadDir(m.root)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "server-") || strings.HasPrefix(entry.Name(), "transcribe-") {
			t.Fatalf("temporary directory leaked: %s", entry.Name())
		}
	}
	// Startup cancellation does not permanently close the Manager.
	t.Setenv("SPEECH_SERVER_LOADING", "")
	text, err := m.Transcribe(context.Background(), "a", "audio/ogg", []byte("fixture"))
	if err != nil || text != "fixture transcript" {
		t.Fatalf("retry: %q %v", text, err)
	}
}

func TestMissingServerSetup(t *testing.T) {
	m := localFixture(t)
	if err := os.Remove(filepath.Join(os.Getenv("PATH"), "whisper-server")); err != nil {
		t.Fatal(err)
	}
	s, err := m.Snapshot()
	if err != nil || s.RuntimeReady || s.Status != "missing_runtime" {
		t.Fatalf("snapshot: %+v %v", s, err)
	}
	if err = m.StartDownload(); err == nil || !strings.Contains(err.Error(), "cmake") {
		t.Fatalf("missing server skipped build prerequisites: %v", err)
	}
}
