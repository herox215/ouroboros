package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A sound file attached to a message appears in the chat as a card below
// it: the name, a play button, where the playback is, and the shape of the
// sound. The samples are read in the background the first time a card is
// drawn; until then the card says that it is reading, and a file that
// cannot be read says why there instead of leaving the chat mute.
//
// Any card plays through one player at a time. Clicking its line toggles
// it, and ctrl+p toggles the sound of the chat: the one that plays or is
// held, or else the newest card. Nothing plays by itself.

// audioPeaks is how many peaks a sound is read into; a card shows as many
// of them as its width holds.
const audioPeaks = 240

// openPlayer opens the player of the chat, replaced in tests. It is
// opened once: every card plays through the same one, since Play replaces
// the sound it has.
var openPlayer = func() (soundPlayer, error) { return newSoundPlayer() }

// playback is the sound the chat has in its player, or is about to, and
// where it stands.
type playback struct {
	path    string
	player  soundPlayer
	started bool
	busy    bool
	state   playerState
	err     error
	// seq is the number of this playback: what an answer of the player
	// about another one brings is dropped, see soundState.
	seq int
}

// soundCard is a card the chat draws, in the order of the history.
type soundCard struct{ path string }

// audioClipMsg is a sound read for its card.
type audioClipMsg struct {
	path string
	clip soundClip
}

// audioAction is what a card asked the player to do.
type audioAction int

const (
	audioPlay audioAction = iota
	audioPause
	audioResume
	audioWhere // only asks where the playback stands
)

// audioStateMsg is where the playback of the sound stands after an action
// of its card, or after a tick while it plays.
type audioStateMsg struct {
	seq    int
	action audioAction
	state  playerState
	err    error
}

// audioTickMsg asks how far the sound that plays has come.
type audioTickMsg struct{ seq int }

// soundGlyphs draw a peak from silence to full, by its height.
var soundGlyphs = []rune("▁▂▃▄▅▆▇█")

// audioEvery is how often the position of a playing sound is read, and the
// card drawn anew once the second it shows has changed.
const audioEvery = 250 * time.Millisecond

// soundWork reads the sounds whose cards wait for it, each in the
// background, so that a long file does not hold the chat.
func (m *Model) soundWork() tea.Cmd {
	if len(m.soundWanted) == 0 {
		return nil
	}
	var cmds []tea.Cmd
	for path := range m.soundWanted {
		delete(m.soundWanted, path)
		path := path
		cmds = append(cmds, func() tea.Msg {
			return audioClipMsg{path: path, clip: analyzeSound(path, audioPeaks)}
		})
	}
	return tea.Batch(cmds...)
}

// soundRead files a sound read for its card and draws it.
func (m *Model) soundRead(msg audioClipMsg) tea.Cmd {
	if _, ok := m.soundClips[msg.path]; ok {
		return nil
	}
	m.soundClips[msg.path] = msg.clip
	m.soundRev++
	m.renderHistory(m.followsEnd())
	return nil
}

// soundText is what a message shows below its text: a card for every
// sound among its attachments, in the order they are named.
func (m *Model) soundText(c *conversation, text string, width int) (string, []soundCard) {
	seen := map[string]bool{}
	var cards []soundCard
	var lines []string
	for _, label := range labels.FindAllString(text, -1) {
		path, ok := c.attachment(label)
		if !ok || !isSound(path) || seen[path] {
			continue
		}
		seen[path] = true
		cards = append(cards, soundCard{path})
		lines = append(lines, m.soundCard(path, width))
	}
	if len(lines) == 0 {
		return "", nil
	}
	return "\n\n" + strings.Join(lines, "\n"), cards
}

// soundPaths is what an answer shows below its words: a card for every
// sound file it names, in the order it names them. A path counts when it is
// there and its extension is that of a sound, so that an answer that talks
// about a file which is gone, or about what a sound file is, stays words.
func (m *Model) soundPaths(text string, width int) (string, []soundCard) {
	seen := map[string]bool{}
	var cards []soundCard
	var lines []string
	for _, match := range namedPath.FindAllStringSubmatch(text, -1) {
		path := strings.TrimPrefix(match[1], "~")
		if strings.HasPrefix(match[1], "~") {
			home, err := os.UserHomeDir()
			if err != nil {
				continue
			}
			path = home + path
		}
		if seen[path] || !isSound(path) || len(cards) == 6 {
			continue
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			continue
		}
		seen[path] = true
		cards = append(cards, soundCard{path})
		lines = append(lines, m.soundCard(path, width))
	}
	if len(lines) == 0 {
		return "", nil
	}
	return "\n\n" + strings.Join(lines, "\n"), cards
}

