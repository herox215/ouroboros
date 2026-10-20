package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"ouroboros/internal/guard"
	"ouroboros/plugin"
)

const sendImage = "matrix_send_image"
const maxSentImageBytes = 10 << 20

// imageSender is a connection that sends images too, as the SDK's does.
type imageSender interface {
	SendImageTo(ctx context.Context, room, peer, name string, data []byte) error
}

func (g guardedConnection) SendImageTo(ctx context.Context, room, peer, name string, data []byte) error {
	if err := g.check(); err != nil {
		return err
	}
	sender, ok := g.connection.(imageSender)
	if !ok {
		return errors.New("this Matrix connection does not send images")
	}
	return sender.SendImageTo(ctx, room, peer, name, data)
}

// live is the connection the node runs now, for tools that send by it
// outside of an answer to a room: there is at most one.
type live struct {
	mu            sync.Mutex
	conn          guardedConnection
	conversations *conversations
}

func (l *live) set(conn guardedConnection, c *conversations) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.conn, l.conversations = conn, c
}

// clear forgets c, unless another connection took its place already.
func (l *live) clear(c *conversations) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conversations == c {
		l.conn, l.conversations = guardedConnection{}, nil
	}
}

type room struct{ ID, Peer string }

// current is the running connection with the rooms that talk to Ouro.
func (l *live) current() (guardedConnection, []room, error) {
	l.mu.Lock()
	conn, c := l.conn, l.conversations
	l.mu.Unlock()
	if c == nil {
		return guardedConnection{}, nil, errors.New("the Matrix gateway is not connected")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var rooms []room
	for _, entry := range c.entries {
		peer, _, _ := strings.Cut(entry.Identity.Subject, "\n")
		rooms = append(rooms, room{entry.Room, peer})
	}
	slices.SortFunc(rooms, func(a, b room) int { return strings.Compare(a.ID, b.ID) })
	return conn, rooms, nil
}

type imageArgs struct {
	Path       string `json:"path"`
	Attachment int    `json:"attachment"`
	Room       string `json:"room"`
}

// imageTool sends a picture end-to-end encrypted to the room of the owner,
// which only an allowlisted user ever wrote to: never to anyone else.
func imageTool(l *live, files *guard.Guard) plugin.Tool {
	t := plugin.NewTool(sendImage,
		"Send an image to the owner's Matrix room, end-to-end encrypted: a local PNG, JPEG, GIF or WebP file of at most 10 MiB, or an image attachment of the conversation. It arrives at once, before the text of the current answer. Only rooms in which an allowed user talks to Ouro can receive it.",
		[]plugin.Param{{Name: "path", Description: "The local image file", Optional: true}},
		func(ctx context.Context, args imageArgs) (string, error) {
			if _, peer := plugin.OriginFrom(ctx); peer {
				return "", errors.New("sending Matrix images is owner-only")
			}
			if (args.Path == "") == (args.Attachment == 0) {
				return "", errors.New("give either a path or an attachment")
			}
			conn, rooms, err := l.current()
			if err != nil {
				return "", err
			}
			var target *room
			for i := range rooms {
				if args.Room == "" && len(rooms) == 1 || rooms[i].ID == args.Room {
					target = &rooms[i]
				}
			}
			if target == nil {
				if len(rooms) == 0 {
					return "", errors.New("no Matrix room talks to Ouro yet: write to it from Matrix first")
				}
				ids := make([]string, len(rooms))
				for i, r := range rooms {
					ids[i] = r.ID
				}
				return "", errors.New("choose the room: one of " + strings.Join(ids, ", "))
			}
			var data []byte
			name := "image"
			if args.Path != "" {
				data, err = localImage(ctx, files, sendImage, args.Path, maxSentImageBytes, "Matrix image", "10 MiB")
				name = filepath.Base(args.Path)
			} else {
				images := plugin.InputImages(ctx)
				if args.Attachment < 1 || args.Attachment > len(images) {
					return "", errors.New("no such image attachment in this conversation")
				}
				data = images[args.Attachment-1].Data
			}
			if err != nil {
				return "", err
			}
			if err := conn.SendImageTo(ctx, target.ID, target.Peer, name, data); err != nil {
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
	t.Schema = `{"type":"object","properties":{"path":{"type":"string","description":"Local image path; relative paths use the current project or workspace."},"attachment":{"type":"integer","minimum":1,"description":"Instead of path: 1-based image index in the most recent user message with images."},"room":{"type":"string","description":"Matrix room ID; needed only when more than one room talks to Ouro."}},"additionalProperties":false}`
	t.OwnerOnly = true
	return t
}
