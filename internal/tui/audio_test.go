package tui

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/internal/audio"
)

// soundFile writes a WAV of two seconds with a tone to it, as a message of
// the owner would attach one.
func soundFile(t *testing.T) string {
	t.Helper()
	const rate, seconds = 8000, 2
	frames := rate * seconds
	data := make([]byte, frames*2)
	for i := 0; i < frames; i++ {
		v := int16(24000 * math.Sin(2*math.Pi*440*float64(i)/rate))
		binary.LittleEndian.PutUint16(data[i*2:], uint16(v))
	}
	header := make([]byte, 44)
	copy(header, "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(data)))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], rate)
	binary.LittleEndian.PutUint32(header[28:], rate*2)
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(data)))
	path := filepath.Join(t.TempDir(), "tone.wav")
	if err := os.WriteFile(path, append(header, data...), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// attached puts a message of the owner with the sound at path into the chat.
func attached(m *Model, path string) {
	m.chat.files = append(m.chat.files, path)
	m.chat.add(entry{mine: true, text: quote(path), at: time.Now()})
	m.renderHistory(true)
}

// readSound reads the sound at path as the card would, in the test.
func readSound(m *Model, path string) {
	m.Update(audioClipMsg{path: path, clip: audio.Analyze(path, audioPeaks)})
}

type fakePlayer struct {
	played  []string
	paused  int
	resumed int
	stopped bool
	closed  bool
	state   audio.State
	err     error
}

func (f *fakePlayer) Play(path string) error {
	f.played = append(f.played, path)
	f.state = audio.State{Playing: true, Duration: f.state.Duration}
	return f.err
}
func (f *fakePlayer) Pause() error  { f.paused++; f.state.Playing = false; return f.err }
func (f *fakePlayer) Resume() error { f.resumed++; f.state.Playing = true; return f.err }
func (f *fakePlayer) State() (audio.State, error) {
	return f.state, f.err
}
func (f *fakePlayer) Stop() error  { f.stopped = true; return nil }
func (f *fakePlayer) Close() error { f.closed = true; return nil }

// usePlayer makes the chat play through p, for the test.
func usePlayer(t *testing.T, p audio.Player) {
	was := openPlayer
	t.Cleanup(func() { openPlayer = was })
	openPlayer = func() (audio.Player, error) { return p, nil }
}

// ctrlP is ctrl+p, which plays or pauses the sound of the chat: the key,
// and what the player answered it.
func ctrlP(t *testing.T, m *Model) {
	t.Helper()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+p asks the player")
	}
	apply(t, m, cmd())
}

// apply hands what a command brought to the chat, a batch too. Ticks are
// left out: they would wait out the next quarter second and ask again.
func apply(t *testing.T, m *Model, msg tea.Msg) {
	t.Helper()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, cmd := range batch {
			if cmd != nil {
				apply(t, m, cmd())
			}
		}
		return
	}
	m.Update(msg)
}

// The card is the sound itself: its name, its button, its length, and a
// waveform made of the samples, not of anything else.
func TestSoundCardShowsNameLengthAndWaveform(t *testing.T) {
	m, _ := newModel(t)
	path := soundFile(t)
	attached(m, path)
	if !strings.Contains(ansi.Strip(m.history.View()), "reading the sound") {
		t.Fatal("the card should say it reads the sound")
	}
	if len(m.soundWanted) != 1 {
		t.Fatalf("the card asks for %d readings", len(m.soundWanted))
	}
	readSound(m, path)
	shown := ansi.Strip(m.history.View())
	for _, want := range []string{"▶", filepath.Base(path), "0:00 / 0:02", "█"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("the card shows no %q", want)
		}
	}
	if len(m.soundLines) != 1 {
		t.Fatalf("%d lines play a sound", len(m.soundLines))
	}
}

// Nothing plays by itself: only the key or the click starts the sound.
func TestTheSoundCardWaitsForTheOwner(t *testing.T) {
	m, _ := newModel(t)
	fake := &fakePlayer{state: audio.State{Duration: 2 * time.Second}}
	usePlayer(t, fake)
	path := soundFile(t)
	attached(m, path)
	readSound(m, path)
	if len(fake.played) != 0 {
		t.Fatalf("it played by itself: %v", fake.played)
	}
	ctrlP(t, m)
	if len(fake.played) != 1 || fake.played[0] != path {
		t.Fatalf("ctrl+p played %v", fake.played)
	}
	if !fake.state.Playing {
		t.Fatal("the sound should play")
	}
	if !strings.Contains(ansi.Strip(m.history.View()), "⏸") {
		t.Fatal("the card should show that it plays")
	}
	ctrlP(t, m)
	if fake.paused != 1 {
		t.Fatalf("ctrl+p paused %d times", fake.paused)
	}
	if !strings.Contains(ansi.Strip(m.history.View()), "▶") {
		t.Fatal("the card should show that it is held")
	}
	ctrlP(t, m)
	if fake.resumed != 1 {
		t.Fatalf("ctrl+p resumed %d times", fake.resumed)
	}
}

// The position moves while the sound plays, and the card says when it is
// through.
func TestTheSoundCardShowsThePosition(t *testing.T) {
	m, _ := newModel(t)
	fake := &fakePlayer{state: audio.State{Duration: 2 * time.Second}}
	usePlayer(t, fake)
	path := soundFile(t)
	attached(m, path)
	readSound(m, path)
	ctrlP(t, m)

	fake.state.Position, fake.state.Playing = 1*time.Second, true
	cmd := m.soundTicked(audioTickMsg{seq: m.sound.seq})
	m.Update(cmd())
	if !strings.Contains(ansi.Strip(m.history.View()), "0:01 / 0:02") {
		t.Fatal("the card should show the position")
	}

	fake.state = audio.State{Position: 2 * time.Second, Duration: 2 * time.Second, Done: true}
	cmd = m.soundTicked(audioTickMsg{seq: m.sound.seq})
	m.Update(cmd())
	if m.sound.state.Playing || !strings.Contains(ansi.Strip(m.history.View()), "0:02 / 0:02") {
		t.Fatal("the card should show the end")
	}
}