// namedPath is a path a message names: one that starts at the root or in the
// home directory, after a blank or the opening of a word, in backticks too.
// A path with a blank in it is none: the chat shows the file that is there,
// it does not guess at where one is.
var namedPath = regexp.MustCompile("(?:^|[\\s(\\[`\"'])((?:/|~/)[^\\s`\"'()\\[\\]<>]+)")

// soundCard is the card of the sound at path: its button, name and
// position, and the waveform below, the part that played in the accent.
func (m *Model) soundCard(path string, width int) string {
	name := filepath.Base(path)
	clip, read := m.soundClips[path]
	if !read {
		m.soundWanted[path] = true
		return mutedStyle.Render("… " + name + " · reading the sound")
	}
	if clip.Err != nil {
		return imageStyle.Render(name) + errStyle.Render(" · "+clip.Err.Error())
	}
	playing := m.sound != nil && m.sound.path == path
	position, duration := time.Duration(0), clip.Duration
	if playing {
		position = m.sound.state.Position
		if m.sound.state.Duration > 0 {
			duration = m.sound.state.Duration
		}
	}
	button := "▶"
	if playing && m.sound.state.Playing {
		button = "⏸"
	}
	head := lipgloss.NewStyle().Foreground(accent).Render(button) + " " +
		boldStyle.Render(name) + mutedStyle.Render(" · "+stamp(position)+" / "+stamp(duration))
	if clip.Partial {
		// A sound too long to be read whole shows the shape of its start;
		// the rest of the waveform is flat, which is not silence.
		head += mutedStyle.Render(" · shape of the start")
	}
	// The waveform is drawn as wide as the card allows, but not wider
	// than a few dozen columns: it is a shape beside the name, not a chart.
	columns := max(8, min(bubbleWidth(width)-2, 60))
	if columns > len(clip.Peaks) {
		columns = len(clip.Peaks)
	}
	wave := make([]rune, columns)
	for i := range wave {
		peak := clip.Peaks[i*len(clip.Peaks)/columns]
		wave[i] = soundGlyphs[min(len(soundGlyphs)-1, int(float64(peak)*float64(len(soundGlyphs))))]
	}
	played := 0
	if duration > 0 {
		played = min(columns, int(float64(position)/float64(duration)*float64(columns)+0.5))
	}
	return head + "\n" + lipgloss.NewStyle().Foreground(accent).Render(string(wave[:played])) +
		mutedStyle.Render(string(wave[played:]))
}

// placeSoundCards maps the lines of the history that play a sound to its
// file. The cards are drawn in the order of the history, and a card whose
// sound could not be read draws no button, so its line is none.
//
// A line counts only if the button is followed by the name of the card
// that comes next: a message that merely begins with ▶ would otherwise
// take the place of a card, and every card after it would play the sound
// of the one before.
func (m *Model) placeSoundCards(content string) {
	m.soundLines = map[int]string{}
	var playable []string
	for _, card := range m.soundCards {
		if clip, ok := m.soundClips[card.path]; ok && clip.Err == nil {
			playable = append(playable, card.path)
		}
	}
	next := 0
	for line, text := range strings.Split(content, "\n") {
		if next == len(playable) {
			break
		}
		plain := strings.TrimLeft(ansi.Strip(text), " ")
		rest, ok := strings.CutPrefix(plain, "▶ ")
		if !ok {
			rest, ok = strings.CutPrefix(plain, "⏸ ")
		}
		if !ok || !namesCard(rest, filepath.Base(playable[next])) {
			continue
		}
		m.soundLines[line] = playable[next]
		next++
	}
}

// namesCard reports whether the rest of a line after the button is the
// card of the sound named name: the name follows the button, or as much
// of it as the line holds when a long name wraps at the edge of the chat.
func namesCard(rest, name string) bool {
	if strings.HasPrefix(rest, name) {
		return true
	}
	rest = strings.TrimRight(rest, " ")
	return len(rest) >= min(len(name), 8) && strings.HasPrefix(name, rest)
}

