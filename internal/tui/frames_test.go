package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// settleName puts the name of an empty chat at rest, as a visit that is over
// leaves it.
func settleName(m *Model) {
	a := &m.nameAnimation
	a.active, a.settled, a.chat, a.name = false, true, m.chat, m.agentName()
	a.fading = time.Time{}
}

// An idle UI asks for no frame at all, and a live one starts its clock only
// when something on the screen moves.
func TestNoFramesWhileIdle(t *testing.T) {
	m, _ := newModel(t)
	settleName(m)
	if m.moving() || m.animate() != nil || m.animateSplash() != nil {
		t.Fatal("an idle UI asked for a frame")
	}
	// The program drives the clock; a UI a test steps through does not.
	m.Live()
	if !m.live {
		t.Fatal("the program should make the UI live")
	}
	if _, cmd := m.Update(nil); cmd != nil {
		t.Fatalf("an idle live UI returned %T", cmd())
	}
	// While an answer is waited for, it moves.
	m.chat.pending = true
	if !m.moving() {
		t.Fatal("a wait should move")
	}
	cmd := m.animate()
	if cmd == nil || !m.animating {
		t.Fatal("the clock of the wait did not start")
	}
	// The clock draws a frame, and stops with the wait.
	m.chat.pending = false
	if next := m.advance(); next != nil || m.animating {
		t.Fatal("the clock went on after the wait")
	}
	// What moves while the answer is written is drawn by the answer's own
	// frames, not by a second clock.
	m.chat.pending, m.streaming = true, true
	if m.moving() {
		t.Fatal("a writing answer should not start the clock")
	}
	m.streaming = false
}

// Every state that moves asks for frames, and every state at rest does not.
func TestMovingStates(t *testing.T) {
	at := time.Unix(100, 0)
	for _, tt := range []struct {
		name  string
		set   func(m *Model)
		moves bool
	}{
		{"idle chat", func(m *Model) {}, false},
		{"a wait", func(m *Model) { m.chat.pending = true }, true},
		{"an empty chat's name", func(m *Model) { m.nameAnimation.active = true }, true},
		{"a name that fades", func(m *Model) {
			nameFade = time.Second
			m.nameAnimation.fading = at
		}, true},
		{"a fade that is over", func(m *Model) {
			nameFade = time.Second
			m.nameAnimation.fading = at.Add(-2 * time.Second)
		}, false},
		{"a session running beside the chat", func(m *Model) {
			m.conversation("other").pending = true
		}, true},
		{"a session running in a narrow window", func(m *Model) {
			m.width = 60
			m.conversation("other").pending = true
		}, false},
		{"the restart's message", func(m *Model) { m.chat.awaiting = &awaitingSend{} }, true},
		{"the frames of a restart", func(m *Model) { m.restartFrame = 1 }, false},
		{"the splash", func(m *Model) { m.splash.show = true }, false},
		{"the snake of the setup", func(m *Model) {
			m.onboard = &onboarding{at: at}
			m.onboard.curl.from, m.onboard.curl.to, m.onboard.curl.since = 0, 1, at
		}, true},
		{"the setup at rest", func(m *Model) { m.onboard = &onboarding{at: at} }, false},
		{"a screen that does not move", func(m *Model) { m.screen = screenHelp }, false},
		{"the sessions list with a session running", func(m *Model) {
			m.screen = screenSessions
			m.conversation("other").pending = true
		}, true},
		{"the sessions list at rest", func(m *Model) { m.screen = screenSessions }, false},
		{"a model list that loads", func(m *Model) {
			m.screen, m.picker.busy = screenModel, "listing models"
		}, true},
		{"a model list that is in", func(m *Model) { m.screen = screenModel }, false},
		{"settings that load", func(m *Model) { m.screen = screenSettings }, true},
		{"settings that are in", func(m *Model) {
			m.screen, m.config.rows = screenSettings, []row{{label: "one"}}
		}, false},
		{"settings that failed", func(m *Model) {
			m.screen, m.config.err = screenSettings, errNoNode
		}, false},
		{"memories that load", func(m *Model) { m.screen = screenMemory }, true},
		{"memories that are in", func(m *Model) {
			m.screen, m.memories.loaded = screenMemory, true
		}, false},
		{"memories that failed", func(m *Model) {
			m.screen, m.memories.err = screenMemory, errNoNode
		}, false},
		{"the cyberpsycho mode", func(m *Model) {
			m.screen, m.mode.at = screenMode, 2
		}, true},
		{"another mode", func(m *Model) { m.screen = screenMode }, false},
		{"the effort, whose mesh thinks", func(m *Model) { m.screen = screenEffort }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newModel(t)
			m.now = func() time.Time { return at }
			settleName(m)
			nameFade = 0
			t.Cleanup(func() { nameFade = 0 })
			tt.set(m)
			if got := m.moving(); got != tt.moves {
				t.Fatalf("moves %v, want %v", got, tt.moves)
			}
		})
	}
}

