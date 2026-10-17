package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"ouroboros/internal/guard"
	transport "ouroboros/internal/matrix"
	"ouroboros/internal/workdir"
	"ouroboros/plugin"
)

const setAvatar = "matrix_set_avatar"
const maxAvatarBytes = 5 << 20

type avatarArgs struct {
	Path       string `json:"path"`
	Attachment int    `json:"attachment"`
}

// avatarTool uses the saved login without opening another sync loop or crypto
// store. Only the configured account's public profile can be changed.
func avatarTool(s store, files *guard.Guard) plugin.Tool {
	t := plugin.NewTool(setAvatar,
		"Set the configured Matrix account's public profile picture from a chat image attachment or a local PNG, JPEG or GIF file (at most 5 MiB, 4096 pixels per side and 4 megapixels overall). Uploads the image, changes the account avatar and reads it back to verify. The image becomes public on the homeserver, not end-to-end encrypted. Use only when the owner requests it; does not change a room avatar or another user's profile.",
		[]plugin.Param{{Name: "path", Description: "The local image file; relative paths use the current project or workspace. Omit to use a chat attachment.", Optional: true}},
		func(ctx context.Context, args avatarArgs) (string, error) {
			if _, peer := plugin.OriginFrom(ctx); peer {
				return "", errors.New("Matrix profile changes are owner-only")
			}
			if args.Path != "" && args.Attachment != 0 {
				return "", errors.New("choose a path or an attachment, not both")
			}
			cfg, err := s.load()
			if err != nil {
				return "", errors.New("cannot read Matrix settings")
			}
			if !cfg.Enabled || cfg.Token == "" {
				return "", errors.New("enable Matrix and log in before setting its profile picture")
			}

			data, err := avatarData(ctx, args, files)
			if err != nil {
				return "", err
			}
			if len(data) > maxAvatarBytes {
				return "", errors.New("the Matrix avatar exceeds 5 MiB")
			}
			latest, err := s.load()
			if err != nil || latest.Revision != cfg.Revision || latest.Token != cfg.Token || latest.UserID != cfg.UserID || latest.Homeserver != cfg.Homeserver || !latest.Enabled {
				return "", errors.New("Matrix settings changed; retry with the current account")
			}
			client, err := transport.New(transport.Config{Homeserver: cfg.Homeserver, UserID: cfg.UserID, Token: cfg.Token, DeviceID: cfg.DeviceID, StateDir: s.path("devices")})
			if err != nil {
				return "", errors.New("the Matrix login is incomplete or invalid")
			}
			defer client.Close()
			callCtx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			url, err := client.SetAvatar(callCtx, data)
			if err != nil {
				return "", err
			}
			result, _ := json.Marshal(struct {
				UserID    string `json:"user_id"`
				AvatarURL string `json:"avatar_url"`
				Verified  bool   `json:"verified"`
			}{cfg.UserID, url, true})
			return string(result), nil
		})
	t.Schema = `{"type":"object","properties":{"path":{"type":"string","description":"Local image path; omit to use a chat attachment."},"attachment":{"type":"integer","minimum":1,"description":"1-based image index in the most recent user message with images; default 1. Exclusive with path. Live conversation only, not after restart."}},"additionalProperties":false}`
	t.OwnerOnly = true
	return t
}

func avatarData(ctx context.Context, args avatarArgs, files *guard.Guard) ([]byte, error) {
	if args.Path == "" {
		images := plugin.InputImages(ctx)
		index := args.Attachment
		if index == 0 {
			index = 1
		}
		if index < 1 || index > len(images) {
			return nil, errors.New("no such image attachment in this conversation; send the image again")
		}
		return images[index-1].Data, nil
	}
	if files == nil {
		return nil, errors.New("Matrix avatar file access is unavailable")
	}
	checker, err := files.Checker(guard.Access{Tool: setAvatar, Directory: workdir.Of(ctx)})
	if err != nil {
		return nil, err
	}
	path, err := checker.Check(guard.Read, args.Path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("the avatar must be a regular image file")
	}
	if info.Size() > maxAvatarBytes {
		return nil, errors.New("the Matrix avatar exceeds 5 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxAvatarBytes+1))
	if err != nil {
		return nil, err
	}

	return data, nil
}
