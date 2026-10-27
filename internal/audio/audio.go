// Package audio reads sound files for the audio cards of the chat and
// plays them on the output of the machine the node runs on.
//
// WAV is read without help; every other supported format needs ffmpeg
// and ffprobe, and playback needs mpv, which can be paused and asked
// where it is (see Player). A file that cannot be read comes back with
// why, so the card can say it instead of leaving the chat mute about it.
package audio

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Supported reports whether path names a sound file the chat shows a card
// for: by its extension, so that a file is tried before anything of it is
// read.
func Supported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav", ".mp3", ".ogg", ".oga", ".opus", ".flac", ".m4a", ".aac", ".aiff", ".aif", ".wma":
		return true
	}
	return false
}

// Clip is what a sound file holds, as its card shows it: how long it
// plays, and the shape of it in Columns peaks between 0 and 1. Err says
// why the file could not be read; Duration and Peaks are zero then.
type Clip struct {
	Duration time.Duration
	Peaks    []float32
	Err      error
}

// Analyze reads the sound file at path into a Clip with the given number
// of peaks. It decodes WAV itself. Other formats go through ffmpeg, whose
// absence in Err comes back like that of a decoder for a broken file.
func Analyze(path string, columns int) Clip {
	if columns < 1 {
		columns = 1
	}
	if strings.EqualFold(filepath.Ext(path), ".wav") {
		if read, ok := readWAV(path, columns); ok {
			return read
		}
	}
	return readWithTools(path, columns)
}

// readWAV decodes a RIFF/WAVE file: its length from the data chunk and
// peaks over the samples of the first channel. It reports false if the
// file is no WAV that it understands, so that another decoder can try.
func readWAV(path string, columns int) (Clip, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Clip{Err: err}, true
	}
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return Clip{}, false
	}
	var (
		format                      uint16
		channels, rate, bits, align int
		seenFmt                     bool
		pcm                         []byte
	)
	for at := 12; at+8 <= len(data); {
		id, size := string(data[at:at+4]), int(binary.LittleEndian.Uint32(data[at+4:at+8]))
		body := at + 8
		if size < 0 || body+size > len(data) {
			size = len(data) - body // a file cut short keeps what is there
		}
		switch id {
		case "fmt ":
			if size >= 16 {
				format = binary.LittleEndian.Uint16(data[body:])
				channels = int(binary.LittleEndian.Uint16(data[body+2:]))
				rate = int(binary.LittleEndian.Uint32(data[body+4:]))
				align = int(binary.LittleEndian.Uint16(data[body+12:]))
				bits = int(binary.LittleEndian.Uint16(data[body+14:]))
				if format == 0xFFFE && size >= 26 { // extensible: the subformat leads
					format = binary.LittleEndian.Uint16(data[body+24:])
				}
				seenFmt = true
			}
		case "data":
			pcm = data[body : body+size]
		}
		at = body + size + size%2
	}
	if !seenFmt || pcm == nil {
		return Clip{Err: errors.New("no WAV format or sample data")}, true
	}
	floating := format == 3
	if format != 1 && !floating {
		return Clip{Err: fmt.Errorf("unsupported WAV encoding %d", format)}, true
	}
	if channels < 1 || rate < 1 || align < 1 || bits < 1 {
		return Clip{Err: errors.New("invalid WAV format")}, true
	}
	frames := len(pcm) / align
	if frames == 0 {
		return Clip{Err: errors.New("WAV holds no samples")}, true
	}
	return Clip{
		Duration: time.Duration(float64(frames) / float64(rate) * float64(time.Second)),
		Peaks:    peaks(pcm, frames, align, bits, floating, channels, columns),
	}, true
}

// peaks is the loudest sample of every column of the sound: the frames
// between two columns go into the one of them, and each channel of a
// frame counts. The peaks are scaled so that the loudest is 1.
func peaks(pcm []byte, frames, align, bits int, floating bool, channels, columns int) []float32 {
	out := make([]float32, columns)
	top := float32(0)
	for frame := 0; frame < frames; frame++ {
		column := frame * columns / frames
		base := frame * align
		for c := 0; c < channels; c++ {
			v := absSample(pcm[base+c*align/channels:], bits, floating)
			if v > out[column] {
				out[column] = v
			}
			if v > top {
				top = v
			}
		}
	}
	if top > 0 {
		for i := range out {
			out[i] /= top
		}
	}
	return out
}

