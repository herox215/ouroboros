package matrix

import (
	"context"
	"errors"
	"mime"
	"slices"
	"strings"

	"ouroboros/plugin"
)

type transcribeFunc func(context.Context, string, string, []byte) (string, error)

var errAudioReported = errors.New("audio transcription failure reported to sender")

func audioAttachment(file plugin.Attachment) bool {
	media, _, err := mime.ParseMediaType(file.Type)
	return err == nil && strings.HasPrefix(strings.ToLower(media), "audio/")
}

// Check immediately before handing bytes to speech (which may use an external
// service), and again before publishing its result. The SDK already checked
// room membership; the connection's revision guard and live allowlist still
// apply here, including while a slow transcription is in progress.
func (c *conversations) allowAudio(ctx context.Context, conn connection, msg incoming, revision string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if guarded, ok := conn.(interface{ check() error }); ok {
		if err := guarded.check(); err != nil {
			return err
		}
	}
	cfg, err := c.store.load()
	if err != nil || !cfg.Enabled || cfg.Token == "" || cfg.Revision != revision || !slices.Contains(cfg.AllowedUsers, msg.Sender) {
		return plugin.ErrGatewayDenied
	}
	return nil
}

// Audio is consumed only after every clip succeeds. Failures stop the entire
// message, rather than sending a partial request or raw audio to the agent.
func (c *conversations) prepareAudio(ctx context.Context, conn connection, msg incoming, revision string) (incoming, bool, error) {
	hasAudio := slices.ContainsFunc(msg.Attachments, audioAttachment)
	if !hasAudio {
		return msg, false, nil
	}
	var transcripts []string
	files := make([]plugin.Attachment, 0, len(msg.Attachments))
	for _, file := range msg.Attachments {
		if !audioAttachment(file) {
			files = append(files, file)
			continue
		}
		if err := c.allowAudio(ctx, conn, msg, revision); err != nil {
			return msg, true, err
		}
		var text string
		var err error
		if c.transcribe == nil {
			err = errors.New("speech transcription is not configured")
		} else {
			text, err = c.transcribe(ctx, file.Name, file.Type, file.Data)
		}
		if checkErr := c.allowAudio(ctx, conn, msg, revision); checkErr != nil {
			return msg, true, checkErr
		}
		if err == nil && strings.TrimSpace(text) == "" {
			err = errors.New("no speech was recognized")
		}
		if err != nil {
			sendErr := conn.SendTo(ctx, msg.RoomID, msg.Sender, "Audio transcription failed: "+err.Error()+". Check Speech settings and install/select a model if needed. This message was not sent to the agent; please retry or send text.")
			if sendErr != nil {
				return msg, true, sendErr
			}
			return msg, true, errAudioReported
		}
		transcripts = append(transcripts, "[Audio transcription]\n"+strings.TrimSpace(text))
	}
	transcript := strings.Join(transcripts, "\n\n")
	if err := conn.SendTo(ctx, msg.RoomID, msg.Sender, transcript); err != nil {
		return msg, true, err
	}
	if err := c.allowAudio(ctx, conn, msg, revision); err != nil {
		return msg, true, err
	}
	if msg.Text != "" {
		msg.Text += "\n\n"
	}
	msg.Text += transcript
	msg.Attachments = files
	return msg, true, nil
}