// toggleSound plays, pauses or resumes the sound at path: the one that
// plays or is held for path "", or else the newest card of the chat.
func (m *Model) toggleSound(path string) tea.Cmd {
	if path == "" && m.sound != nil {
		path = m.sound.path
	}
	if path == "" && len(m.soundCards) > 0 {
		path = m.soundCards[len(m.soundCards)-1].path
	}
	if path == "" {
		m.setNotice("there is no sound in the chat", false)
		return nil
	}
	clip, read := m.soundClips[path]
	if !read {
		m.setNotice("the sound is still being read", false)
		return nil
	}
	if clip.Err != nil {
		m.setNotice("the sound cannot be played: "+clip.Err.Error(), true)
		return nil
	}
	action := audioPlay
	switch {
	case m.sound != nil && m.sound.path == path:
		if m.sound.busy {
			return nil
		}
		switch {
		case m.sound.state.Playing:
			action = audioPause
		case m.sound.state.Done || !m.sound.started:
			action = audioPlay
		default:
			action = audioResume
		}
	default:
		// One player serves the chat: Play replaces the sound it has, so
		// another card takes it over instead of a second mpv being started
		// and the first left idle for the rest of the session.
		var player soundPlayer
		if m.sound != nil {
			player = m.sound.player
		}
		if player == nil {
			var err error
			if player, err = openPlayer(); err != nil {
				m.setNotice("the sound: "+err.Error(), true)
				return nil
			}
		}
		m.soundSeq++
		m.sound = &playback{path: path, player: player, seq: m.soundSeq}
		m.soundRev++
	}
	m.sound.busy = true
	m.renderHistory(m.followsEnd())
	player, seq := m.sound.player, m.sound.seq
	return func() tea.Msg {
		var err error
		switch action {
		case audioPause:
			err = player.Pause()
		case audioResume:
			err = player.Resume()
		case audioPlay:
			err = player.Play(path)
		}
		state, stateErr := player.State()
		if err == nil {
			err = stateErr
		}
		return audioStateMsg{seq: seq, action: action, state: state, err: err}
	}
}

// soundState applies where the playback stands after an action or a tick.
func (m *Model) soundState(msg audioStateMsg) tea.Cmd {
	if m.sound == nil || msg.seq != m.sound.seq {
		return nil
	}
	before := m.sound.state
	m.sound.busy = false
	m.sound.err = msg.err
	if msg.err != nil {
		m.setNotice("the sound: "+msg.err.Error(), true)
		m.soundRev++
		m.renderHistory(m.followsEnd())
		return nil
	}
	if msg.action == audioPlay {
		m.sound.started = true
	}
	m.sound.state = msg.state
	// mpv answers the first question about a sound just loaded before it
	// has played a sample of it: it is on its way then, not done.
	if msg.action == audioPlay && !m.sound.state.Playing && !m.sound.state.Done {
		m.sound.state.Playing = true
	}
	if stamp(before.Position) != stamp(m.sound.state.Position) ||
		before.Playing != m.sound.state.Playing || before.Done != m.sound.state.Done {
		m.soundRev++
		m.renderHistory(m.followsEnd())
	}
	if m.sound.state.Playing && !m.sound.state.Done {
		return m.soundTick(m.sound.seq)
	}
	return nil
}

// soundTick asks the player again in audioEvery, while the sound plays.
func (m *Model) soundTick(seq int) tea.Cmd {
	return tea.Tick(audioEvery, func(time.Time) tea.Msg { return audioTickMsg{seq} })
}

// soundTicked reads where the sound that plays has come.
func (m *Model) soundTicked(msg audioTickMsg) tea.Cmd {
	if m.sound == nil || msg.seq != m.sound.seq || !m.sound.state.Playing || m.sound.busy {
		return nil
	}
	player, seq := m.sound.player, m.sound.seq
	return func() tea.Msg {
		state, err := player.State()
		return audioStateMsg{seq: seq, action: audioWhere, state: state, err: err}
	}
}

// closeSound ends the player of the chat, if it has one.
func (m *Model) closeSound() {
	if m.sound != nil && m.sound.player != nil {
		m.sound.player.Close()
	}
	m.sound = nil
}

// stamp is a length of sound as a card shows it, like 0:07.
func stamp(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%d:%02d", int(d/time.Minute), int(d/time.Second)%60)
}
