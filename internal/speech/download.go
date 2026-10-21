package speech

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *Manager) prepare(lock *os.File, spec modelSpec) {
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := os.Stat(m.path("cancel")); err == nil {
					cancel()
					return
				}
			}
		}
	}()
	err := m.prepareLocal(ctx, spec)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	p, _ := m.progress()
	p.Active = false
	p.Phase = "complete"
	p.Error = ""
	if err != nil {
		p.Phase = "error"
		p.Error = err.Error()
		if errors.Is(err, context.Canceled) {
			p.Phase = "cancelled"
			p.Error = "speech download cancelled"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			p.Error = "speech preparation timed out"
		}
	}
	// Publish the final state before releasing download.lock.
	_ = m.setProgress(p)
}

func (m *Manager) prepareLocal(ctx context.Context, s modelSpec) error {
	ff, err := lookCommand("ffmpeg")
	if err != nil {
		return err
	}
	if err = command(ctx, ff, "-version"); err != nil {
		return errors.New("ffmpeg is not usable")
	}
	if m.runtime() == "" || m.serverRuntime() == "" {
		if err = m.buildRuntime(ctx); err != nil {
			return err
		}
	}
	if err = command(ctx, m.runtime(), "--help"); err != nil {
		return errors.New("whisper-cli is not usable")
	}
	if err = command(ctx, m.serverRuntime(), "--help"); err != nil {
		return errors.New("whisper-server is not usable")
	}
	if !m.modelReady(s) {
		if err = m.fetch(ctx, m.modelBase+s.ID+".bin", m.path("ggml-"+s.ID+".bin"), "model", s.Bytes, s.Bytes, s.sha); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// fetch only publishes a complete size-checked file, and checks a pinned SHA256
// when supplied. No URL or response body is ever included in errors.
func (m *Manager) fetch(ctx context.Context, url, dest, phase string, expected, max int64, sha string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errors.New("invalid speech download URL")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("speech download request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("speech download returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > max || (expected > 0 && resp.ContentLength >= 0 && resp.ContentLength != expected) {
		return errors.New("speech download content length mismatch")
	}
	part := dest + ".part"
	// The per-root download lock makes removing a leftover part safe.
	if err = os.Remove(part); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { f.Close(); os.Remove(part) }()
	total := resp.ContentLength
	if expected > 0 {
		total = expected
	}
	if total < 0 {
		total = 0
	}
	p := Progress{Active: true, Phase: phase, Total: total}
	if err = m.setProgress(p); err != nil {
		return err
	}
	hash := sha256.New()
	buf := make([]byte, 128<<10)
	last := time.Now()
	reader := io.LimitReader(resp.Body, max+1)
	for {
		n, e := reader.Read(buf)
		if n > 0 {
			p.Received += int64(n)
			if p.Received > max {
				return errors.New("speech download exceeds size limit")
			}
			if _, err = f.Write(buf[:n]); err != nil {
				return err
			}
			hash.Write(buf[:n])
			if time.Since(last) > 200*time.Millisecond {
				if err = m.setProgress(p); err != nil {
					return err
				}
				last = time.Now()
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return errors.New("speech download body failed")
		}
	}
	if p.Received == 0 || (expected > 0 && p.Received != expected) || (resp.ContentLength >= 0 && p.Received != resp.ContentLength) {
		return errors.New("speech download is incomplete")
	}
	if sha != "" && hex.EncodeToString(hash.Sum(nil)) != sha {
		return errors.New("speech download checksum mismatch")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(part, dest); err != nil {
		return err
	}
	return m.setProgress(p)
}

func (m *Manager) buildRuntime(ctx context.Context) error {
	tmp, err := os.MkdirTemp(m.root, "build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, "source.tar.gz")
	if err = m.fetch(ctx, m.sourceURL, archive, "runtime_download", 0, 64<<20, ""); err != nil {
		return err
	}
	source := filepath.Join(tmp, "source")
	if err = os.Mkdir(source, 0700); err != nil {
		return err
	}
	if err = extract(ctx, archive, source); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		return errors.New("invalid whisper.cpp source layout")
	}
	src := filepath.Join(source, entries[0].Name())
	build := filepath.Join(tmp, "build")
	if err = m.setProgress(Progress{Active: true, Phase: "runtime_build"}); err != nil {
		return err
	}
	if err = command(ctx, "cmake", "-S", src, "-B", build, "-DCMAKE_BUILD_TYPE=Release", "-DBUILD_SHARED_LIBS=OFF", "-DGGML_NATIVE=OFF", "-DGGML_BACKEND_DL=OFF", "-DWHISPER_BUILD_TESTS=OFF", "-DWHISPER_BUILD_EXAMPLES=ON", "-DWHISPER_CURL=OFF"); err != nil {
		return errors.New("whisper.cpp cmake configuration failed (check cmake and C/C++ toolchain)")
	}
	if err = command(ctx, "cmake", "--build", build, "--config", "Release", "--target", "whisper-cli", "whisper-server", "-j", "2"); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("whisper.cpp build failed (check C/C++ toolchain)")
	}
	dir := m.path("runtime")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Managed executables are statically linked to whisper/ggml, not libc.
	for _, name := range []string{"whisper-cli", "whisper-server"} {
		bin := filepath.Join(build, "bin", name)
		if err = command(ctx, bin, "--help"); err != nil {
			return fmt.Errorf("built %s failed its help check", name)
		}
		f, e := os.Open(bin)
		if e != nil {
			return e
		}
		data, e := io.ReadAll(io.LimitReader(f, (128<<20)+1))
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		if len(data) > 128<<20 {
			return fmt.Errorf("%s executable exceeds size limit", name)
		}
		if err = atomicExecutable(filepath.Join(dir, name), data); err != nil {
			return err
		}
	}
	return nil
}

// extract rejects traversal and links, bounds expansion, and never restores
// upstream permissions. Only regular files and directories are needed to build.
func extract(ctx context.Context, archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return errors.New("invalid whisper.cpp source archive")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for count := 0; ; count++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		h, e := tr.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return errors.New("invalid whisper.cpp source archive")
		}
		if count >= 20000 || h.Size < 0 || h.Size > 256<<20 || total+h.Size > 256<<20 {
			return errors.New("whisper.cpp source exceeds extraction limit")
		}
		total += h.Size
		clean := filepath.Clean(h.Name)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return errors.New("unsafe whisper.cpp source path")
		}
		target := filepath.Join(dest, clean)
		switch h.Typeflag {
		case tar.TypeDir:
			if err = os.MkdirAll(target, 0700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			out, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			_, e = io.CopyN(out, tr, h.Size)
			ce := out.Close()
			if e != nil {
				return e
			}
			if ce != nil {
				return ce
			}
		case tar.TypeXGlobalHeader:
			// GitHub tarballs start with a pax_global_header carrying the commit.
		default:
			return errors.New("unsupported link or entry in whisper.cpp source archive")
		}
	}
}
