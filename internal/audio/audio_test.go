package audio

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net"
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

// piped is an mpv whose socket is one end of a pipe: answer gets every
// command the player sends, with its request_id, and writes back what mpv
// would. No mpv needs to be installed for it.
func piped(t *testing.T, answer func(w net.Conn, id int)) *mpv {
	t.Helper()
	ours, theirs := net.Pipe()
	t.Cleanup(func() { ours.Close(); theirs.Close() })
	go func() {
		lines := bufio.NewReader(theirs)
		for {
			line, err := lines.ReadBytes('\n')
			if err != nil {
				return
			}
			var request struct {
				ID int `json:"request_id"`
			}
			if json.Unmarshal(line, &request) != nil {
				return
			}
			answer(theirs, request.ID)
		}
	}()
	return &mpv{conn: ours, reader: bufio.NewReader(ours)}
}

// mpv writes events and answers to other calls on the same socket: call
// takes only the answer to its own request, whatever comes before it.
func TestCallTakesTheAnswerToItsOwnRequest(t *testing.T) {
	p := piped(t, func(w net.Conn, id int) {
		fmt.Fprintf(w, "{\"event\":\"playback-restart\"}\n")
		fmt.Fprintf(w, "{\"request_id\":%d,\"error\":\"success\",\"data\":1.5}\n", id+100)
		fmt.Fprintf(w, "{\"request_id\":%d,\"error\":\"success\",\"data\":42.25}\n", id)
	})
	position, ok := p.number("time-pos")
	if !ok || position != 42.25 {
		t.Fatalf("the call took %v, %v", position, ok)
	}
	// The next call is matched by its own id, not by the one before.
	if position, ok := p.number("time-pos"); !ok || position != 42.25 {
		t.Fatalf("the second call took %v, %v", position, ok)
	}
}

func TestCallReportsTheErrorOfMpv(t *testing.T) {
	p := piped(t, func(w net.Conn, id int) {
		fmt.Fprintf(w, "{\"request_id\":%d,\"error\":\"property unavailable\"}\n", id)
	})
	if _, err := p.call("get_property", "duration"); err == nil || err.Error() != "mpv: property unavailable" {
		t.Fatalf("the call failed with %v", err)
	}
	if p.conn == nil {
		t.Fatal("an error of mpv is no reason to let it go")
	}
}

// An mpv that died leaves the player ready to start a fresh one, instead
// of every later call failing on the broken socket.
func TestCallDropsAnMpvThatIsGone(t *testing.T) {
	p := piped(t, func(w net.Conn, _ int) { w.Close() })
	if _, err := p.call("get_property", "pause"); err == nil {
		t.Fatal("a closed socket answered")
	}
	if p.conn != nil || p.reader != nil {
		t.Fatal("the dead socket is kept")
	}
	if _, err := p.call("get_property", "pause"); err == nil {
		t.Fatal("a call without mpv answered")
	}
	if state, err := p.State(); err != nil || state != (State{}) {
		t.Fatalf("the state without mpv is %+v, %v", state, err)
	}
}

// The samples are streamed in blocks: a sound longer than one block keeps
// its shape across them, and a file cut short reads as long as what is
// there, not as long as its header claims.
func TestAnalyzeWAVStreamsALongFileCutShort(t *testing.T) {
	const rate = 8000
	path := tone(t, 2, rate, func(i int) int16 {
		if i < rate { // the first second is silence
			return 0
		}
		return 20000
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(data[40:], uint32(len(data)*4)) // claims more than it holds
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	clip := Analyze(path, 4)
	if clip.Err != nil {
		t.Fatal(clip.Err)
	}
	if want := 2 * time.Second; clip.Duration < want-10*time.Millisecond || clip.Duration > want+10*time.Millisecond {
		t.Fatalf("duration %s, want %s", clip.Duration, want)
	}
	if clip.Peaks[0] != 0 || clip.Peaks[1] != 0 || clip.Peaks[2] != 1 || clip.Peaks[3] != 1 {
		t.Fatalf("peaks %v", clip.Peaks)
	}
}
