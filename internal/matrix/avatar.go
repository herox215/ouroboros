package matrix

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"time"
)

const (
	maxAvatarBytes     = 5 * 1024 * 1024
	maxAvatarDimension = 4096
	maxAvatarPixels    = 4 * 1024 * 1024
)

// SetAvatar uploads a PNG, JPEG or GIF and sets this client's own profile avatar.
// Images must be at most 5 MiB, 4096 pixels per side and 4 megapixels overall.
// It returns the mxc URI only after reading back the same profile URL. An error
// after upload may leave unused media; an error after PUT may still mean the
// profile changed. GIF validation decodes the first frame, not the animation.
func (c *Client) SetAvatar(ctx context.Context, data []byte) (string, error) {
	if len(data) == 0 || len(data) > maxAvatarBytes {
		return "", errors.New("avatar must be nonempty and at most 5 MiB")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", errors.New("avatar must be a valid PNG, JPEG or GIF")
	}
	var mime string
	switch format {
	case "png", "jpeg", "gif":
		mime = "image/" + format
	default:
		return "", errors.New("supported avatars are PNG, JPEG and GIF")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxAvatarDimension || cfg.Height > maxAvatarDimension || cfg.Width > maxAvatarPixels/cfg.Height {
		return "", errors.New("avatar exceeds dimension or pixel limits")
	}
	// Decode only after checking dimensions: a valid header alone is not an image.
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return "", errors.New("avatar image could not be decoded")
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	uploaded, err := c.cli.UploadBytes(ctx, data, mime)
	if err != nil || uploaded == nil {
		return "", errors.New("avatar upload failed")
	}
	uri := uploaded.ContentURI
	if uri.IsEmpty() {
		return "", errors.New("avatar upload returned an invalid Matrix media URI")
	}
	if err := c.cli.SetAvatarURL(ctx, uri); err != nil {
		return "", errors.New("avatar profile update failed")
	}
	actual, err := c.cli.GetOwnAvatarURL(ctx)
	if err != nil {
		return "", errors.New("avatar profile verification failed")
	}
	if actual != uri {
		return "", errors.New("avatar profile verification mismatch")
	}
	return uri.String(), nil
}
