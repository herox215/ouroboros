package files

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"strings"
	"testing"
	"testing/iotest"

	"ouroboros/internal/agent"
	"ouroboros/internal/openai"
)

// picture is a w×h picture of one color, encoded by encode.
func picture(t *testing.T, w, h int, encode func(io.Writer, image.Image) error) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var b bytes.Buffer
	if err := encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func encodeJPEG(w io.Writer, img image.Image) error { return jpeg.Encode(w, img, nil) }
func encodeGIF(w io.Writer, img image.Image) error  { return gif.Encode(w, img, nil) }

// llmLooking is an LLM that reads a file with the tool read, and keeps the
// result it gets.
type llmLooking struct {
	args   readArgs
	result openai.Message
}

func (l *llmLooking) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	last := msgs[len(msgs)-1]
	if last.Role == "user" {
		args, _ := json.Marshal(l.args)
		return openai.Message{ToolCalls: []openai.ToolCall{{ID: "c1", Type: "function", Function: openai.FunctionCall{Name: Read, Arguments: string(args)}}}}, nil
	}
	l.result = last
	return openai.Message{Content: "seen"}, nil
}

// look has an agent read path and returns the result it gets, with the
// pictures it is shown.
func (w *world) look(path string) openai.Message {
	w.t.Helper()
	return w.lookAt(readArgs{Path: path})
}

// lookAt is look with the arguments args.
func (w *world) lookAt(args readArgs) openai.Message {
	w.t.Helper()
	for _, tool := range tools(w.guard) {
		if tool.Name != Read {
			continue
		}
		tool, _ = tool.Configure(json.RawMessage(`{}`))
		llm := &llmLooking{args: args}
		w.remark = ""
		observe := agent.Observer{Step: func(s agent.Step) { w.remark = s.Remark }}
		if _, err := agent.New(llm, "m", "sys", tool).Run(openai.WithEffort(context.Background(), w.effort), "c", "look", observe); err != nil {
			w.t.Fatal(err)
		}
		return llm.result
	}
	return openai.Message{}
}

