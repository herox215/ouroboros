// Package media names the files the owner and an agent exchange: the pictures
// every front end and every gateway shows, and how large one may be.
//
// It holds no wire format and reads no picture: what the bytes are is the only
// thing that decides, never the name of the file they came from.
package media

import (
	"net/http"
	"path/filepath"
	"strings"
)

// MaxPictureBytes is how large a picture the front ends and the gateways take:
// 10 MiB, which leaves room for base64 within the 16 MiB of a session socket
// request.
const MaxPictureBytes = 10 << 20

// Kind names the media type of data when it is a picture the window, the page,
// the gateways and the models take, and "" when it is none of them. The kinds
// are what http.DetectContentType reads of the first bytes.
func Kind(data []byte) string {
	kind := http.DetectContentType(data)
	if !IsPicture(kind) {
		return ""
	}
	return kind
}

// IsPicture reports whether kind is one of the pictures the front ends and the
// gateways take: PNG, JPEG, GIF or WebP.
func IsPicture(kind string) bool {
	switch kind {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// Ext is the file extension of a picture kind, "." included, and "" for a kind
// that is no picture.
func Ext(kind string) string {
	switch kind {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ""
}

// KindOfName is the media type a file name says its picture is, and "" for a
// name whose extension is no picture. What the file holds is what Kind says;
// this is for serving a file that was already checked when it was kept.
func KindOfName(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return ""
}