// absSample is one sample as its distance from silence, between 0 and 1.
func absSample(b []byte, bits int, floating bool) float32 {
	switch {
	case floating && bits == 32 && len(b) >= 4:
		return float32(math.Abs(float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))))
	case floating && bits == 64 && len(b) >= 8:
		return float32(math.Abs(math.Float64frombits(binary.LittleEndian.Uint64(b))))
	case bits == 8 && len(b) >= 1:
		return float32(math.Abs(float64(int(b[0])-128) / 128))
	case bits == 16 && len(b) >= 2:
		return float32(math.Abs(float64(int16(binary.LittleEndian.Uint16(b))) / 32768))
	case bits == 24 && len(b) >= 3:
		v := int32(b[0]) | int32(b[1])<<8 | int32(b[2])<<16
		if v&0x800000 != 0 {
			v |= ^int32(0xFFFFFF)
		}
		return float32(math.Abs(float64(v) / 8388608))
	case bits == 32 && len(b) >= 4:
		return float32(math.Abs(float64(int32(binary.LittleEndian.Uint32(b))) / 2147483648))
	}
	return 0
}

// readWithTools reads a format that is no WAV through ffmpeg: ffprobe
// for the length, ffmpeg for the samples the peaks are made of. It is the
// path for MP3, OGG, FLAC, M4A and the rest of Supported.
func readWithTools(path string, columns int) Clip {
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		return Clip{Err: errors.New("reading this format needs ffmpeg and ffprobe")}
	}
	decode, err := exec.LookPath("ffmpeg")
	if err != nil {
		return Clip{Err: errors.New("reading this format needs ffmpeg and ffprobe")}
	}
	out, err := exec.Command(probe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1", path).Output()
	duration := time.Duration(0)
	if err == nil {
		if seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil {
			duration = time.Duration(seconds * float64(time.Second))
		}
	}
	var raw limitedBuffer
	raw.limit = 32 << 20
	var stderr bytes.Buffer
	cmd := exec.Command(decode, "-v", "error", "-i", path, "-map", "0:a:0",
		"-f", "s16le", "-ac", "1", "-ar", "4000", "-")
	cmd.Stdout, cmd.Stderr = &raw, &stderr
	if err := cmd.Run(); err != nil {
		return Clip{Err: fmt.Errorf("%s", reason(err, stderr.String()))}
	}
	samples := len(raw.data) / 2
	if samples == 0 {
		return Clip{Err: errors.New("the file holds no sound")}
	}
	peaks := make([]float32, columns)
	top := float32(0)
	for i := 0; i < samples; i++ {
		v := float32(math.Abs(float64(int16(binary.LittleEndian.Uint16(raw.data[i*2:]))) / 32768))
		column := i * columns / samples
		if v > peaks[column] {
			peaks[column] = v
		}
		if v > top {
			top = v
		}
	}
	if top > 0 {
		for i := range peaks {
			peaks[i] /= top
		}
	}
	if duration == 0 {
		duration = time.Duration(float64(samples) / 4000 * float64(time.Second))
	}
	return Clip{Duration: duration, Peaks: peaks}
}

// reason is why a tool failed, from its first line of error, or the error
// itself if it said nothing.
func reason(err error, stderr string) string {
	for _, line := range strings.Split(stderr, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return err.Error()
}

// limitedBuffer keeps the first limit bytes written to it and lets the
// rest go, so that a long sound does not fill the memory.
type limitedBuffer struct {
	data  []byte
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.data); room > 0 {
		b.data = append(b.data, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// State is where the playback of a sound stands.
type State struct {
	Position time.Duration
	Duration time.Duration
	Playing  bool
	Done     bool // it played to its end
}

// Player plays one sound at a time on the output of this machine. Play
// starts a sound from its beginning; Pause and Resume hold it where it is;
// State can be asked while it plays. Close ends the output.
type Player interface {
	Play(path string) error
	Pause() error
	Resume() error
	State() (State, error)
	Stop() error
	Close() error
}

// mpv is the player the node uses: a background mpv whose JSON IPC the
// calls drive. It is the way to pause and to know the position without
// decoding the sound a second time, and it plays every format mpv knows.
type mpv struct {
	command *exec.Cmd
	conn    net.Conn
	reader  *bufio.Reader
	dir     string
	nextID  int
	loaded  bool
	path    string
	mu      sync.Mutex
}

// NewPlayer returns the player of the node: mpv in the background, see
// mpv. It fails with why if mpv is not installed; the chat shows that in
// the card instead of failing when the owner wants to listen.
func NewPlayer() (Player, error) {
	if _, err := exec.LookPath("mpv"); err != nil {
		return nil, errors.New("mpv is not installed")
	}
	return &mpv{}, nil
}

// Play starts the sound at path from its beginning.
func (p *mpv) Play(path string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.start(); err != nil {
		return err
	}
	if _, err := p.call("loadfile", path); err != nil {
		return err
	}
	if _, err := p.call("set_property", "pause", false); err != nil {
		return err
	}
	p.loaded, p.path = true, path
	return nil
}

// Pause holds the sound where it is.
func (p *mpv) Pause() error { return p.setPaused(true) }

// Resume goes on from where the sound was held.
func (p *mpv) Resume() error { return p.setPaused(false) }

func (p *mpv) setPaused(paused bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return errors.New("nothing is playing")
	}
	_, err := p.call("set_property", "pause", paused)
	return err
}

// State is where the playback stands, as mpv tells it: the position, how
// long the sound is, whether it plays, and whether it reached its end and
// mpv went back to waiting.
func (p *mpv) State() (State, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var state State
	if p.conn == nil {
		return state, nil
	}
	idle, err := p.bool("idle-active")
	if err != nil {
		return state, err
	}
	if idle {
		state.Done = p.loaded
		return state, nil
	}
	if paused, err := p.bool("pause"); err == nil {
		state.Playing = !paused
	}
	if position, ok := p.number("time-pos"); ok && position > 0 {
		state.Position = time.Duration(position * float64(time.Second))
	}
	if duration, ok := p.number("duration"); ok && duration > 0 {
		state.Duration = time.Duration(duration * float64(time.Second))
	}
	if ended, ok := p.boolean("eof-reached"); ok && ended {
		state.Done, state.Playing = true, false
	}
	if state.Duration > 0 && state.Position >= state.Duration-50*time.Millisecond {
		state.Done, state.Playing = true, false
	}
	return state, nil
}

// Stop takes the sound away and lets mpv wait again.
func (p *mpv) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return nil
	}
	p.loaded, p.path = false, ""
	_, err := p.call("stop")
	return err
}

