package matrix

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// MaxMediaBytes bounds every image and audio file sent or received. It
// leaves room for base64 encoding within the session socket's 16 MiB JSON
// limit.
const MaxMediaBytes = 10 * 1024 * 1024

// downloadImage uses the authenticated homeserver media endpoint, never an
// arbitrary sender-provided HTTP URL. Authorization is checked by Run first.
func (c *Client) downloadImage(ctx context.Context, content *event.MessageEventContent) (Image, error) {
	file, err := c.downloadMedia(ctx, content, "image")
	if err != nil {
		return Image{}, err
	}
	mime := http.DetectContentType(file.Data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return Image{}, errors.New("supported images are PNG, JPEG, GIF and WebP")
	}
	return Image{MIMEType: mime, Data: file.Data}, nil
}

// downloadAttachment accepts arbitrary file content, without interpreting it as
// an image. MIME metadata is advisory; absent metadata is sniffed from the bytes.
func (c *Client) downloadAttachment(ctx context.Context, content *event.MessageEventContent) (Attachment, error) {
	return c.downloadMedia(ctx, content, "attachment")
}

// downloadMedia shares the authenticated endpoint, timeout, byte bound and
// encrypted-file integrity verification for every incoming media kind.
func (c *Client) downloadMedia(ctx context.Context, content *event.MessageEventContent, kind string) (Attachment, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	uri := content.URL
	if content.File != nil {
		uri = content.File.URL
		if err := content.File.PrepareForDecryption(); err != nil {
			return Attachment{}, errors.New("invalid encrypted " + kind + " metadata")
		}
	}
	mxc, err := uri.Parse()
	if err != nil || mxc.IsEmpty() {
		return Attachment{}, errors.New("invalid Matrix media URI")
	}
	if content.Info != nil && content.Info.Size > MaxMediaBytes {
		return Attachment{}, errors.New(kind + " exceeds 10 MiB")
	}
	resp, err := c.cli.Download(ctx, mxc)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		return Attachment{}, errors.New("media download failed")
	}
	if resp.StatusCode != http.StatusOK {
		return Attachment{}, errors.New("media download failed")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxMediaBytes+1))
	if err != nil {
		return Attachment{}, errors.New("media download failed")
	}
	if len(data) > MaxMediaBytes {
		return Attachment{}, errors.New(kind + " exceeds 10 MiB")
	}
	if content.File != nil {
		if err := content.File.DecryptInPlace(data); err != nil {
			return Attachment{}, errors.New(kind + " decryption or integrity check failed")
		}
	}
	mime := ""
	if content.Info != nil {
		mime = content.Info.MimeType
	}
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	name := content.FileName
	if name == "" {
		name = content.Body
	}
	return Attachment{Name: name, MIMEType: mime, Data: data}, nil
}

// SendImageTo uploads a PNG, JPEG, GIF or WebP of at most 10 MiB encrypted
// and sends it as an image message, only if the room still contains the
// expected allowlisted peer. The homeserver sees the ciphertext of the file,
// never its content; name is the file name the peer's client shows.
func (c *Client) SendImageTo(ctx context.Context, room, expectedPeer, name string, data []byte) error {
	if expectedPeer == "" {
		return errors.New("the expected Matrix peer is required")
	}
	if len(data) == 0 || len(data) > MaxMediaBytes {
		return errors.New("the image must be nonempty and at most 10 MiB")
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return errors.New("supported images are PNG, JPEG, GIF and WebP")
	}
	info := &event.FileInfo{MimeType: mime, Size: len(data)}
	if mime != "image/webp" { // the standard library reads no WebP
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return errors.New("the image could not be read")
		}
		info.Width, info.Height = cfg.Width, cfg.Height
	}
	if name == "" {
		name = "image"
	}
	return c.sendMedia(ctx, room, expectedPeer, name, data, event.MsgImage, info)
}

// SendAudioTo uploads a WAV, OGG, MP3 or M4A of at most 10 MiB encrypted and
// sends it as an audio message, only if the room still contains the expected
// allowlisted peer. Like an image, the homeserver sees only its ciphertext;
// name is the file name the peer's client shows.
func (c *Client) SendAudioTo(ctx context.Context, room, expectedPeer, name string, data []byte) error {
	if expectedPeer == "" {
		return errors.New("the expected Matrix peer is required")
	}
	if len(data) == 0 || len(data) > MaxMediaBytes {
		return errors.New("the audio file must be nonempty and at most 10 MiB")
	}
	mime, ok := audioType(name, data)
	if !ok {
		return errors.New("supported audio files are .wav, .ogg, .mp3 and .m4a whose content matches the extension")
	}
	info := &event.FileInfo{MimeType: mime, Size: len(data)}
	return c.sendMedia(ctx, room, expectedPeer, name, data, event.MsgAudio, info)
}

// audioType names the MIME type of an audio file by its extension, and only
// if its first bytes fit that format. Sniffing alone cannot tell: it calls
// OGG application/ogg, M4A video/mp4 and an MP3 without ID3 tag nothing.
func audioType(name string, data []byte) (string, bool) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".wav":
		return "audio/wav", len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE"
	case ".ogg":
		return "audio/ogg", bytes.HasPrefix(data, []byte("OggS"))
	case ".mp3":
		frame := len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0
		return "audio/mpeg", frame || bytes.HasPrefix(data, []byte("ID3"))
	case ".m4a":
		return "audio/mp4", len(data) >= 8 && string(data[4:8]) == "ftyp"
	}
	return "", false
}

// sendMedia seals data for the room's allowlisted peer and sends it as the
// message type asked for. Every media kind shares the room, lock and upload
// path, so what holds for one holds for all.
func (c *Client) sendMedia(ctx context.Context, room, expectedPeer, name string, data []byte, msgType event.MessageType, info *event.FileInfo) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	c.mu.RLock()
	ready := c.running && c.crypto != nil
	c.mu.RUnlock()
	if !ready {
		return errors.New("the Matrix transport is not running")
	}
	// Check the room before anything is uploaded; sendContent checks it again.
	peer, err := c.safeRoom(ctx, id.RoomID(room))
	if err != nil {
		return err
	}
	if peer != id.UserID(expectedPeer) {
		return errors.New("the peer of the Matrix room changed")
	}
	file := attachment.NewEncryptedFile()
	sealed := bytes.Clone(data)
	file.EncryptInPlace(sealed)
	uploadCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	uploaded, err := c.cli.UploadBytes(uploadCtx, sealed, "application/octet-stream")
	if err != nil || uploaded == nil || uploaded.ContentURI.IsEmpty() {
		return errors.New("the upload failed")
	}
	content := &event.MessageEventContent{MsgType: msgType, Body: name, FileName: name, Info: info,
		File: &event.EncryptedFileInfo{EncryptedFile: *file, URL: uploaded.ContentURI.CUString()}}
	return c.sendContent(ctx, room, id.UserID(expectedPeer), content)
}