// A click on the line of the card does what ctrl+p does.
func TestTheCardPlaysOnAClick(t *testing.T) {
	m, _ := newModel(t)
	fake := &fakePlayer{state: audio.State{Duration: 2 * time.Second}}
	usePlayer(t, fake)
	path := soundFile(t)
	attached(m, path)
	readSound(m, path)
	if fake.played != nil {
		t.Fatal("nothing should play yet")
	}
	apply(t, m, clickOn(t, m, "▶", 0)())
	if len(fake.played) != 1 || fake.played[0] != path {
		t.Fatalf("the click played %v", fake.played)
	}
}

// Without a player or with a broken file the card stays there and says
// why it cannot play, instead of the chat going quiet about it.
func TestTheSoundCardSaysWhyItCannotPlay(t *testing.T) {
	m, _ := newModel(t)
	was := openPlayer
	t.Cleanup(func() { openPlayer = was })
	openPlayer = func() (audio.Player, error) { return nil, errors.New("mpv is not installed") }
	path := soundFile(t)
	attached(m, path)
	readSound(m, path)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if cmd != nil {
		cmd()
	}
	if !strings.Contains(m.notice, "mpv is not installed") {
		t.Fatalf("the chat should say why there is no sound: %q", m.notice)
	}
	if !strings.Contains(ansi.Strip(m.history.View()), "▶") {
		t.Fatal("the card should stay")
	}

	broken := filepath.Join(t.TempDir(), "broken.wav")
	if err := os.WriteFile(broken, []byte("RIFF\x24\x00\x00\x00WAVE"), 0600); err != nil {
		t.Fatal(err)
	}
	attached(m, broken)
	readSound(m, broken)
	shown := ansi.Strip(m.history.View())
	if !strings.Contains(shown, filepath.Base(broken)) || !strings.Contains(shown, "no WAV format") {
		t.Fatal("the card should say why it cannot be read")
	}
}

// An answer that names a sound file shows it as a card, as an attachment
// of the owner does, and plays it the same way. A path that is not there,
// or no sound, stays words.
func TestAnAnswerShowsTheSoundItNames(t *testing.T) {
	m, _ := newModel(t)
	fake := &fakePlayer{state: audio.State{Duration: 2 * time.Second}}
	usePlayer(t, fake)
	path := soundFile(t)
	notes := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(notes, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	m.chat.add(entry{text: "Die Takes: `" + path + "`, " + notes + ", /nowhere/kick.wav"})
	m.renderHistory(true)
	readSound(m, path)
	if len(m.soundCards) != 1 || m.soundCards[0].path != path {
		t.Fatalf("the answer shows %d cards: %+v", len(m.soundCards), m.soundCards)
	}
	if !strings.Contains(ansi.Strip(m.history.View()), filepath.Base(path)) {
		t.Fatal("the card should name the sound")
	}
	apply(t, m, clickOn(t, m, "▶", 0)())
	if len(fake.played) != 1 || fake.played[0] != path {
		t.Fatalf("the click played %v", fake.played)
	}
}

// Another card takes over the player of the chat: playing one sound and
// then another leaves no player behind, neither one that is still open
// nor a second one started beside it.
func TestAnotherCardTakesOverThePlayer(t *testing.T) {
	m, _ := newModel(t)
	var opened []*fakePlayer
	was := openPlayer
	t.Cleanup(func() { openPlayer = was })
	openPlayer = func() (audio.Player, error) {
		p := &fakePlayer{state: audio.State{Duration: 2 * time.Second}}
		opened = append(opened, p)
		return p, nil
	}
	first, second := soundFile(t), soundFile(t)
	attached(m, first)
	attached(m, second)
	readSound(m, first)
	readSound(m, second)
	for _, path := range []string{first, second, first} {
		apply(t, m, m.toggleSound(path)())
	}
	for i, p := range opened[:len(opened)-1] {
		if !p.closed {
			t.Fatalf("player %d of %d is left open", i+1, len(opened))
		}
	}
	last := opened[len(opened)-1]
	if len(last.played) == 0 || last.played[len(last.played)-1] != first {
		t.Fatalf("the player played %v, last %s", last.played, first)
	}
	if m.sound.player != last || m.sound.path != first {
		t.Fatal("the chat should hold the player that plays the first sound")
	}
}

// namedSound is a sound file like soundFile, under the given name.
func namedSound(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.Rename(soundFile(t), path); err != nil {
		t.Fatal(err)
	}
	return path
}

// A line of a message that begins with the button is no card: the lines
// of the cards still play their own sounds, in their order.
func TestALineThatLooksLikeACardTakesNoSound(t *testing.T) {
	m, _ := newModel(t)
	first, second := namedSound(t, "kick.wav"), namedSound(t, "snare.wav")
	m.chat.add(entry{text: "▶ press play on the takes below"})
	attached(m, first)
	attached(m, second)
	readSound(m, first)
	readSound(m, second)
	var played []string
	for line, text := range strings.Split(ansi.Strip(m.history.GetContent()), "\n") {
		path, ok := m.soundLines[line]
		if !ok {
			continue
		}
		if !strings.Contains(text, filepath.Base(path)) {
			t.Fatalf("line %d plays %s but reads %q", line, path, text)
		}
		played = append(played, path)
	}
	if len(played) != 2 || played[0] != first || played[1] != second {
		t.Fatalf("the lines play %v, want %s then %s", played, first, second)
	}
}
