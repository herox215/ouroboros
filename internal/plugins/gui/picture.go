package gui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"ouroboros/internal/guard"
	"ouroboros/internal/workdir"
	"ouroboros/plugin"
)

// The pictures the agent shows in the window. They are kept in the pictures
// directory of the profile, named after their content, so that the same
// picture is kept once and a name never means another picture: the window
// serves them, and keeps them past the answer they were shown in.

// maxPictureBytes is how large a picture the window shows may be: as large as
// one that goes through a gateway, 10 MiB.
const maxPictureBytes = 10 << 20

// mediaPath is where the window and the page of the thread serve the pictures.
// It is the same path in both: they show the same app, and what the thread
// tells it is a name, see plugin.SessionPicture.
const mediaPath = "/media/"

// pictureName is what the name of a picture the window serves may be: a hash
// of its content and its kind, as keep makes it. No path: nothing to climb
// out of the pictures directory with.
var pictureName = regexp.MustCompile(`^[a-f0-9]{64}\.(png|jpe?g|gif|webp)$`)

// pictures is the directory the pictures of the window are kept in.
type pictures struct{ dir string }

// keep writes data as a picture of the profile and returns its name: the
// same data always gets the same name. What is no PNG, JPEG, GIF or WebP, or
// larger than maxPictureBytes, is refused.
func (p pictures) keep(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("the picture is empty")
	}
	if len(data) > maxPictureBytes {
		return "", fmt.Errorf("the picture exceeds %d MiB", maxPictureBytes>>20)
	}
	ext, ok := pictureExt(data)
	if !ok {
		return "", errors.New("supported pictures are PNG, JPEG, GIF and WebP")
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:]) + ext
	path := filepath.Join(p.dir, name)
	if _, err := os.Stat(path); err == nil {
		return name, nil
	}
	if err := os.MkdirAll(p.dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(p.dir, ".picture-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // once it is renamed, there is nothing left to remove
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return name, nil
}

// pictureExt names the extension of the picture in data, and reports whether
// it is one the window shows. What the content is decides, not the name of
// the file it came from.
func pictureExt(data []byte) (string, bool) {
	switch http.DetectContentType(data) {
	case "image/png":
		return ".png", true
	case "image/jpeg":
		return ".jpg", true
	case "image/gif":
		return ".gif", true
	case "image/webp":
		return ".webp", true
	}
	return "", false
}

// handler serves the pictures of the window under mediaPath, and nothing
// else. The window serves it beside its own files, the page of the thread
// behind its login: the same app shows the same <img> in both.
func (p pictures) handler() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		name, ok := strings.CutPrefix(r.URL.Path, mediaPath)
		if !ok || !pictureName.MatchString(name) {
			http.NotFound(rw, r)
			return
		}
		f, err := os.Open(filepath.Join(p.dir, name))
		if err != nil {
			http.NotFound(rw, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(rw, r)
			return
		}
		rw.Header().Set("Content-Type", pictureType(name))
		// The name is the content: what is under it never changes.
		rw.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		http.ServeContent(rw, r, name, info.ModTime(), f)
	})
}

// pictureType is the media type of the picture name, which pictureName allows.
func pictureType(name string) string {
	switch filepath.Ext(name) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	}
	return "image/webp"
}

// pictureOutput offers the window as a channel for the pictures an agent
// shows, beside the other gateways: the shared gateway_send_image tool reaches
// it, with the same checks as the others, and the picture appears in the
// thread the window shows.
func pictureOutput(sessions plugin.Sessions, files *guard.Guard, store pictures) plugin.GatewayOutput {
	return plugin.GatewayOutput{
		Name: "gui",
		ImageDescription: "Shows the picture in the window of ouro, in the thread it shows. " +
			"Destination is not used. It stays there while the thread is read, as long as the profile keeps it.",
		SendImage: func(ctx context.Context, request plugin.GatewayImageRequest) (string, error) {
			return showPicture(ctx, sessions, files, store, request)
		},
	}
}

// showPicture shows the picture a call of gateway_send_image asks for: the
// file at its path, or an attachment of the conversation, kept in the profile
// and told to the thread the window shows.
func showPicture(ctx context.Context, sessions plugin.Sessions, files *guard.Guard, store pictures, request plugin.GatewayImageRequest) (string, error) {
	if _, peer := plugin.OriginFrom(ctx); peer {
		return "", errors.New("showing pictures is owner-only")
	}
	if (request.Path == "") == (request.Attachment == 0) {
		return "", errors.New("give either a path or an attachment")
	}
	id := plugin.ContextID(ctx)
	if id == "" {
		return "", errors.New("a picture is shown while an answer is on its way, not outside of one")
	}
	var data []byte
	var err error
	if request.Path != "" {
		data, err = readPicture(ctx, files, request.Path)
	} else {
		images := plugin.InputImages(ctx)
		if request.Attachment < 1 || request.Attachment > len(images) {
			return "", errors.New("no such image attachment in this conversation")
		}
		data = images[request.Attachment-1].Data
	}
	if err != nil {
		return "", err
	}
	name, err := store.keep(data)
	if err != nil {
		return "", err
	}
	shown, ok := sessions.(plugin.PictureSessions)
	if !ok {
		return "", errors.New("this node cannot show pictures in the window")
	}
	// The window shows the thread, also while the answer that made the
	// picture works in a session of its own, which the window does not show:
	// there the picture appears, and there it is kept with the answer it was
	// shown in.
	if err := shown.ShowPicture(ctx, plugin.ThreadID, name); err != nil {
		return "", err
	}
	result, _ := json.Marshal(struct {
		Picture string `json:"picture"`
		Bytes   int    `json:"bytes"`
	}{name, len(data)})
	return string(result), nil
}

// readPicture reads the picture at path as far as the rules of files let the
// tool read it: a regular file of a kind the window shows, of at most
// maxPictureBytes.
func readPicture(ctx context.Context, files *guard.Guard, path string) ([]byte, error) {
	if files == nil {
		return nil, errors.New("the file access of the window is unavailable")
	}
	checker, err := files.Checker(guard.Access{Tool: plugin.GatewaySendImageTool, Directory: workdir.Of(ctx)})
	if err != nil {
		return nil, err
	}
	path, err = checker.Check(guard.Read, path)
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
		return nil, errors.New("the picture must be a regular file")
	}
	if info.Size() > maxPictureBytes {
		return nil, fmt.Errorf("the picture exceeds %d MiB", maxPictureBytes>>20)
	}
	return io.ReadAll(io.LimitReader(f, maxPictureBytes+1))
}
