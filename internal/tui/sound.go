// The sound files of the audio cards of the chat are read here and played
// on the output of the machine the node runs on.
//
// WAV is read without help; every other supported format needs ffmpeg
// and ffprobe, and playback needs mpv, which can be paused and asked
// where it is (see soundPlayer). A file that cannot be read comes back with
// why, so the card can say it instead of leaving the chat mute about it.

package tui

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

// isSound reports whether path names a sound file the chat shows a card
// for: by its extension, so that a file is tried before anything of it is
// read.
func isSound(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".wav", ".mp3", ".ogg", ".oga", ".opus", ".flac", ".m4a", ".aac", ".aiff", ".aif", ".wma":
		return true
	}
	return false
}

// soundClip is what a sound file holds, as its card shows it: how long it
// plays, and the shape of it in Columns peaks between 0 and 1. Err says
// why the file could not be read; Duration and Peaks are zero then.
// Partial says that the shape covers only the start of a long sound: the
// peaks keep their place in the whole of it, and those past the part
// that was read stay at zero.
type soundClip struct {
	Duration time.Duration
	Peaks    []float32
	Partial  bool
	Err      error
}

// analyzeSound reads the sound file at path into a soundClip with the given number
// of peaks. It decodes WAV itself. Other formats go through ffmpeg, whose
// absence in Err comes back like that of a decoder for a broken file.
func analyzeSound(path string, columns int) soundClip {
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
//
// The file is walked chunk by chunk and its samples streamed, never read
// whole: a long recording in high resolution is hundreds of megabytes,
// and the card only needs a few hundred peaks of it.
func readWAV(path string, columns int) (soundClip, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return soundClip{Err: err}, true
	}
	file, err := os.Open(path)
	if err != nil {
		return soundClip{Err: err}, true
	}
	defer file.Close()
	return decodeWAV(file, info.Size(), columns)
}

