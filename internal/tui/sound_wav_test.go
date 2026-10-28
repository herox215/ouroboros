package tui

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// chunk is one RIFF chunk: its id, its size and its body.
func chunk(id string, body []byte) []byte {
	out := append([]byte(id), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[4:], uint32(len(body)))
	out = append(out, body...)
	if len(body)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

// wavFormat is the body of a fmt chunk of 16 bytes.
func wavFormat(format, channels, rate, align, bits int) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint16(b, uint16(format))
	binary.LittleEndian.PutUint16(b[2:], uint16(channels))
	binary.LittleEndian.PutUint32(b[4:], uint32(rate))
	binary.LittleEndian.PutUint32(b[8:], uint32(rate*align))
	binary.LittleEndian.PutUint16(b[12:], uint16(align))
	binary.LittleEndian.PutUint16(b[14:], uint16(bits))
	return b
}

// riff is a WAVE file of the given chunks.
func riff(chunks ...[]byte) []byte {
	body := []byte("WAVE")
	for _, c := range chunks {
		body = append(body, c...)
	}
	return chunk("RIFF", body)
}

// failingAt reads data, but fails every read that reaches from.
type failingAt struct {
	data []byte
	from int64
}

func (f failingAt) ReadAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > f.from {
		return 0, errors.New("the disk is gone")
	}
	return bytes.NewReader(f.data).ReadAt(p, off)
}

// A WAV of float samples in two channels, its format named in the
// extensible way and after a chunk of odd size, reads like any other.
func TestDecodeWAVReadsExtensibleFloatStereo(t *testing.T) {
	format := append(wavFormat(0xFFFE, 2, 100, 8, 32), make([]byte, 24)...)
	binary.LittleEndian.PutUint16(format[24:], 3) // the subformat: float
	samples := make([]byte, 0, 800)
	for i := 0; i < 100; i++ {
		var frame [8]byte
		binary.LittleEndian.PutUint32(frame[:], math.Float32bits(float32(i)/200))
		binary.LittleEndian.PutUint32(frame[4:], math.Float32bits(-float32(i)/100))
		samples = append(samples, frame[:]...)
	}
	data := riff(chunk("LIST", []byte("odd")), chunk("fmt ", format), chunk("data", samples))
	clip, ok := decodeWAV(bytes.NewReader(data), int64(len(data)), 4)
	if !ok || clip.Err != nil {
		t.Fatalf("the WAV reads as %+v, %v", clip, ok)
	}
	if clip.Duration != time.Second || clip.Peaks[3] != 1 {
		t.Fatalf("the WAV reads as %s with %v", clip.Duration, clip.Peaks)
	}
}

// A WAV whose format is missing, unknown or broken, or that holds no
// samples, says so.
func TestDecodeWAVSaysWhatIsWrongWithAFile(t *testing.T) {
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{
		{"short format", riff(chunk("fmt ", make([]byte, 8)), chunk("data", []byte{1, 2})), "no WAV format"},
		{"no data", riff(chunk("fmt ", wavFormat(1, 1, 8000, 2, 16))), "no WAV format"},
		{"compressed", riff(chunk("fmt ", wavFormat(2, 1, 8000, 2, 16)), chunk("data", []byte{1, 2})), "unsupported WAV encoding 2"},
		{"no channels", riff(chunk("fmt ", wavFormat(1, 0, 8000, 2, 16)), chunk("data", []byte{1, 2})), "invalid WAV format"},
		{"empty", riff(chunk("fmt ", wavFormat(1, 1, 8000, 2, 16)), chunk("data", nil)), "WAV holds no samples"},
	} {
		clip, ok := decodeWAV(bytes.NewReader(c.data), int64(len(c.data)), 4)
		if !ok || clip.Err == nil || !strings.Contains(clip.Err.Error(), c.want) {
			t.Fatalf("%s: read as %+v, want %q", c.name, clip, c.want)
		}
	}
}

// A disk that fails while the file is walked leaves what was read: a
// format it could not read is none, and samples it could not read are
// silence after what came before.
func TestDecodeWAVKeepsWhatItReadBeforeAFailure(t *testing.T) {
	data := riff(chunk("fmt ", wavFormat(1, 1, 8000, 2, 16)), chunk("data", []byte{0, 0x40, 0, 0x40}))
	for _, from := range []int64{20, 28} { // the first chunk, the body of the format
		if clip, _ := decodeWAV(failingAt{data, from}, int64(len(data)), 4); clip.Err == nil {
			t.Fatalf("a failure at %d read as %+v", from, clip)
		}
	}
	// The file claims more than the disk gives: the samples stop early.
	binary.LittleEndian.PutUint32(data[len(data)-8:], 104)
	clip, ok := decodeWAV(bytes.NewReader(data), int64(len(data))+100, 2)
	if !ok || clip.Err != nil || clip.Peaks[0] != 1 || clip.Peaks[1] != 0 {
		t.Fatalf("the cut file read as %+v", clip)
	}
}

