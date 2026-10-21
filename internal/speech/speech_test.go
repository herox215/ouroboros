package speech

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nset -eu\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func commands(t *testing.T, whisper bool) string {
	t.Helper()
	dir := t.TempDir()
	script(t, dir, "ffmpeg", `if [ "$1" = -version ]; then exit 0; fi
for last; do :; done
printf 'RIFF\020\000\000\000WAVEdata\004\000\000\000\000\000\000\000' > "$last"
`)
	if whisper {
		script(t, dir, "whisper-cli", `if [ "$1" = --help ]; then exit 0; fi
while [ "$#" -gt 0 ]; do
 if [ "$1" = -of ]; then shift; printf ' fixture transcript \n' > "$1.txt"; exit 0; fi
 shift
done
exit 1
`)
		script(t, dir, "whisper-server", fmt.Sprintf("if [ \"$1\" = --help ]; then exit 0; fi\nexec %q -test.run=^TestWhisperServerProcess$ -- \"$@\"\n", os.Args[0]))
	}
	t.Setenv("PATH", dir)
	return dir
}
func fixture(m *Manager) []byte {
	b := []byte("tiny model fixture")
	h := sha256.Sum256(b)
	m.models = []modelSpec{{Model{"large-v3-turbo-q5_0", "Fixture", int64(len(b))}, hex.EncodeToString(h[:])}}
	return b
}
func saveLocal(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.Save(Config{Provider: "local", Model: "large-v3-turbo-q5_0", Language: "auto"}); err != nil {
		t.Fatal(err)
	}
}
func waitDownload(t *testing.T, m *Manager) Progress {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s, err := m.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if !s.Download.Active {
			return s.Download
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("download did not finish")
	return Progress{}
}

func TestSettings(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "speech"))
	s, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if s.Config.Provider != "disabled" || s.Config.Model != "large-v3-turbo-q5_0" || len(s.Models) != 3 {
		t.Fatalf("defaults: %+v", s)
	}
	c := Config{Provider: "remote", Model: "custom-remote", Endpoint: "https://example.com/v1", APIKey: "secret", Language: "en"}
	if err = m.Save(c); err != nil {
		t.Fatal(err)
	}
	c.APIKey = ""
	c.Language = "de"
	if err = New(m.root).Save(c); err != nil {
		t.Fatal(err)
	}
	s, err = New(m.root).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "secret") || !s.HasKey || s.Config.APIKey != "" {
		t.Fatalf("key exposed: %s", b)
	}
	persisted, err := m.config()
	if err != nil || persisted.APIKey != "secret" {
		t.Fatal("key not preserved", err)
	}
	for _, name := range []string{"config.json", "state.lock"} {
		st, err := os.Stat(m.path(name))
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatalf("permissions %s: %v %v", name, st, err)
		}
	}
	for _, bad := range []Config{
		{Provider: "bogus"}, {Provider: "local", Model: "../../model"},
		{Provider: "remote", Endpoint: "https://key@example.com"},
		{Provider: "remote", Endpoint: "https://example.com?key=secret"},
		{Provider: "remote", Endpoint: "https://example.com", APIKey: "bad\nkey"},
	} {
		if m.Save(bad) == nil {
			t.Fatalf("accepted invalid configuration: %+v", bad)
		}
	}
}

