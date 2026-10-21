package speech

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mimetype "mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxAudio = 25 << 20
const maxText = 1 << 20

// checkWAVDuration counts PCM bytes, excluding variable-sized WAV metadata.
// The file is produced by our fixed mono 16 kHz s16le conversion, not the caller.
func checkWAVDuration(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot read converted audio")
	}
	defer f.Close()
	invalid := errors.New("invalid converted WAV audio")
	var header [12]byte
	if _, err = io.ReadFull(f, header[:]); err != nil || string(header[:4]) != "RIFF" || string(header[8:]) != "WAVE" {
		return invalid
	}
	for {
		var chunk [8]byte
		if _, err = io.ReadFull(f, chunk[:]); err != nil {
			return invalid
		}
		size := int64(binary.LittleEndian.Uint32(chunk[4:]))
		if string(chunk[:4]) == "data" {
			if size > 600*16000*2 {
				return errors.New("local speech audio exceeds 600 seconds")
			}
			return nil
		}
		if _, err = f.Seek(size+size%2, io.SeekCurrent); err != nil {
			return invalid
		}
	}
}

// Transcribe never downloads anything. Local input (including Matrix Ogg/Opus)
// is normalized through ffmpeg to mono 16 kHz PCM before persistent local inference.
func (m *Manager) Transcribe(ctx context.Context, name, mime string, data []byte) (string, error) {
	if len(data) == 0 || len(data) > maxAudio {
		return "", errors.New("speech audio must be between 1 byte and 25 MiB")
	}
	unlock, err := m.lock()
	if err != nil {
		return "", err
	}
	c, err := m.config()
	unlock()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	switch c.Provider {
	case "remote":
		return m.remote(ctx, c, name, mime, data)
	case "local":
		return m.local(ctx, c, data)
	default:
		return "", errors.New("speech transcription is disabled")
	}
}

func (m *Manager) remote(ctx context.Context, c Config, name, mime string, data []byte) (string, error) {
	if !validEndpoint(c.Endpoint) {
		return "", errors.New("invalid speech endpoint")
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	// Do not let a Matrix filename or MIME type inject multipart headers.
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || len(name) > 255 {
		name = "audio"
	}
	contentType, _, parseErr := mimetype.ParseMediaType(mime)
	if parseErr != nil {
		contentType = "application/octet-stream"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mimetype.FormatMediaType("form-data", map[string]string{"name": "file", "filename": name}))
	header.Set("Content-Type", contentType)
	part, err := w.CreatePart(header)
	if err != nil {
		return "", errors.New("cannot encode speech audio")
	}
	if _, err = part.Write(data); err != nil {
		return "", errors.New("cannot encode speech audio")
	}
	if err = w.WriteField("model", c.Model); err != nil {
		return "", err
	}
	if c.Language != "" && c.Language != "auto" {
		if err = w.WriteField("language", c.Language); err != nil {
			return "", err
		}
	}
	if err = w.Close(); err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(c.Endpoint, "/")
	if !strings.HasSuffix(endpoint, "/audio/transcriptions") {
		endpoint += "/audio/transcriptions"
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", errors.New("invalid speech endpoint")
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	client := *m.client
	// Never forward credentials or audio to a redirect target.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("speech endpoint request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("speech endpoint returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxText+1))
	if err != nil {
		return "", errors.New("cannot read speech response")
	}
	if len(b) > maxText {
		return "", errors.New("speech response exceeds size limit")
	}
	var result struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(b, &result) != nil || result.Text == nil {
		return "", errors.New("invalid speech endpoint response")
	}
	return strings.TrimSpace(*result.Text), nil
}

func (m *Manager) local(ctx context.Context, c Config, data []byte) (string, error) {
	ctx, release, err := m.acquireLocal(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	s, ok := m.spec(c.Model)
	if !ok || !m.modelReady(s) {
		return "", errors.New("local speech model is missing; download it explicitly first")
	}
	bin := m.serverRuntime()
	if bin == "" {
		return "", errors.New("local whisper-server is missing; download it explicitly first")
	}
	ff, err := lookCommand("ffmpeg")
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(m.root, "transcribe-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	input := filepath.Join(tmp, "audio")
	wav := filepath.Join(tmp, "audio.wav")
	if err = os.WriteFile(input, data, 0600); err != nil {
		return "", err
	}
	convertCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	// Decode one second beyond the limit so an overlong request is rejected,
	// never transcribed as a silently truncated prefix. The byte cap exceeds
	// 601 seconds of mono 16 kHz s16 PCM, including WAV headers.
	// Only the first audio stream is used; no network protocols or external playlists.
	err = command(convertCtx, ff, "-nostdin", "-v", "error", "-protocol_whitelist", "file,pipe", "-i", input, "-map", "0:a:0", "-vn", "-t", "601", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-fs", "20000000", "-y", wav)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("ffmpeg audio conversion failed or timed out")
	}
	if err = checkWAVDuration(wav); err != nil {
		return "", err
	}
	if err = m.startServer(ctx, bin, m.path("ggml-"+s.ID+".bin")); err != nil {
		return "", err
	}
	return m.infer(ctx, wav, c.Language)
}