// decodeWAV is readWAV over the length bytes of file.
func decodeWAV(file io.ReaderAt, length int64, columns int) (soundClip, bool) {
	head := make([]byte, 12)
	if _, err := file.ReadAt(head, 0); err != nil || string(head[:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return soundClip{}, false
	}
	var (
		format                      uint16
		channels, rate, bits, align int
		seenFmt                     bool
		pcmAt, pcmSize              int64 = 0, -1
	)
	chunk := make([]byte, 8)
	for at := int64(12); at+8 <= length; {
		if _, err := file.ReadAt(chunk, at); err != nil {
			break
		}
		id, size := string(chunk[:4]), int64(binary.LittleEndian.Uint32(chunk[4:]))
		body := at + 8
		if body+size > length {
			size = length - body // a file cut short keeps what is there
		}
		switch id {
		case "fmt ":
			if size >= 16 {
				fmtBody := make([]byte, min(size, 40))
				if _, err := file.ReadAt(fmtBody, body); err != nil {
					break
				}
				format = binary.LittleEndian.Uint16(fmtBody)
				channels = int(binary.LittleEndian.Uint16(fmtBody[2:]))
				rate = int(binary.LittleEndian.Uint32(fmtBody[4:]))
				align = int(binary.LittleEndian.Uint16(fmtBody[12:]))
				bits = int(binary.LittleEndian.Uint16(fmtBody[14:]))
				if format == 0xFFFE && size >= 26 { // extensible: the subformat leads
					format = binary.LittleEndian.Uint16(fmtBody[24:])
				}
				seenFmt = true
			}
		case "data":
			pcmAt, pcmSize = body, size
		}
		at = body + size + size%2
	}
	if !seenFmt || pcmSize < 0 {
		return soundClip{Err: errors.New("no WAV format or sample data")}, true
	}
	floating := format == 3
	if format != 1 && !floating {
		return soundClip{Err: fmt.Errorf("unsupported WAV encoding %d", format)}, true
	}
	if channels < 1 || rate < 1 || align < 1 || bits < 1 {
		return soundClip{Err: errors.New("invalid WAV format")}, true
	}
	frames := int(pcmSize / int64(align))
	if frames == 0 {
		return soundClip{Err: errors.New("WAV holds no samples")}, true
	}
	pcm := bufio.NewReader(io.NewSectionReader(file, pcmAt, int64(frames)*int64(align)))
	return soundClip{
		Duration: time.Duration(float64(frames) / float64(rate) * float64(time.Second)),
		Peaks:    peaks(pcm, frames, align, bits, floating, channels, columns),
	}, true
}

// peaks is the loudest sample of every column of the sound: the frames
// between two columns go into the one of them, and each channel of a
// frame counts. The peaks are scaled so that the loudest is 1. A sound
// that ends before its frames, a file changed while it is read, keeps
// the peaks up to there.
func peaks(pcm io.Reader, frames, align, bits int, floating bool, channels, columns int) []float32 {
	out := make([]float32, columns)
	top := float32(0)
	block := make([]byte, align*4096)
	for frame := 0; frame < frames; {
		n, err := io.ReadFull(pcm, block[:min(len(block), (frames-frame)*align)])
		for base := 0; base+align <= n; base, frame = base+align, frame+1 {
			column := frame * columns / frames
			for c := 0; c < channels; c++ {
				v := absSample(block[base+c*align/channels:base+align], bits, floating)
				if v > out[column] {
					out[column] = v
				}
				if v > top {
					top = v
				}
			}
		}
		if err != nil {
			break
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
// path for MP3, OGG, FLAC, M4A and the rest of isSound.
func readWithTools(path string, columns int) soundClip {
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		return soundClip{Err: errors.New("reading this format needs ffmpeg and ffprobe")}
	}
	decode, err := exec.LookPath("ffmpeg")
	if err != nil {
		return soundClip{Err: errors.New("reading this format needs ffmpeg and ffprobe")}
	}
	out, err := exec.Command(probe, "-v", "error", "-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1", path).Output()
	duration := time.Duration(0)
	if err == nil {
		if seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil {
			duration = time.Duration(seconds * float64(time.Second))
		}
	}
	// The shape is read at 4000 samples a second into at most 32 MiB,
	// which is about 70 minutes. ffmpeg is told to stop there, so that a
	// longer file is not decoded to its end only for the rest to be let go.
	var raw limitedBuffer
	raw.limit = toolBytes
	var stderr bytes.Buffer
	cmd := exec.Command(decode, "-v", "error", "-i", path, "-map", "0:a:0",
		"-t", strconv.Itoa(toolBytes/2/toolRate),
		"-f", "s16le", "-ac", "1", "-ar", strconv.Itoa(toolRate), "-")
	cmd.Stdout, cmd.Stderr = &raw, &stderr
	if err := cmd.Run(); err != nil {
		return soundClip{Err: fmt.Errorf("%s", reason(err, stderr.String()))}
	}
	samples := len(raw.data) / 2
	if samples == 0 {
		return soundClip{Err: errors.New("the file holds no sound")}
	}
	// The peaks are spread over the whole length of the sound, not over
	// the part that was read: a column then stands for the same moment as
	// the position of the playback over it, and a sound longer than what
	// was read shows a shape that ends early, said so by Partial.
	span := samples
	if whole := int(duration.Seconds() * toolRate); whole > span {
		span = whole
	}
	peaks := make([]float32, columns)
	top := float32(0)
	for i := 0; i < samples; i++ {
		v := float32(math.Abs(float64(int16(binary.LittleEndian.Uint16(raw.data[i*2:]))) / 32768))
		column := i * columns / span
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
		duration = time.Duration(float64(samples) / toolRate * float64(time.Second))
	}
	// A second of the length left out of the shape is rounding; more is
	// a sound longer than what was read.
	partial := span-samples > toolRate
	return soundClip{Duration: duration, Peaks: peaks, Partial: partial}
}

// toolRate and toolBytes are what the shape of a sound read through
// ffmpeg is made of: samples a second, and the most bytes of them.
const (
	toolRate  = 4000
	toolBytes = 32 << 20
)

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

// playerState is where the playback of a sound stands.
type playerState struct {
	Position time.Duration
	Duration time.Duration
	Playing  bool
	Done     bool // it played to its end
}

// soundPlayer plays one sound at a time on the output of this machine. Play
// starts a sound from its beginning; Pause and Resume hold it where it is;
// State can be asked while it plays. Close ends the output.
type soundPlayer interface {
	Play(path string) error
	Pause() error
	Resume() error
	State() (playerState, error)
	Stop() error
	Close() error
}

// mpv is the player the node uses: a background mpv whose JSON IPC the
// calls drive. It is the way to pause and to know the position without
// decoding the sound a second time, and it plays every format mpv knows.
type mpv struct {
	command *exec.Cmd
	exited  chan struct{} // closed once mpv has ended and been reaped
	conn    net.Conn
	reader  *bufio.Reader
	dir     string
	nextID  int
	loaded  bool
	path    string
	mu      sync.Mutex
}

// mpvStartWait is how long mpv is given to open its socket, and
// mpvQuitWait how long to quit before it is killed. Tests shorten them.
var (
	mpvStartWait = 3 * time.Second
	mpvQuitWait  = 2 * time.Second
)

// newSoundPlayer returns the player of the node: mpv in the background, see
// mpv. It fails with why if mpv is not installed; the chat shows that in
// the card instead of failing when the owner wants to listen.
func newSoundPlayer() (soundPlayer, error) {
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

// playerState is where the playback stands, as mpv tells it: the position, how
// long the sound is, whether it plays, and whether it reached its end and
// mpv went back to waiting.
func (p *mpv) State() (playerState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var state playerState
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

// Close ends mpv and removes its socket. mpv is asked to quit and given
// mpvQuitWait for it; then it is killed.
func (p *mpv) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return nil
	}
	command, exited, dir := p.command, p.exited, p.dir
	p.call("quit")
	if p.conn != nil {
		p.conn.Close()
	}
	p.command, p.exited, p.conn, p.reader, p.dir = nil, nil, nil, nil, ""
	p.loaded, p.path = false, ""
	if exited != nil {
		select {
		case <-exited:
		case <-time.After(mpvQuitWait):
			command.Process.Kill()
		}
	}
	if dir != "" {
		os.RemoveAll(dir)
	}
	return nil
}

// drop lets go of an mpv that stopped answering, or died: the next Play
// starts a fresh one instead of every call failing on the same broken
// socket for the rest of the chat. One that still runs is killed, so
// that it does not linger without anyone to talk to it.
func (p *mpv) drop() {
	if p.conn != nil {
		p.conn.Close()
	}
	if p.command != nil && p.command.Process != nil {
		p.command.Process.Kill()
	}
	if p.dir != "" {
		os.RemoveAll(p.dir)
	}
	p.command, p.exited, p.conn, p.reader, p.dir = nil, nil, nil, nil, ""
	p.loaded, p.path = false, ""
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
	// mpv is reaped as soon as it ends, by itself too, so that one which
	// died does not stay a zombie until the node exits.
	exited := make(chan struct{})
	go func() { command.Wait(); close(exited) }()
	deadline := time.Now().Add(mpvStartWait)
	for {
		conn, err := net.Dial("unix", socket)
		if err == nil {
			p.command, p.exited, p.conn, p.reader, p.dir = command, exited, conn, bufio.NewReader(conn), dir
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

// call runs one JSON IPC command and returns what it answered. A socket
// that fails to take the command or to answer it means mpv is gone or
// stuck, and it is dropped, see drop.
func (p *mpv) call(args ...any) (json.RawMessage, error) {
	if p.conn == nil {
		return nil, errors.New("nothing is playing")
	}
	p.nextID++
	id := p.nextID
	payload, err := json.Marshal(map[string]any{"command": args, "request_id": id})
	if err != nil {
		return nil, err
	}
	p.conn.SetDeadline(time.Now().Add(3 * time.Second))
	defer p.conn.SetDeadline(time.Time{})
	if _, err := p.conn.Write(append(payload, '\n')); err != nil {
		p.drop()
		return nil, err
	}
	for {
		line, err := p.reader.ReadBytes('\n')
		if err != nil {
			p.drop()
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
