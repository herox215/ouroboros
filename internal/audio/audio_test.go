package audio

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// tone writes a WAV of the given seconds at rate: sample is the value of
// the 16-bit PCM frame i.
func tone(t *testing.T, seconds float64, rate int, sample func(int) int16) string {
	t.Helper()
	frames := int(seconds * float64(rate))
	data := make([]byte, frames*2)
	for i := 0; i < frames; i++ {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(sample(i)))
	}
	path := filepath.Join(t.TempDir(), "tone.wav")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	header := make([]byte, 44)
	copy(header, "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(data)))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1) // PCM
	binary.LittleEndian.PutUint16(header[22:], 1) // mono
	binary.LittleEndian.PutUint32(header[24:], uint32(rate))
	binary.LittleEndian.PutUint32(header[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(data)))
	if _, err := file.Write(header); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSupportedKnowsSoundFiles(t *testing.T) {
	for _, name := range []string{"a.wav", "B.MP3", "x.ogg", "y.flac", "z.m4a", "q.opus"} {
		if !Supported(name) {
			t.Fatalf("%s is no sound", name)
		}
	}
	for _, name := range []string{"notes.txt", "cat.png", "sound", "a.wav.txt"} {
		if Supported(name) {
			t.Fatalf("%s is a sound", name)
		}
	}
}

// The card shows the real length and shape: silence stays at the bottom of
// the waveform, and the loudest part reaches the top.
func TestAnalyzeWAVReadsLengthAndPeaks(t *testing.T) {
	const rate = 8000
	path := tone(t, 0.5, rate, func(i int) int16 {
		if i < rate/4 { // the first half of the sound is silence
			return 0
		}
		return int16(30000 * math.Sin(2*math.Pi*440*float64(i)/rate))
	})
	clip := Analyze(path, 20)
	if clip.Err != nil {
		t.Fatal(clip.Err)
	}
	if want := 500 * time.Millisecond; clip.Duration < want-10*time.Millisecond || clip.Duration > want+10*time.Millisecond {
		t.Fatalf("duration %s, want %s", clip.Duration, want)
	}
	if len(clip.Peaks) != 20 {
		t.Fatalf("%d peaks", len(clip.Peaks))
	}
	for i, peak := range clip.Peaks {
		if peak < 0 || peak > 1 {
			t.Fatalf("peak %d is %f", i, peak)
		}
	}
	if peak := clip.Peaks[19]; peak < 0.95 {
		t.Fatalf("the loud half ends at %f", peak)
	}
}

func TestAnalyzeWAVReportsWhatItCannotRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.wav")
	if err := os.WriteFile(path, []byte("this is no sound"), 0600); err != nil {
		t.Fatal(err)
	}
	if clip := Analyze(path, 10); clip.Err == nil {
		t.Fatal("a text file reads as a sound")
	}
	if clip := Analyze(filepath.Join(t.TempDir(), "gone.wav"), 10); clip.Err == nil {
		t.Fatal("a missing file reads as a sound")
	}
	silent := tone(t, 0.1, 8000, func(int) int16 { return 0 })
	clip := Analyze(silent, 8)
	if clip.Err != nil {
		t.Fatal(clip.Err)
	}
	for i, peak := range clip.Peaks {
		if peak != 0 {
			t.Fatalf("silence %d is %f", i, peak)
		}
	}
}
