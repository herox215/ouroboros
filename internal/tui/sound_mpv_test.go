package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for mpv: started under that
// name, it is a fake mpv (see fakeMpv) instead of the tests.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "mpv" {
		fakeMpv(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeMpv answers the JSON IPC of mpv on the socket it is given, enough
// for the player: it loads, pauses, stops and quits. OURO_FAKE_MPV makes
// it "mute", never opening its socket, or "stubborn", never quitting.
func fakeMpv(args []string) {
	mode := os.Getenv("OURO_FAKE_MPV")
	var socket string
	for _, arg := range args {
		if s, ok := strings.CutPrefix(arg, "--input-ipc-server="); ok {
			socket = s
		}
	}
	if mode == "mute" || socket == "" {
		time.Sleep(time.Minute)
		return
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return
	}
	conn, err := listener.Accept()
	if err != nil {
		return
	}
	idle, paused := true, false
	lines := bufio.NewReader(conn)
	for {
		line, err := lines.ReadBytes('\n')
		if err != nil {
			if mode == "stubborn" {
				time.Sleep(time.Minute)
			}
			return
		}
		var request struct {
			Command []any `json:"command"`
			ID      int   `json:"request_id"`
		}
		if json.Unmarshal(line, &request) != nil || len(request.Command) == 0 {
			continue
		}
		var data any
		switch request.Command[0] {
		case "loadfile":
			idle = false
		case "stop":
			idle = true
		case "set_property":
			paused = request.Command[2] == true
		case "get_property":
			data = map[any]any{
				"idle-active": idle, "pause": paused, "time-pos": 1.0,
				"duration": 2.0, "eof-reached": false,
			}[request.Command[1]]
		}
		answer, _ := json.Marshal(map[string]any{"request_id": request.ID, "error": "success", "data": data})
		fmt.Fprintf(conn, "{\"event\":\"idle\"}\n%s\n", answer)
		if request.Command[0] == "quit" && mode != "stubborn" {
			return
		}
	}
}

// withMpv puts the fake mpv on the path, alone, in the given mode.
func withMpv(t *testing.T, mode string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(self, filepath.Join(dir, "mpv")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("OURO_FAKE_MPV", mode)
}

// The player starts mpv once, plays, holds and goes on with the sound,
// tells where it stands, stops it, and ends mpv with its socket.
func TestMpvPlaysAndEnds(t *testing.T) {
	withMpv(t, "")
	player, err := newSoundPlayer()
	if err != nil {
		t.Fatal(err)
	}
	p := player.(*mpv)
	t.Cleanup(func() { p.Close() })
	if err := p.Play("take.wav"); err != nil {
		t.Fatal(err)
	}
	dir := p.dir
	state, err := p.State()
	if err != nil || !state.Playing || state.Position != time.Second || state.Duration != 2*time.Second {
		t.Fatalf("the playing sound stands at %+v, %v", state, err)
	}
	if err := p.Pause(); err != nil {
		t.Fatal(err)
	}
	if state, _ := p.State(); state.Playing {
		t.Fatal("the held sound plays")
	}
	if err := p.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := p.Play("other.wav"); err != nil || p.dir != dir {
		t.Fatalf("the second sound started another mpv: %v", err)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	if state, err := p.State(); err != nil || state.Done || state.Playing {
		t.Fatalf("the stopped sound stands at %+v, %v", state, err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the socket of mpv is left behind")
	}
	if p.Close() != nil || p.Stop() != nil || p.Pause() == nil {
		t.Fatal("a closed player still acts")
	}
}

// An mpv that will not quit is killed after a while.
func TestMpvThatWillNotQuitIsKilled(t *testing.T) {
	withMpv(t, "stubborn")
	was := mpvQuitWait
	t.Cleanup(func() { mpvQuitWait = was })
	mpvQuitWait = 10 * time.Millisecond
	p := &mpv{}
	if err := p.Play("take.wav"); err != nil {
		t.Fatal(err)
	}
	exited := p.exited
	p.Close()
	<-exited
}

// An mpv that died is let go of on the next call, and the next sound
// starts a fresh one.
func TestMpvThatDiedIsReplaced(t *testing.T) {
	withMpv(t, "")
	p := &mpv{}
	t.Cleanup(func() { p.Close() })
	if err := p.Play("take.wav"); err != nil {
		t.Fatal(err)
	}
	p.command.Process.Kill()
	<-p.exited
	if _, err := p.State(); err == nil {
		t.Fatal("a dead mpv answered")
	}
	if p.conn != nil || p.dir != "" {
		t.Fatal("the dead mpv is kept")
	}
	if err := p.Play("take.wav"); err != nil {
		t.Fatal(err)
	}
}

// mpv that does not open its socket, that is not there, or a temporary
// directory that cannot be made, fail the sound with why.
func TestMpvThatCannotStartSaysWhy(t *testing.T) {
	withMpv(t, "mute")
	was := mpvStartWait
	t.Cleanup(func() { mpvStartWait = was })
	mpvStartWait = 50 * time.Millisecond
	if err := (&mpv{}).Play("take.wav"); err == nil || err.Error() != "mpv did not answer" {
		t.Fatalf("a mute mpv gave %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if err := (&mpv{}).Play("take.wav"); err == nil {
		t.Fatal("a missing mpv played")
	}
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "gone"))
	if err := (&mpv{}).Play("take.wav"); err == nil {
		t.Fatal("mpv started without a place for its socket")
	}
}

// scripted is an mpv on a pipe that answers each command by its name, a
// property by "get_property name": the answer is the JSON after the
// request_id, success without data if the name has none.
func scripted(t *testing.T, answers map[string]string) *mpv {
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
				Command []any `json:"command"`
				ID      int   `json:"request_id"`
			}
			if json.Unmarshal(line, &request) != nil {
				return
			}
			name := fmt.Sprint(request.Command[0])
			if name == "get_property" || name == "set_property" {
				name += " " + fmt.Sprint(request.Command[1])
			}
			answer, ok := answers[name]
			if !ok {
				answer = `"error":"success"`
			}
			fmt.Fprintf(theirs, "{\"request_id\":%d,%s}\n", request.ID, answer)
		}
	}()
	return &mpv{conn: ours, reader: bufio.NewReader(ours)}
}

