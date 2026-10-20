package matrix

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"net/http"
	"time"

	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Leave room for base64 encoding within the session socket's 16 MiB JSON limit.
const maxImageBytes = 10 * 1024 * 1024

// downloadImage uses the authenticated homeserver media endpoint, never an
// arbitrary sender-provided HTTP URL. Authorization is checked by Run first.
func (c *Client) downloadImage(ctx context.Context, content *event.MessageEventContent) (Image, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	uri := content.URL
	if content.File != nil {
		uri = content.File.URL
		if err := content.File.PrepareForDecryption(); err != nil {
			return Image{}, errors.New("invalid encrypted image metadata")
		}
	}
	mxc, err := uri.Parse()
	if err != nil || mxc.IsEmpty() {
		return Image{}, errors.New("invalid Matrix media URI")
	}
	if content.Info != nil && content.Info.Size > maxImageBytes {
		return Image{}, errors.New("image exceeds 10 MiB")
	}
	resp, err := c.cli.Download(ctx, mxc)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		return Image{}, errors.New("media download failed")
	}
	if resp.StatusCode != http.StatusOK {
		return Image{}, errors.New("media download failed")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return Image{}, errors.New("media download failed")
	}
	if len(data) > maxImageBytes {
		return Image{}, errors.New("image exceeds 10 MiB")
	}
	if content.File != nil {
		if err := content.File.DecryptInPlace(data); err != nil {
			return Image{}, errors.New("image decryption or integrity check failed")
		}
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return Image{}, errors.New("supported images are PNG, JPEG, GIF and WebP")
	}
	return Image{MIMEType: mime, Data: data}, nil
}

// SendImageTo uploads a PNG, JPEG, GIF or WebP of at most 10 MiB encrypted
// and sends it as an image message, only if the room still contains the
// expected allowlisted peer. The homeserver sees the ciphertext of the file,
// never its content; name is the file name the peer's client shows.
func (c *Client) SendImageTo(ctx context.Context, room, expectedPeer, name string, data []byte) error {
	if expectedPeer == "" {
		return errors.New("the expected Matrix peer is required")
	}
	if len(data) == 0 || len(data) > maxImageBytes {
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
		return errors.New("the image upload failed")
	}
	if name == "" {
		name = "image"
	}
	content := &event.MessageEventContent{MsgType: event.MsgImage, Body: name, FileName: name, Info: info,
		File: &event.EncryptedFileInfo{EncryptedFile: *file, URL: uploaded.ContentURI.CUString()}}
	return c.sendContent(ctx, room, id.UserID(expectedPeer), content)
}