// halves is a w×h PNG, red on its left half and blue on its right.
func halves(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{255, 0, 0, 255}
			if x >= w/2 {
				c = color.RGBA{0, 0, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.String()
}

// read shows the LLM a picture as an image, as it is, and says in words
// what it is.
func TestReadShowsPictures(t *testing.T) {
	w := newWorld(t)
	wide := picture(t, 3000, 1000, png.Encode)
	w.file("~/wide.PNG", wide)
	tall := picture(t, 10, 20, encodeJPEG)
	w.file("~/tall.jpg", tall)
	moving := picture(t, 16, 10, encodeGIF)
	w.file("~/moving.gif", moving)
	webp := "RIFF\x00\x00\x00\x00WEBPVP8 rest"
	w.file("~/photo.webp", webp)

	for _, tt := range []struct{ path, data, kind, about string }{
		{"~/wide.PNG", wide, "image/png", "image/png, 3000×1000"},
		{"~/tall.jpg", tall, "image/jpeg", "image/jpeg, 10×20"},
		{"~/moving.gif", moving, "image/gif", "image/gif, 16×10"},
		{"~/photo.webp", webp, "image/webp", "image/webp"},
	} {
		got := w.look(tt.path)
		if want := tt.path + " is a picture (" + tt.about + "). It is shown to you as an image."; got.Content != want {
			t.Errorf("%s: %q, want %q", tt.path, got.Content, want)
		}
		if len(got.Images) != 1 || got.Images[0].Type != tt.kind || string(got.Images[0].Data) != tt.data {
			t.Errorf("%s: should be shown as it is", tt.path)
		}
	}
	// The owner is told of a WebP, whose size is not read, only that it is
	// one; a path is in quotes as in a message.
	w.file(`~/my "photo".webp`, webp)
	if w.look(`~/my "photo".webp`); w.remark != `picture "~/my \"photo\".webp"` {
		t.Errorf("the owner is told %q", w.remark)
	}
}

// With size a picture is shown smaller, never larger, and with region only
// a part of it, as far as it is within the picture.
func TestReadShowsPicturesSmallerOrAPart(t *testing.T) {
	w := newWorld(t)
	w.file("~/halves.png", halves(t, 400, 200))
	w.file("~/tall.jpg", picture(t, 100, 200, encodeJPEG))
	w.file("~/line.png", picture(t, 4000, 1, png.Encode))
	w.file("~/moving.gif", picture(t, 16, 10, encodeGIF))

	for _, tt := range []struct {
		args  readArgs
		kind  string
		shown string
		w, h  int
	}{
		{readArgs{Size: 100}, "image/png", "image/png, 400×200; shown: 100×50", 100, 50},
		{readArgs{Size: 1000}, "image/png", "image/png, 400×200", 400, 200}, // as it is
		{readArgs{Region: "200,0,200,200"}, "image/png", "image/png, 400×200; shown: the region 200,0,200,200 at 200×200", 200, 200},
		{readArgs{Region: " 300, 100, 500, 500 ", Size: 50}, "image/png", "image/png, 400×200; shown: the region 300,100,100,100 at 50×50", 50, 50},
		{readArgs{Path: "~/tall.jpg", Size: 50}, "image/jpeg", "image/jpeg, 100×200; shown: 25×50", 25, 50},
		{readArgs{Path: "~/line.png", Size: 100}, "image/png", "image/png, 4000×1; shown: 100×1", 100, 1},
		{readArgs{Path: "~/moving.gif", Size: 8}, "image/png", "image/gif, 16×10; shown: 8×5", 8, 5},
	} {
		tt.args.Path = cmp.Or(tt.args.Path, "~/halves.png")
		got := w.lookAt(tt.args)
		if want := tt.args.Path + " is a picture (" + tt.shown + "). It is shown to you as an image."; got.Content != want {
			t.Errorf("%+v: %q, want %q", tt.args, got.Content, want)
			continue
		}
		shown, kind, err := image.Decode(bytes.NewReader(got.Images[0].Data))
		if err != nil || got.Images[0].Type != tt.kind || "image/"+kind != tt.kind || shown.Bounds().Dx() != tt.w || shown.Bounds().Dy() != tt.h {
			t.Errorf("%+v: shown %s %v, %v", tt.args, got.Images[0].Type, shown.Bounds(), err)
		}
	}

	// The region is the part it names.
	right := w.lookAt(readArgs{Path: "~/halves.png", Region: "200,0,200,200"})
	shown, _, _ := image.Decode(bytes.NewReader(right.Images[0].Data))
	if r, _, b, _ := shown.At(0, 0).RGBA(); r != 0 || b == 0 {
		t.Errorf("the right half should be blue")
	}
}

// Without a size a picture is made smaller for the effort of the answer, and
// the LLM is told how to see more; with a size or the effort that is the
// most, or none, it is not. A region is made smaller as well.
func TestReadShowsPicturesForTheEffort(t *testing.T) {
	w := newWorld(t)
	wide := picture(t, 3000, 1500, png.Encode)
	w.file("~/wide.png", wide)
	more := ", smaller for the effort %s: ask with size to see more"
	for _, tt := range []struct {
		effort        string
		args          readArgs
		shown, remark string
	}{
		{"", readArgs{}, "", "3000×1500"},
		{"max", readArgs{}, "", "3000×1500"},
		{"high", readArgs{}, "; shown: 1568×784" + fmt.Sprintf(more, "high"), "1568×784 of 3000×1500 (effort high)"},
		{"medium", readArgs{}, "; shown: 1024×512" + fmt.Sprintf(more, "medium"), "1024×512 of 3000×1500 (effort medium)"},
		{"low", readArgs{}, "; shown: 768×384" + fmt.Sprintf(more, "low"), "768×384 of 3000×1500 (effort low)"},
		{"none", readArgs{}, "; shown: 512×256" + fmt.Sprintf(more, "none"), "512×256 of 3000×1500 (effort none)"},
		{"none", readArgs{Size: 3000}, "", "3000×1500"},
		{"none", readArgs{Size: 100}, "; shown: 100×50", "100×50 of 3000×1500"},
		{"none", readArgs{Region: "0,0,2000,1000"}, "; shown: the region 0,0,2000,1000 at 512×256" + fmt.Sprintf(more, "none"), "a part 512×256 of 3000×1500 (effort none)"},
		{"none", readArgs{Region: "0,0,200,100"}, "; shown: the region 0,0,200,100 at 200×100", "a part 200×100 of 3000×1500"},
	} {
		w.effort = tt.effort
		tt.args.Path = "~/wide.png"
		got := w.lookAt(tt.args)
		if want := "~/wide.png is a picture (image/png, 3000×1500" + tt.shown + "). It is shown to you as an image."; got.Content != want {
			t.Errorf("%q %+v: %q, want %q", tt.effort, tt.args, got.Content, want)
		}
		if want := `picture "~/wide.png" ` + tt.remark; w.remark != want {
			t.Errorf("%q %+v: the owner is told %q, want %q", tt.effort, tt.args, w.remark, want)
		}
		if tt.shown == "" && string(got.Images[0].Data) != wide {
			t.Errorf("%q %+v: should be shown as it is", tt.effort, tt.args)
		}
	}
}

// An SVG is text and read as such.
func TestReadSVGAsText(t *testing.T) {
	w := newWorld(t)
	w.file("~/logo.svg", "<svg/>\n")
	if got, err := w.run(owner, Read, readArgs{Path: "~/logo.svg"}); err != nil || got != "<svg/>\n" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// A picture that cannot be shown is an error that says why.
func TestReadPicturesThatCannotBeShown(t *testing.T) {
	w := newWorld(t)
	w.file("~/small.png", picture(t, 4, 4, png.Encode))
	w.file("~/old.bmp", "BM rest")
	w.file("~/broken.png", "\x89PNG\r\n\x1a\nbroken")
	wide := picture(t, 3000, 1000, png.Encode)
	w.file("~/cut.png", wide[:len(wide)/2])
	w.file("~/huge.webp", "RIFF\x00\x00\x00\x00WEBPVP8 "+strings.Repeat("x", maxShownBytes))
	w.file("~/giant.png", "")
	if err := os.Truncate(w.path("~/giant.png"), maxPictureBytes+1); err != nil {
		t.Fatal(err)
	}
	// As large as a picture may be, it is looked at: these fail for what they are.
	w.file("~/largest.png", "")
	if err := os.Truncate(w.path("~/largest.png"), maxPictureBytes); err != nil {
		t.Fatal(err)
	}
	header := "RIFF\x00\x00\x00\x00WEBPVP8 "
	w.file("~/full.webp", header+strings.Repeat("x", maxShownBytes-len(header)))
	if got := w.look("~/full.webp"); len(got.Images) != 1 || len(got.Images[0].Data) != maxShownBytes {
		t.Errorf("a picture of all the bytes one may have: %q", got.Content)
	}

	for path, want := range map[string]string{
		"~/old.bmp":     "~/old.bmp is a picture the model cannot be shown (image/bmp): only PNG, JPEG, GIF and WebP",
		"~/broken.png":  "~/broken.png cannot be read as a picture",
		"~/cut.png":     "~/cut.png cannot be read as a picture",
		"~/huge.webp":   "~/huge.webp is 5 MiB as it is shown, more than the 5 MiB a picture can have: ask for a smaller size or a region",
		"~/giant.png":   "~/giant.png has 20 MiB: a picture of at most 20 MiB can be shown",
		"~/largest.png": "~/largest.png is a picture the model cannot be shown (application/octet-stream)",
	} {
		got := w.look(path)
		if !strings.Contains(got.Content, "error: "+want) || got.Images != nil {
			t.Errorf("%s: %q, want %q", path, got.Content, want)
		}
	}
	for _, tt := range []struct {
		args readArgs
		want string
	}{
		{readArgs{Path: "~/huge.webp", Size: 10}, "~/huge.webp is a WebP, which is only shown as it is: leave out size and region"},
		{readArgs{Path: "~/huge.webp", Region: "0,0,1,1"}, "~/huge.webp is a WebP"},
		{readArgs{Path: "~/small.png", Region: "0,0,4"}, `~/small.png: the region "0,0,4" is no "x,y,width,height"`},
		{readArgs{Path: "~/small.png", Region: "0,0,4,x"}, `~/small.png: the region "0,0,4,x" is no`},
		{readArgs{Path: "~/small.png", Region: "0,0,0,4"}, `~/small.png: the region "0,0,0,4" is no`},
		{readArgs{Path: "~/small.png", Region: "0,0,4,0"}, `~/small.png: the region "0,0,4,0" is no`},
		{readArgs{Path: "~/small.png", Region: "0,0,4,-1"}, `~/small.png: the region "0,0,4,-1" is no`},
		{readArgs{Path: "~/small.png", Region: "4,0,4,4"}, "~/small.png: the region 4,0,4,4 is outside of the picture, which is 4×4"},
	} {
		got := w.lookAt(tt.args)
		if !strings.Contains(got.Content, "error: "+tt.want) || got.Images != nil {
			t.Errorf("%+v: %q, want %q", tt.args, got.Content, tt.want)
		}
	}

	// Outside of an answer it goes nowhere.
	got, err := w.run(owner, Read, readArgs{Path: "~/small.png"})
	fails(t, got, err, "~/small.png is a picture, and pictures cannot be shown here")

	// A file that cannot be read to its end is an error.
	open = func(string) (io.ReadCloser, error) { return io.NopCloser(iotest.ErrReader(errTest)), nil }
	defer func() { open = func(path string) (io.ReadCloser, error) { return os.Open(path) } }()
	if got, err := w.run(owner, Read, readArgs{Path: "~/small.png"}); !errors.Is(err, errTest) || got != "" {
		t.Errorf("a failing file: %q, %v", got, err)
	}
}

// shrink makes every pixel the mean of those it covers.
func TestShrink(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for x, v := range []uint8{0, 100, 200, 250} {
		for y := range 2 {
			src.Set(x, y, color.RGBA{v, v / 2, 0, 255})
		}
	}
	got := shrink(src, src.Bounds(), 2, 1)
	want := []uint8{50, 25, 0, 255, 225, 112, 0, 255}
	if !bytes.Equal(got.Pix, want) {
		t.Fatalf("shrunk to %v, want %v", got.Pix, want)
	}
	// Of a part, only what it covers.
	if got := shrink(src, image.Rect(2, 0, 4, 2), 1, 1); !bytes.Equal(got.Pix, []uint8{225, 112, 0, 255}) {
		t.Fatalf("the part shrunk to %v", got.Pix)
	}
	// Rows are taken together like columns.
	tall := image.NewRGBA(image.Rect(0, 0, 2, 4))
	for y, v := range []uint8{0, 100, 200, 250} {
		for x := range 2 {
			tall.Set(x, y, color.RGBA{v, v / 2, 0, 255})
		}
	}
	if got := shrink(tall, tall.Bounds(), 1, 2); !bytes.Equal(got.Pix, want) {
		t.Fatalf("rows shrunk to %v, want %v", got.Pix, want)
	}
	// Three of four pixels each way: none is left out, and none counts twice.
	if got := shrink(tall, tall.Bounds(), 2, 3); !bytes.Equal(got.Pix, []uint8{0, 0, 0, 255, 0, 0, 0, 255, 100, 50, 0, 255, 100, 50, 0, 255, 225, 112, 0, 255, 225, 112, 0, 255}) {
		t.Fatalf("to three rows: %v", got.Pix)
	}
}
