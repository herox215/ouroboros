package matrix

import (
	"bytes"
	"testing"

	transport "ouroboros/internal/matrix"
)

func TestImagesOfKeepsTypeAndData(t *testing.T) {
	got := imagesOf(transport.Message{Images: []transport.Image{
		{MIMEType: "image/png", Data: []byte{1, 2}},
		{MIMEType: "image/jpeg", Data: []byte{3}},
	}})
	if len(got) != 2 || got[0].Type != "image/png" || !bytes.Equal(got[0].Data, []byte{1, 2}) {
		t.Fatalf("first image %+v", got)
	}
	if got[1].Type != "image/jpeg" || !bytes.Equal(got[1].Data, []byte{3}) {
		t.Fatalf("second image %+v", got)
	}
	if images := imagesOf(transport.Message{}); len(images) != 0 {
		t.Fatalf("images of a text message: %+v", images)
	}
}