// Only a session of the owner's that works moves the list; the thread and a
// conversation of a peer are not sessions of the owner's.
func TestAnyRunning(t *testing.T) {
	m, _ := newModel(t)
	if m.anyRunning() {
		t.Fatal("nothing runs")
	}
	thread := m.conversation(plugin.ThreadID)
	thread.pending = true
	peer := m.conversation("peer/1")
	peer.peer, peer.pending = "luna", true
	if m.anyRunning() {
		t.Fatal("the thread and a peer should not count")
	}
	m.conversation("other").pending = true
	if !m.anyRunning() {
		t.Fatal("a session of the owner's runs")
	}
}

// The setup moves only while the snake curls, the end turns, or a page waits
// for the node: a scene at rest asks for no frame.
func TestSetupMovesOnlyWhileItChanges(t *testing.T) {
	at := time.Unix(100, 0)
	o := &onboarding{at: at}
	o.curl.from, o.curl.to, o.curl.since = 0, 0, at
	if o.moving(at) {
		t.Fatal("a snake that lies still should not move")
	}
	o.curl.to = 1
	if !o.moving(at) || o.moving(at.Add(curlDuration)) {
		t.Fatal("a snake that curls should move until it has")
	}
	o.busy = "connecting"
	if !o.moving(at.Add(time.Hour)) {
		t.Fatal("a page that waits should move")
	}
	o.busy = ""
	o.saved = at
	if !o.moving(at.Add(turnAt)) || o.moving(at.Add(turnAt+turnDuration)) {
		t.Fatal("the end should turn until it has")
	}
}

// The clock of the splash starts once and comes back only while the splash
// or the setup moves.
func TestAnimateSplash(t *testing.T) {
	m, _ := newModel(t)
	m.Live()
	m.splash.show = true
	if cmd := m.animateSplash(); cmd == nil || !m.splash.animating {
		t.Fatal("the splash should ask for a frame")
	}
	if cmd := m.animateSplash(); cmd != nil {
		t.Fatal("the clock of the splash runs already")
	}
	m.splash.show, m.splash.animating = false, false
	m.onboard = &onboarding{}
	at := time.Unix(100, 0)
	m.now = func() time.Time { return at }
	m.onboard.at = at
	m.onboard.curl.from, m.onboard.curl.to, m.onboard.curl.since = 0, 1, at
	if cmd := m.animateSplash(); cmd == nil {
		t.Fatal("the snake of the setup should ask for a frame while it curls")
	}
	m.splash.animating = false
	m.onboard.curl.to = 0
	if cmd := m.animateSplash(); cmd != nil {
		t.Fatal("a scene at rest asked for a frame")
	}
	m.onboard = nil
	if cmd := m.animateSplash(); cmd != nil {
		t.Fatal("nothing moves")
	}
}