func TestRemote(t *testing.T) {
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.URL.Path != "/v1/audio/transcriptions" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("wrong request path/auth")
		}
		if err := r.ParseMultipartForm(maxAudio); err != nil {
			t.Error(err)
			return
		}
		if r.FormValue("model") != "remote-model" || r.FormValue("language") != "fr" {
			t.Error("missing model/language")
		}
		f, h, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		var b bytes.Buffer
		b.ReadFrom(f)
		if b.String() != "opus fixture" || h.Filename != "voice.ogg" {
			t.Error("bad file part")
		}
		switch count {
		case 1:
			fmt.Fprint(w, `{"text":" bonjour "}`)
		case 2:
			w.WriteHeader(401)
			fmt.Fprint(w, "secret response body")
		case 3:
			fmt.Fprint(w, strings.Repeat("x", maxText+1))
		case 4:
			http.Redirect(w, r, "/leak", http.StatusTemporaryRedirect)
		case 5:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	m := New(t.TempDir())
	if err := m.Save(Config{Provider: "remote", Model: "remote-model", Endpoint: srv.URL + "/v1", Language: "fr", APIKey: "secret"}); err != nil {
		t.Fatal(err)
	}
	text, err := m.Transcribe(context.Background(), "../voice.ogg", "audio/ogg", []byte("opus fixture"))
	if err != nil || text != "bonjour" {
		t.Fatalf("%q %v", text, err)
	}
	for i := 0; i < 4; i++ {
		_, err = m.Transcribe(context.Background(), "voice.ogg", "audio/ogg", []byte("opus fixture"))
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe/missing error: %v", err)
		}
	}
	if count != 5 {
		t.Fatalf("redirect followed or missing requests: %d", count)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = m.Transcribe(ctx, "voice.ogg", "audio/ogg", []byte("opus fixture")); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestDownloadRequiresLocalProvider(t *testing.T) {
	for _, provider := range []string{"disabled", "remote"} {
		t.Run(provider, func(t *testing.T) {
			m := New(t.TempDir())
			if err := m.Save(Config{Provider: provider, Model: catalog[0].ID, Endpoint: "https://example.com/v1"}); err != nil {
				t.Fatal(err)
			}
			// No commands are needed: rejection must precede setup/worker creation,
			// even when the remote model ID happens to be in the local catalog.
			t.Setenv("PATH", t.TempDir())
			if err := m.StartDownload(); err == nil || !strings.Contains(err.Error(), "local provider") {
				t.Fatalf("setup accepted %s or wrong error: %v", provider, err)
			}
			if _, err := os.Stat(m.path("download.json")); !os.IsNotExist(err) {
				t.Fatalf("setup started: %v", err)
			}
		})
	}
}

func TestLocalDurationLimit(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("real ffmpeg unavailable")
	}
	dir := commands(t, true)
	script(t, dir, "ffmpeg", fmt.Sprintf("exec %q \"$@\"\n", ff))
	for _, duration := range []string{"600", "600.0000625", "601", "900"} {
		t.Run(duration, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			input := filepath.Join(t.TempDir(), "silence.flac")
			cmd := exec.CommandContext(ctx, ff, "-nostdin", "-v", "error", "-f", "lavfi", "-i", "anullsrc=r=16000:cl=mono", "-t", duration, "-c:a", "flac", input)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generate audio: %v %s", err, out)
			}
			data, err := os.ReadFile(input)
			if err != nil {
				t.Fatal(err)
			}
			m := New(t.TempDir())
			model := fixture(m)
			saveLocal(t, m)
			if err := os.WriteFile(m.path("ggml-"+m.models[0].ID+".bin"), model, 0600); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "called")
			script(t, dir, "whisper-cli", fmt.Sprintf(`if [ "$1" = --help ]; then exit 0; fi
printf called > %q
while [ "$#" -gt 0 ]; do
 if [ "$1" = -of ]; then shift; printf transcript > "$1.txt"; exit 0; fi
 shift
done
exit 1
`, marker))
			defer m.Close()
			t.Setenv("SPEECH_SERVER_MARKER", marker)
			t.Setenv("SPEECH_SERVER_TEXT", "transcript")
			text, err := m.Transcribe(ctx, "silence.flac", "audio/flac", data)
			if duration == "600" {
				if err != nil || text != "transcript" {
					t.Fatalf("exact limit rejected: %q %v", text, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "exceeds 600 seconds") || text != "" {
					t.Fatalf("overlong audio not rejected: %q %v", text, err)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("whisper invoked for overlong audio: %v", err)
				}
			}
			entries, err := os.ReadDir(m.root)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "transcribe-") {
					t.Fatal("conversion directory left behind")
				}
			}
		})
	}
}

func TestDownloadAndLocal(t *testing.T) {
	commands(t, true)
	m := New(t.TempDir())
	defer m.Close()
	b := fixture(m)
	saveLocal(t, m)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(b) }))
	defer srv.Close()
	m.modelBase = srv.URL + "/ggml-"
	if err := m.StartDownload(); err != nil {
		t.Fatal(err)
	}
	if p := waitDownload(t, m); p.Error != "" || p.Phase != "complete" || p.Received != int64(len(b)) {
		t.Fatalf("progress: %+v", p)
	}
	s, err := m.Snapshot()
	if err != nil || !s.RuntimeReady || !s.ModelReady || s.Status != "ready" {
		t.Fatalf("snapshot: %+v %v", s, err)
	}
	text, err := m.Transcribe(context.Background(), "voice.ogg", "audio/ogg; codecs=opus", []byte("opus fixture"))
	if err != nil || text != "fixture transcript" {
		t.Fatalf("local result %q %v", text, err)
	}
	entries, _ := os.ReadDir(m.root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "transcribe-") || strings.HasSuffix(e.Name(), ".part") {
			t.Fatalf("temporary file left: %s", e.Name())
		}
	}
}

func TestDownloadValidation(t *testing.T) {
	commands(t, true)
	for _, kind := range []string{"checksum", "length", "oversize", "http"} {
		t.Run(kind, func(t *testing.T) {
			m := New(t.TempDir())
			b := fixture(m)
			saveLocal(t, m)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "checksum":
					w.Write(bytes.Repeat([]byte("x"), len(b)))
				case "length":
					w.Header().Set("Content-Length", "1")
					w.Write([]byte("x"))
				case "oversize":
					w.(http.Flusher).Flush()
					w.Write(bytes.Repeat([]byte("x"), len(b)+1))
				case "http":
					w.WriteHeader(403)
				}
			}))
			defer srv.Close()
			m.modelBase = srv.URL + "/"
			if err := m.StartDownload(); err != nil {
				t.Fatal(err)
			}
			if p := waitDownload(t, m); p.Error == "" {
				t.Fatalf("accepted invalid %s: %+v", kind, p)
			}
			if _, err := os.Stat(m.path("ggml-large-v3-turbo-q5_0.bin")); !os.IsNotExist(err) {
				t.Fatal("published bad model", err)
			}
			if _, err := os.Stat(m.path("ggml-large-v3-turbo-q5_0.bin.part")); !os.IsNotExist(err) {
				t.Fatal("left part", err)
			}
		})
	}
}

