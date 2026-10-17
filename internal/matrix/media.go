package matrix

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"maunium.net/go/mautrix/event"
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
