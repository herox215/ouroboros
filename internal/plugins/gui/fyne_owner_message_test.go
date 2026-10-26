//go:build cgo

package gui

import (
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
)

func TestOwnerMessagesStayOnTheRight(t *testing.T) {
	widgetApp(t, "light")
	c := newFyneChat(&fyneUI{}, 0, "test", false)
	for _, kind := range []string{"user", "queued", "assistant"} {
		for _, body := range []string{"Hello", strings.Repeat("Long message ", 60)} {
			message := c.message(kind, "Speaker", body, time.Time{})
			message.item.Resize(fyne.NewSize(600, 150))
			if kind == "assistant" {
				if message.box.Position().X != 0 {
					t.Fatal("assistant message moved away from the left")
				}
				continue
			}
			outer := message.item.(*fyne.Container)
			inner := outer.Objects[0]
			if inner.Position().X <= 0 || inner.Position().X+inner.Size().Width != 600 {
				t.Fatalf("%s message is not right-aligned: position %v, size %v", kind, inner.Position(), inner.Size())
			}
			outer.Resize(fyne.NewSize(240, 150))
			if inner.Size().Width > 192 || inner.Position().X+inner.Size().Width != 240 {
				t.Fatalf("%s message does not fit a narrow chat: %v %v", kind, inner.Position(), inner.Size())
			}
		}
	}
}