// A sound asked for no columns is read into one, and a file that cannot
// be opened says why.
func TestAnalyzeSoundTakesAtLeastOneColumn(t *testing.T) {
	path := tone(t, 0.1, 8000, func(int) int16 { return 1000 })
	if clip := analyzeSound(path, 0); clip.Err != nil || len(clip.Peaks) != 1 {
		t.Fatalf("no columns read as %+v", clip)
	}
	if os.Geteuid() == 0 {
		t.Skip("root opens every file")
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if clip := analyzeSound(path, 4); clip.Err == nil {
		t.Fatal("a file that cannot be opened read as a sound")
	}
}

// Every depth of sample reads as its distance from silence.
func TestAbsSampleReadsEveryDepth(t *testing.T) {
	f64 := make([]byte, 8)
	binary.LittleEndian.PutUint64(f64, math.Float64bits(-0.5))
	i32 := make([]byte, 4)
	binary.LittleEndian.PutUint32(i32, uint32(1<<30))
	for _, c := range []struct {
		b        []byte
		bits     int
		floating bool
		want     float32
	}{
		{f64, 64, true, 0.5},
		{[]byte{0}, 8, false, 1},
		{[]byte{0, 0, 0xC0}, 24, false, 0.5},
		{[]byte{0, 0, 0x40}, 24, false, 0.5},
		{i32, 32, false, 0.5},
		{[]byte{1, 2}, 12, false, 0},
	} {
		if got := absSample(c.b, c.bits, c.floating); got != c.want {
			t.Fatalf("%d bits %v read as %f, want %f", c.bits, c.b, got, c.want)
		}
	}
}

// fakeTools puts ffprobe and ffmpeg on the path as shell scripts of the
// given bodies, a tool left out when its body is "". Nothing else is on
// the path, so the scripts use only what the shell has built in.
func fakeTools(t *testing.T, probe, decode string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"ffprobe": probe, "ffmpeg": decode} {
		if body == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// Without ffmpeg and ffprobe a format that is no WAV says what it needs.
func TestReadWithToolsNeedsBothTools(t *testing.T) {
	fakeTools(t, "", "")
	if clip := analyzeSound("song.mp3", 4); clip.Err == nil || !strings.Contains(clip.Err.Error(), "needs ffmpeg") {
		t.Fatalf("without the tools the sound reads as %+v", clip)
	}
	fakeTools(t, "echo 1", "")
	if clip := analyzeSound("song.mp3", 4); clip.Err == nil || !strings.Contains(clip.Err.Error(), "needs ffmpeg") {
		t.Fatalf("without ffmpeg the sound reads as %+v", clip)
	}
}

// The length comes from ffprobe and the shape from ffmpeg: a sound longer
// than the samples ffmpeg gave spreads them over the whole of it and says
// that the shape is partial.
func TestReadWithToolsSpreadsTheShapeOverTheLength(t *testing.T) {
	fakeTools(t, "echo 2.5", `printf '\000\100\000\040'`)
	clip := analyzeSound("song.mp3", 4)
	if clip.Err != nil || clip.Duration != 2500*time.Millisecond || !clip.Partial {
		t.Fatalf("the sound reads as %+v", clip)
	}
	if clip.Peaks[0] != 1 || clip.Peaks[3] != 0 {
		t.Fatalf("the peaks are %v", clip.Peaks)
	}
}

// Without a length from ffprobe the sound is as long as its samples.
func TestReadWithToolsTakesTheLengthOfTheSamples(t *testing.T) {
	for _, probe := range []string{"exit 1", "echo unknown"} {
		fakeTools(t, probe, `printf '\000\000\000\000'`)
		clip := analyzeSound("song.ogg", 2)
		if clip.Err != nil || clip.Duration != 500*time.Microsecond || clip.Partial {
			t.Fatalf("with %q the sound reads as %+v", probe, clip)
		}
	}
}

// A decoder that fails says why, in its own words when it has some; a
// file with no sound in it says that.
func TestReadWithToolsSaysWhyTheDecoderFailed(t *testing.T) {
	for decode, want := range map[string]string{
		"echo '  ' >&2; echo 'Invalid data found' >&2; exit 1": "Invalid data found",
		"exit 3": "exit status 3",
		"true":   "the file holds no sound",
	} {
		fakeTools(t, "echo 1", decode)
		if clip := analyzeSound("song.flac", 4); clip.Err == nil || clip.Err.Error() != want {
			t.Fatalf("%q read as %+v, want %q", decode, clip, want)
		}
	}
}

// The buffer of the decoded samples keeps its first bytes and lets the
// rest go.
func TestLimitedBufferKeepsItsLimit(t *testing.T) {
	b := limitedBuffer{limit: 3}
	for _, p := range []string{"ab", "cd", "ef"} {
		if n, err := b.Write([]byte(p)); n != 2 || err != nil {
			t.Fatalf("the write took %d, %v", n, err)
		}
	}
	if string(b.data) != "abc" {
		t.Fatalf("the buffer kept %q", b.data)
	}
}
