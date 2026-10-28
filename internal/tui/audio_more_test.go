package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// The player the chat opens by itself is mpv, and without mpv it says so.
func TestTheChatOpensMpvAsItsPlayer(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := openPlayer(); err == nil || err.Error() != "mpv is not installed" {
		t.Fatalf("opening the player without mpv gave %v", err)
	}
}

// The sounds a card waits for are read in the background, each once: a
// reading that comes in a second time changes nothing.
func TestSoundWorkReadsEverySoundOnce(t *testing.T) {
	m, _ := newModel(t)
	path := soundFile(t)
	attached(m, path)
	cmd := m.soundWork()
	if cmd == nil || len(m.soundWanted) != 0 {
		t.Fatal("the sound should be read in the background")
	}
	apply(t, m, cmd())
	clip, ok := m.soundClips[path]
	if !ok || clip.Err != nil {
		t.Fatalf("the sound was read as %+v", clip)
	}
	rev := m.soundRev
	m.soundRead(audioClipMsg{path: path, clip: soundClip{Err: errors.New("later")}})
	if m.soundRev != rev || m.soundClips[path].Err != nil {
		t.Fatal("a second reading replaced the first")
	}
}

// A path in the home directory is named with ~: the card shows the file
// there, and without a home directory the path stays words.
func TestAnAnswerShowsASoundInTheHomeDirectory(t *testing.T) {
	m, _ := newModel(t)
	path := soundFile(t)
	t.Setenv("HOME", filepath.Dir(path))
	m.chat.add(entry{text: "listen to ~/" + filepath.Base(path)})
	m.renderHistory(true)
	if len(m.soundCards) != 1 || m.soundCards[0].path != path {
		t.Fatalf("the answer shows the cards %+v", m.soundCards)
	}

	t.Setenv("HOME", "")
	if text, cards := m.soundPaths("listen to ~/"+filepath.Base(path), 80); text != "" || cards != nil {
		t.Fatalf("without a home the answer shows %q", text)
	}
}

// A sound longer than what was read says that its shape is the start of
// it, and a sound of few peaks draws as many columns as it has.
func TestTheCardOfAPartialSoundSaysSo(t *testing.T) {
	m, _ := newModel(t)
	path := soundFile(t)
	m.soundClips[path] = soundClip{Duration: time.Hour, Peaks: []float32{0, 0.5, 1}, Partial: true}
	card := ansi.Strip(m.soundCard(path, 80))
	if !strings.Contains(card, "shape of the start") {
		t.Fatalf("the card does not say it is partial: %q", card)
	}
	if lines := strings.Split(card, "\n"); len([]rune(lines[1])) != 3 {
		t.Fatalf("the waveform is %q", lines[1])
	}
}

// ctrl+p says why nothing plays: no card, a card still being read, or a
// sound that cannot be read.
func TestCtrlPSaysWhyNothingPlays(t *testing.T) {
	m, _ := newModel(t)
	if cmd := m.toggleSound(""); cmd != nil || m.notice != "there is no sound in the chat" {
		t.Fatalf("without a card the notice is %q", m.notice)
	}
	path := soundFile(t)
	attached(m, path)
	if cmd := m.toggleSound(""); cmd != nil || m.notice != "the sound is still being read" {
		t.Fatalf("before the reading the notice is %q", m.notice)
	}
	m.soundClips[path] = soundClip{Err: errors.New("broken")}
	if cmd := m.toggleSound(path); cmd != nil || !strings.Contains(m.notice, "cannot be played: broken") {
		t.Fatalf("for a broken sound the notice is %q", m.notice)
	}
}

// While the player is busy with the last press, another one waits; a
// sound that played to its end starts again from its beginning.
func TestToggleWaitsForThePlayerAndPlaysAgainAtTheEnd(t *testing.T) {
	m, _ := newModel(t)
	fake := &fakePlayer{state: playerState{Duration: 2 * time.Second}}
	usePlayer(t, fake)
	path := soundFile(t)
	attached(m, path)
	readSound(m, path)
	cmd := m.toggleSound(path)
	if m.toggleSound(path) != nil {
		t.Fatal("a press while the player is busy asked it again")
	}
	apply(t, m, cmd())
	m.sound.state = playerState{Done: true}
	apply(t, m, m.toggleSound(path)())
	if len(fake.played) != 2 {
		t.Fatalf("the sound played %d times", len(fake.played))
	}
}

// An answer of the player about an earlier playback is dropped; an error
// of it is said; a sound just loaded counts as playing before mpv says so.
func TestSoundStateKeepsToItsPlayback(t *testing.T) {
	m, _ := newModel(t)
	fake := &fakePlayer{state: playerState{Duration: 2 * time.Second}}
	usePlayer(t, fake)
	path := soundFile(t)
	attached(m, path)
	readSound(m, path)
	apply(t, m, m.toggleSound(path)())
	seq := m.sound.seq
	if m.soundState(audioStateMsg{seq: seq - 1, err: errors.New("old")}) != nil || m.sound.err != nil {
		t.Fatal("an answer about another playback was taken")
	}
	m.soundState(audioStateMsg{seq: seq, err: errors.New("mpv went away")})
	if !strings.Contains(m.notice, "mpv went away") || m.sound.busy {
		t.Fatalf("the error is not said: %q", m.notice)
	}
	if cmd := m.soundState(audioStateMsg{seq: seq, action: audioPlay}); cmd == nil || !m.sound.state.Playing {
		t.Fatal("a sound just loaded should count as playing and be asked again")
	}
	// The tick comes back after a quarter second as the question where the
	// sound stands.
	if msg, ok := m.soundTick(seq)().(audioTickMsg); !ok || msg.seq != seq {
		t.Fatalf("the tick brought %+v", msg)
	}
	m.sound.busy = true
	if _, cmd := m.Update(audioTickMsg{seq: seq}); cmd != nil {
		t.Fatal("a tick asked a busy player")
	}
}

// Leaving the chat closes its player.
func TestCloseSoundClosesThePlayer(t *testing.T) {
	m, _ := newModel(t)
	fake := &fakePlayer{}
	m.sound = &playback{player: fake}
	m.closeSound()
	if !fake.closed || m.sound != nil {
		t.Fatal("the player is left open")
	}
}

// A position before the start reads as the start.
func TestStampOfANegativeLengthIsZero(t *testing.T) {
	if got := stamp(-time.Second); got != "0:00" {
		t.Fatalf("stamp is %q", got)
	}
}