// The name of an empty chat comes to rest after a while, and stays there:
// the visit is over, and an idle chat has no clock.
func TestNameSettles(t *testing.T) {
	was := nameSettle
	nameSettle = 100 * time.Millisecond
	t.Cleanup(func() { nameSettle = was })
	m, b := newModel(t)
	b.status.Name = "Luna"
	m.Update(m.refreshStatus())
	at := time.Unix(100, 0)
	m.now = func() time.Time { return at }
	m.nameAnimation.start = at
	if !m.nameAnimation.active {
		t.Fatal("an empty chat should begin the animation")
	}
	at = at.Add(nameSettle - time.Millisecond)
	m.advance()
	if !m.nameAnimation.active || m.nameAnimation.settled {
		t.Fatal("the name should still move before its time")
	}
	at = at.Add(time.Millisecond)
	m.advance()
	if m.nameAnimation.active || !m.nameAnimation.settled {
		t.Fatal("the name should have come to rest")
	}
	// It stands still from now on, in its own tagline.
	if got, want := m.history.GetContent(), emptyChat("Luna", m.history.Width(), m.history.Height(), m.nameAnimation.tagline); got != want {
		t.Fatalf("the name at rest:\n%s", got)
	}
	m.Update(nil)
	m.syncNameAnimation()
	if m.nameAnimation.active || !m.nameAnimation.settled {
		t.Fatal("a visit at rest began again")
	}
	// Another chat is another visit.
	m.chat = m.conversation("other")
	m.syncNameAnimation()
	if !m.nameAnimation.active || m.nameAnimation.settled {
		t.Fatal("another chat should move its name")
	}
}

// The name fades out behind the first message and is gone after it.
func TestNameSettlesAfterTheFade(t *testing.T) {
	was := nameFade
	nameFade = time.Second
	t.Cleanup(func() { nameFade = was })
	m, _ := newModel(t)
	at := time.Unix(100, 0)
	m.now = func() time.Time { return at }
	m.nameAnimation.start = at
	if !m.nameAnimation.active || m.nameAnimation.settled {
		t.Fatal("the name should be moving")
	}
	m.chat.entries = []entry{{mine: true, text: "hello"}}
	m.syncNameAnimation()
	if m.nameAnimation.fading.IsZero() {
		t.Fatal("the name should fade behind the first message")
	}
	m.Update(nil)
	if !m.moving() {
		t.Fatal("a name that fades should move")
	}
	at = at.Add(nameFade)
	m.advance()
	if !m.nameAnimation.fading.IsZero() || m.moving() {
		t.Fatal("the fade should be over")
	}
	m.Update(nil)
	if strings.Contains(m.history.GetContent(), nameRow(m.agentName())) {
		t.Fatal("the name is behind the message after the fade")
	}
}

// The clock of a node with several profiles runs for every one of them: the
// one shown draws, and the others keep their sessions in view.
func TestLiveWithProfiles(t *testing.T) {
	root, terra, luna, _ := profiles(t)
	root.Live()
	if !terra.live || !luna.live {
		t.Fatal("not every profile is live")
	}
}

// A piece of the answer on its way does not draw the frame again: it is drawn
// on the clock of the answer, once for every frame there, and not once for
// every piece the stream brings.
func TestAnsweringReusesTheFrame(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.chat.pending, m.streaming = true, true
	m.chat.partial = "answer one two"
	m.Update(answerMsg{})
	first := m.View()
	if !strings.Contains(first.Content, "answer one") {
		t.Fatalf("the answer is not drawn:\n%s", first.Content)
	}
	live := make(chan Event)
	m.Update(eventMsg{id: m.chat.contextID, event: Event{Kind: plugin.SessionAnswering, Text: " three"}, live: live})
	if !m.reuseFrame {
		t.Fatal("a piece of the answer should keep the frame")
	}
	if got := m.View(); got.Content != first.Content {
		t.Fatal("the piece drew the frame")
	}
	// The clock of the answer draws it, and what is no piece draws it too.
	m.Update(answerMsg{})
	if got := m.View(); !strings.Contains(got.Content, "answer one two") {
		t.Fatalf("the answer's clock did not draw the piece:\n%s", got.Content)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.reuseFrame {
		t.Fatal("a resize should draw the frame")
	}
	m.chat.pending, m.streaming = false, false
}
