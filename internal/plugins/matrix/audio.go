package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"ouroboros/internal/guard"
	transport "ouroboros/internal/matrix"
	"ouroboros/plugin"
)

const sendAudio = plugin.GatewaySendAudioTool

// audioSender is a connection that sends audio too, as the SDK's does.
type audioSender interface {
	SendAudioTo(ctx context.Context, room, peer, name string, data []byte) error
}

func (g guardedConnection) SendAudioTo(ctx context.Context, room, peer, name string, data []byte) error {
	if err := g.check(); err != nil {
		return err
	}
	sender, ok := g.connection.(audioSender)
	if !ok {
		return errors.New("this Matrix connection does not send audio")
	}
	return sender.SendAudioTo(ctx, room, peer, name, data)
}

type audioArgs struct {
	Path string `json:"path"`
	Room string `json:"room"`
}

// audioTool sends an audio file end-to-end encrypted to the room of the
// owner, which only an allowlisted user ever wrote to: never to anyone else.
// It is for sounds the owner wants to hear away from the machine, so the
// formats are the ones a phone's Matrix client plays.
func audioTool(l *live, files *guard.Guard) plugin.Tool {
	t := plugin.NewTool(sendAudio,
		"Send an audio file to the owner's Matrix room, end-to-end encrypted: a local WAV, OGG, MP3 or M4A of at most 10 MiB. It arrives at once, before the text of the current answer. Only rooms in which an allowed user talks to Ouro can receive it.",
		[]plugin.Param{{Name: "path", Description: "The local audio file"}},
		func(ctx context.Context, args audioArgs) (string, error) {
			if _, peer := plugin.OriginFrom(ctx); peer {
				return "", errors.New("sending Matrix audio is owner-only")
			}
			if args.Path == "" {
				return "", errors.New("give the path of the audio file")
			}
			conn, target, err := ownerRoom(l, args.Room)
			if err != nil {
				return "", err
			}
			data, err := localMedia(ctx, files, sendAudio, args.Path, transport.MaxMediaBytes, "Matrix audio file", "10 MiB")
			if err != nil {
				return "", err
			}
			name := filepath.Base(args.Path)
			if err := conn.SendAudioTo(ctx, target.ID, target.Peer, name, data); err != nil {
				return "", err
			}
			result, _ := json.Marshal(struct {
				Room  string `json:"room"`
				To    string `json:"to"`
				Bytes int    `json:"bytes"`
				Sent  bool   `json:"sent"`
			}{target.ID, target.Peer, len(data), true})
			return string(result), nil
		})
	t.Schema = `{"type":"object","properties":{"path":{"type":"string","description":"Local audio path; relative paths use the current project or workspace."},"room":{"type":"string","description":"Matrix room ID; needed only when more than one room talks to Ouro."}},"additionalProperties":false}`
	t.OwnerOnly = true
	return t
}
