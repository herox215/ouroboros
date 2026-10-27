package session

import (
	"context"
	"errors"

	"ouroboros/internal/chat"
)

// ImageAPI optionally extends API with image input. Service and Client implement
// it without requiring text-only session backends to accept attachments.
// Images are kept in live model memory only, not archived or exposed in events.
// Callers must bound and prepare images for their model; socket requests have
// the usual 16 MiB JSON limit (including base64 overhead).
type ImageAPI interface {
	AskImages(ctx context.Context, id, text string, images []chat.Image) error
}

// ErrImage means an input image has no data or an unsupported media type.
var ErrImage = errors.New("invalid input image")

// AskImages starts an answer with text and pictures, including picture-only
// input. Like Ask it returns ErrBusy rather than answering a pending question.
// It copies the pictures before returning so the caller may reuse its buffers.
func (s *Service) AskImages(ctx context.Context, id, text string, images []chat.Image) error {
	copied, err := copyImages(images)
	if err != nil {
		return err
	}
	return s.askImages(ctx, id, text, copied, false)
}

// ReplyImageAPI optionally extends API with conversational image replies.
// Like Steer, it answers a pending question, queues while busy, or starts when idle.
type ReplyImageAPI interface {
	SteerImages(ctx context.Context, id, text string, images []chat.Image) error
}

// SteerImages copies and validates images before delivering a conversational reply.
// An image-bearing permission reply never approves the pending call.
func (s *Service) SteerImages(ctx context.Context, id, text string, images []chat.Image) error {
	copied, err := copyImages(images)
	if err != nil {
		return err
	}
	return s.steerImages(ctx, id, text, copied)
}

func copyImages(images []chat.Image) ([]chat.Image, error) {
	copied := make([]chat.Image, len(images))
	for i, image := range images {
		switch image.Type {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return nil, ErrImage
		}
		if len(image.Data) == 0 {
			return nil, ErrImage
		}
		copied[i] = chat.Image{Type: image.Type, Data: append([]byte(nil), image.Data...)}
	}
	return copied, nil
}
