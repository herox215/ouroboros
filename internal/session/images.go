package session

import (
	"context"
	"errors"

	"ouroboros/internal/openai"
)

// ImageAPI optionally extends API with image input. Service and Client implement
// it without requiring text-only session backends to accept attachments.
// Images are kept in live model memory only, not archived or exposed in events.
// Callers must bound and prepare images for their model; socket requests have
// the usual 16 MiB JSON limit (including base64 overhead).
type ImageAPI interface {
	AskImages(ctx context.Context, id, text string, images []openai.Image) error
}

// ErrImage means an input image has no data or an unsupported media type.
var ErrImage = errors.New("invalid input image")

// AskImages starts an answer with text and pictures, including picture-only
// input. Like Ask it returns ErrBusy rather than answering a pending question.
// It copies the pictures before returning so the caller may reuse its buffers.
func (s *Service) AskImages(ctx context.Context, id, text string, images []openai.Image) error {
	copied := make([]openai.Image, len(images))
	for i, image := range images {
		switch image.Type {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return ErrImage
		}
		if len(image.Data) == 0 {
			return ErrImage
		}
		copied[i] = openai.Image{Type: image.Type, Data: append([]byte(nil), image.Data...)}
	}
	return s.askImages(ctx, id, text, copied, false)
}