// A separate test-binary process observes the owner's lock and cancels its
// worker. This is not merely two pointers sharing an in-process mutex.
func TestDownloadProcess(t *testing.T) {
	if root := os.Getenv("SPEECH_TEST_ROOT"); root != "" {
		m := New(root)
		fixture(m)
		s, err := m.Snapshot()
		if err != nil || !s.Download.Active {
			t.Fatalf("cross-process snapshot: %+v %v", s, err)
		}
		if m.StartDownload() == nil {
			t.Fatal("second process acquired download")
		}
		if err = m.CancelDownload(); err != nil {
			t.Fatal(err)
		}
		return
	}
	commands(t, true)
	m := New(t.TempDir())
	fixture(m)
	saveLocal(t, m)
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(m.models[0].Bytes))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	m.modelBase = srv.URL + "/"
	if err := m.StartDownload(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download not started")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDownloadProcess$")
	cmd.Env = append(os.Environ(), "SPEECH_TEST_ROOT="+m.root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %s %v", out, err)
	}
	p := waitDownload(t, m)
	if p.Phase != "cancelled" {
		t.Fatalf("cancel: %+v", p)
	}
	// A stale persisted state after a crash never claims an active download.
	if err := writeJSON(m.path("download.json"), Progress{Active: true, Phase: "model"}); err != nil {
		t.Fatal(err)
	}
	s, err := m.Snapshot()
	if err != nil || s.Download.Active || s.Download.Error == "" {
		t.Fatalf("stale state: %+v %v", s, err)
	}
}

func archiveFixture(t *testing.T, name string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	// Like GitHub's codeload tarballs, which start with a pax global header.
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": "4979e04f"}}); err != nil {
		t.Fatal(err)
	}
	data := []byte("tiny source fixture")
	if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0600}); err != nil {
		t.Fatal(err)
	}
	tw.Write(data)
	tw.Close()
	gz.Close()
	return b.Bytes()
}
func TestManagedRuntimeBuild(t *testing.T) {
	dir := commands(t, false)
	fake := script(t, dir, "template", `[ "$1" = --help ]`)
	t.Setenv("FAKE_CLI", fake)
	script(t, dir, "cc", "exit 0\n")
	script(t, dir, "c++", "exit 0\n")
	script(t, dir, "cmake", `if [ "$1" = --build ]; then
 /bin/mkdir -p "$2/bin"
 /bin/cp "$FAKE_CLI" "$2/bin/whisper-cli"
 /bin/cp "$FAKE_CLI" "$2/bin/whisper-server"
fi
`)
	m := New(t.TempDir())
	b := fixture(m)
	saveLocal(t, m)
	archive := archiveFixture(t, "whisper/CMakeLists.txt")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/source" {
			w.Write(archive)
		} else {
			w.Write(b)
		}
	}))
	defer srv.Close()
	m.sourceURL = srv.URL + "/source"
	m.modelBase = srv.URL + "/"
	if err := m.StartDownload(); err != nil {
		t.Fatal(err)
	}
	if p := waitDownload(t, m); p.Error != "" {
		t.Fatalf("build failed: %+v", p)
	}
	s, err := m.Snapshot()
	if err != nil || !s.RuntimeReady || s.Status != "ready" {
		t.Fatalf("built snapshot: %+v %v", s, err)
	}
	entries, _ := os.ReadDir(m.root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "build-") {
			t.Fatal("build directory leaked")
		}
	}
}

func TestArchiveAndPrerequisites(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute"} {
		p := filepath.Join(t.TempDir(), "source.tar.gz")
		os.WriteFile(p, archiveFixture(t, name), 0600)
		if extract(context.Background(), p, t.TempDir()) == nil {
			t.Fatalf("accepted archive path %s", name)
		}
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	m := New(t.TempDir())
	saveLocal(t, m)
	if err := m.StartDownload(); err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Fatalf("missing ffmpeg: %v", err)
	}
	script(t, dir, "ffmpeg", "exit 0\n")
	if err := m.StartDownload(); err == nil || !strings.Contains(err.Error(), "cmake") {
		t.Fatalf("missing cmake: %v", err)
	}
	script(t, dir, "whisper-cli", "exit 1\n")
	s, err := m.Snapshot()
	if err != nil || s.RuntimeReady {
		t.Fatalf("false runtime readiness: %+v %v", s, err)
	}
	if _, err := m.Transcribe(context.Background(), "x", "audio/ogg", []byte("x")); err == nil {
		t.Fatal("missing model accepted")
	}
}