// Close ends mpv and removes its socket.
func (p *mpv) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return nil
	}
	p.call("quit")
	p.conn.Close()
	p.conn = nil
	if p.command != nil && p.command.Process != nil {
		done := make(chan struct{})
		go func() { p.command.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			p.command.Process.Kill()
		}
	}
	if p.dir != "" {
		os.RemoveAll(p.dir)
	}
	return nil
}

// start brings mpv up and connects to its socket, once.
func (p *mpv) start() error {
	if p.conn != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "ouro-sound-")
	if err != nil {
		return err
	}
	socket := filepath.Join(dir, "sock")
	command := exec.Command("mpv", "--no-config", "--no-video", "--really-quiet",
		"--no-terminal", "--force-window=no", "--idle=yes",
		"--input-ipc-server="+socket)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		os.RemoveAll(dir)
		return err
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.Dial("unix", socket)
		if err == nil {
			p.command, p.conn, p.reader, p.dir = command, conn, bufio.NewReader(conn), dir
			return nil
		}
		if time.Now().After(deadline) {
			command.Process.Kill()
			os.RemoveAll(dir)
			return errors.New("mpv did not answer")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// call runs one JSON IPC command and returns what it answered.
func (p *mpv) call(args ...any) (json.RawMessage, error) {
	p.nextID++
	id := p.nextID
	payload, err := json.Marshal(map[string]any{"command": args, "request_id": id})
	if err != nil {
		return nil, err
	}
	p.conn.SetDeadline(time.Now().Add(3 * time.Second))
	defer p.conn.SetDeadline(time.Time{})
	if _, err := p.conn.Write(append(payload, '\n')); err != nil {
		return nil, err
	}
	for {
		line, err := p.reader.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var reply struct {
			ID    int             `json:"request_id"`
			Error string          `json:"error"`
			Data  json.RawMessage `json:"data"`
		}
		if json.Unmarshal(line, &reply) != nil || reply.ID != id {
			continue // an event, or an answer to another call
		}
		if reply.Error != "" && reply.Error != "success" {
			return nil, fmt.Errorf("mpv: %s", reply.Error)
		}
		return reply.Data, nil
	}
}

// number is a property mpv gives as a number.
func (p *mpv) number(name string) (float64, bool) {
	data, err := p.call("get_property", name)
	if err != nil || len(data) == 0 || string(data) == "null" {
		return 0, false
	}
	var v float64
	if json.Unmarshal(data, &v) != nil {
		return 0, false
	}
	return v, true
}

// boolean is a property mpv gives as true or false.
func (p *mpv) boolean(name string) (bool, bool) {
	data, err := p.call("get_property", name)
	if err != nil || len(data) == 0 || string(data) == "null" {
		return false, false
	}
	var v bool
	if json.Unmarshal(data, &v) != nil {
		return false, false
	}
	return v, true
}

// bool is a property mpv must give, for the state of its playback.
func (p *mpv) bool(name string) (bool, error) {
	data, err := p.call("get_property", name)
	if err != nil {
		return false, err
	}
	var v bool
	if err := json.Unmarshal(data, &v); err != nil {
		return false, err
	}
	return v, nil
}