// The state of the playback follows what mpv tells: done when it waits
// after a sound, or when the sound reached or nearly reached its end;
// properties mpv does not give leave their part of the state alone.
func TestMpvStateFollowsWhatMpvTells(t *testing.T) {
	for _, c := range []struct {
		name    string
		answers map[string]string
		loaded  bool
		want    playerState
		fails   bool
	}{
		{"idle after a sound", map[string]string{"get_property idle-active": `"data":true`}, true, playerState{Done: true}, false},
		{"no idle", map[string]string{"get_property idle-active": `"error":"unavailable"`}, false, playerState{}, true},
		{"idle unreadable", map[string]string{"get_property idle-active": `"data":1`}, false, playerState{}, true},
		{"at the end", map[string]string{
			"get_property idle-active": `"data":false`,
			"get_property pause":       `"data":false`,
			"get_property time-pos":    `"data":1.99`,
			"get_property duration":    `"data":2`,
			"get_property eof-reached": `"data":null`,
		}, true, playerState{Position: 1990 * time.Millisecond, Duration: 2 * time.Second, Done: true}, false},
		{"reached its end", map[string]string{
			"get_property idle-active": `"data":false`,
			"get_property pause":       `"error":"unavailable"`,
			"get_property time-pos":    `"data":"soon"`,
			"get_property duration":    `"data":null`,
			"get_property eof-reached": `"data":true`,
		}, true, playerState{Done: true}, false},
		{"unknown end", map[string]string{
			"get_property idle-active": `"data":false`,
			"get_property pause":       `"data":true`,
			"get_property time-pos":    `"error":"unavailable"`,
			"get_property eof-reached": `"data":2`,
		}, true, playerState{}, false},
		{"no eof", map[string]string{
			"get_property idle-active": `"data":false`,
			"get_property pause":       `"data":true`,
			"get_property eof-reached": `"error":"unavailable"`,
		}, true, playerState{}, false},
	} {
		p := scripted(t, c.answers)
		p.loaded = c.loaded
		state, err := p.State()
		if (err != nil) != c.fails || state != c.want {
			t.Fatalf("%s: the state is %+v, %v", c.name, state, err)
		}
	}
}

// A sound mpv will not load or start fails Play with what mpv said.
func TestMpvPlayFailsWithWhatMpvSaid(t *testing.T) {
	for _, failing := range []string{"loadfile", "set_property pause"} {
		p := scripted(t, map[string]string{failing: `"error":"loading failed"`})
		if err := p.Play("take.wav"); err == nil || err.Error() != "mpv: loading failed" || p.loaded {
			t.Fatalf("%s: Play gave %v", failing, err)
		}
	}
}

// A command that cannot be written, or not even be put into JSON, fails;
// the socket that took no command is let go of.
func TestMpvCallFailsWithoutAWay(t *testing.T) {
	p := scripted(t, nil)
	if _, err := p.call(make(chan int)); err == nil {
		t.Fatal("a command that is no JSON was sent")
	}
	p.conn.Close()
	if _, err := p.call("stop"); err == nil || p.conn != nil {
		t.Fatalf("a closed socket took the command: %v", err)
	}
}
