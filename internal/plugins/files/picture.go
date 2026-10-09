package files

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif" // a GIF is decoded as well
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"ouroboros/plugin"
)

// The limits of a picture that read shows the LLM.
const (
	maxPictureBytes = 20 << 20 // of the file
	maxShownBytes   = 5 << 20  // of what is sent
)

// effortSizes are the longer sides a picture is made smaller to for the
// effort of the call, when it names no size: with another effort, as with
// none or the most, a picture is shown in its own size.
var effortSizes = map[string]int{"none": 512, "minimal": 512, "low": 768, "medium": 1024, "high": 1568}

// isPicture reports whether read shows the file at path to the LLM as a
// picture. An SVG is text, and is read as such.
func isPicture(path string) bool {
	return plugin.IsPicture(path) && strings.ToLower(filepath.Ext(path)) != ".svg"
}

// readPicture shows the LLM the picture in f, the file args.Path names, and
// returns what it is in words. It is shown as it is, in its own size, unless
// args asks for a smaller size or a region of it, or the effort of the call
// for a smaller size, see effortSizes. The APIs take PNG, JPEG, GIF and
// WebP: another kind is an error, and a WebP is only shown as it is.
func readPicture(ctx context.Context, f io.Reader, args readArgs, size int64) (string, error) {
	path := args.Path
	if size > maxPictureBytes {
		return "", fmt.Errorf("%s has %d MiB: a picture of at most %d MiB can be shown", path, size>>20, maxPictureBytes>>20)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	kind := http.DetectContentType(data)
	about := kind                                       // what the LLM is told
	remark := `picture "` + escapes.Replace(path) + `"` // and the owner
	switch kind {
	case "image/png", "image/jpeg", "image/gif":
		// Decoded also to be shown as it is: what the API cannot read fails
		// the whole answer.
		src, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return "", fmt.Errorf("%s cannot be read as a picture: %w", path, err)
		}
		all := fmt.Sprintf("%d×%d", src.Bounds().Dx(), src.Bounds().Dy())
		about += ", " + all
		effort := plugin.Effort(ctx)
		byEffort := args.Size <= 0 && effortSizes[effort] > 0
		if byEffort {
			args.Size = effortSizes[effort]
		}
		if args.Region == "" && (args.Size <= 0 || max(src.Bounds().Dx(), src.Bounds().Dy()) <= args.Size) {
			remark += " " + all
			break
		}
		var part image.Rectangle
		var w, h int
		if data, kind, part, w, h, err = reshape(src, kind, args); err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		shown := fmt.Sprintf("%d×%d", w, h)
		if args.Region != "" {
			about += fmt.Sprintf("; shown: the region %d,%d,%d,%d at %s", part.Min.X, part.Min.Y, part.Dx(), part.Dy(), shown)
			remark += " a part " + shown + " of " + all
		} else {
			about += "; shown: " + shown
			remark += " " + shown + " of " + all
		}
		if byEffort && max(w, h) < max(part.Dx(), part.Dy()) {
			about += fmt.Sprintf(", smaller for the effort %s: ask with size to see more", effort)
			remark += " (effort " + effort + ")"
		}
	case "image/webp":
		if args.Size > 0 || args.Region != "" {
			return "", fmt.Errorf("%s is a WebP, which is only shown as it is: leave out size and region", path)
		}
	default:
		return "", fmt.Errorf("%s is a picture the model cannot be shown (%s): only PNG, JPEG, GIF and WebP; convert it first", path, kind)
	}
	if len(data) > maxShownBytes {
		return "", fmt.Errorf("%s is %d MiB as it is shown, more than the %d MiB a picture can have: ask for a smaller size or a region", path, len(data)>>20, maxShownBytes>>20)
	}
	if !plugin.ShowImage(ctx, plugin.Image{Type: kind, Data: data}) {
		return "", fmt.Errorf("%s is a picture, and pictures cannot be shown here", path)
	}
	plugin.Remark(ctx, remark)
	return fmt.Sprintf("%s is a picture (%s). It is shown to you as an image.", path, about), nil
}

// escapes are what a path in double quotes has before a quote and a
// backslash of its own.
var escapes = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// reshape returns the picture src of the kind kind as args asks for it:
// only its region, and with its longer side at most args.Size, never larger
// than it is. It returns the part of src that is shown and how large, and is
// a JPEG if it was one, else a PNG.
func reshape(src image.Image, kind string, args readArgs) ([]byte, string, image.Rectangle, int, int, error) {
	part := src.Bounds()
	if args.Region != "" {
		var err error
		if part, err = region(args.Region, part); err != nil {
			return nil, "", part, 0, 0, err
		}
	}
	w, h := part.Dx(), part.Dy()
	if args.Size > 0 && max(w, h) > args.Size {
		if w >= h {
			w, h = args.Size, max(1, h*args.Size/w)
		} else {
			w, h = max(1, w*args.Size/h), args.Size
		}
	}
	dst := shrink(src, part, w, h)
	var out bytes.Buffer
	if kind == "image/jpeg" {
		jpeg.Encode(&out, dst, &jpeg.Options{Quality: 90}) // into memory: cannot fail
		return out.Bytes(), kind, part, w, h, nil
	}
	png.Encode(&out, dst) // into memory: cannot fail
	return out.Bytes(), "image/png", part, w, h, nil
}

// region is the part "x,y,width,height" of a picture with the bounds all,
// as far as it is within them.
func region(text string, all image.Rectangle) (image.Rectangle, error) {
	fields := strings.Split(text, ",")
	var n [4]int
	ok := len(fields) == len(n)
	for i := 0; ok && i < len(n); i++ {
		var err error
		n[i], err = strconv.Atoi(strings.TrimSpace(fields[i]))
		ok = err == nil
	}
	if !ok || n[2] <= 0 || n[3] <= 0 {
		return image.Rectangle{}, fmt.Errorf(`the region %q is no "x,y,width,height" with a width and a height above 0`, text)
	}
	part := image.Rect(n[0], n[1], n[0]+n[2], n[1]+n[3]).Intersect(all)
	if part.Empty() {
		return image.Rectangle{}, fmt.Errorf("the region %s is outside of the picture, which is %d×%d", text, all.Dx(), all.Dy())
	}
	return part, nil
}

// shrink returns the part b of src made w×h pixels, no larger than it is:
// every pixel is the mean of those of src it covers.
func shrink(src image.Image, b image.Rectangle, w, h int) *image.RGBA {
	in := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(in, in.Bounds(), src, b.Min, draw.Src)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		y0, y1 := y*b.Dy()/h, max((y+1)*b.Dy()/h, y*b.Dy()/h+1)
		for x := range w {
			x0, x1 := x*b.Dx()/w, max((x+1)*b.Dx()/w, x*b.Dx()/w+1)
			var sum [4]int
			for sy := y0; sy < y1; sy++ {
				row := in.Pix[sy*in.Stride+x0*4 : sy*in.Stride+x1*4]
				for i := 0; i < len(row); i += 4 {
					sum[0] += int(row[i])
					sum[1] += int(row[i+1])
					sum[2] += int(row[i+2])
					sum[3] += int(row[i+3])
				}
			}
			n := (y1 - y0) * (x1 - x0)
			at := out.PixOffset(x, y)
			for c := range sum {
				out.Pix[at+c] = uint8(sum[c] / n)
			}
		}
	}
	return out
}
