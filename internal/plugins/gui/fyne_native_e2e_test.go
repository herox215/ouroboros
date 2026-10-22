//go:build cgo && linux

package gui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
)

// The window on the real desktop driver, drawn by OpenGL, in a disposable
// process: OURO_TEST_FYNE_SHOTS names the directory its pictures go to. The
// driver needs the main goroutine, so the helper takes this path in init.
func init() {
	dir := os.Getenv("OURO_TEST_FYNE_SHOTS")
	if dir == "" || os.Getenv("OURO_TEST_FYNE_PHASE") != "" {
		return
	}
	runtime.LockOSThread()
	if err := fyneShots(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func fyneShots(dir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sessions := newE2ESessions()
	bridge := newWindowContext(ctx, page{choices: []choice{{name: "herox", id: "herox"}}, profiles: []*Profile{{sessions: sessions}}})
	defer bridge.Close()
	application := app.NewWithID("dev.ouroboros.gui.fyne.test")
	bridge.useTheme(application)
	native := application.NewWindow("Ouroboros")
	native.SetPadded(false)
	native.Resize(fyne.NewSize(1180, 780))
	ui := newFyneUI(ctx, application, native, bridge)
	ui.build()
	var failure error
	go func() {
		defer fyne.Do(native.Close)
		ui.start()
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			var ready bool
			fyne.DoAndWait(func() { ready = ui.ready.Load() })
			if ready {
				break
			}
			if time.Now().After(deadline) {
				failure = fmt.Errorf("the thread did not load")
				return
			}
		}
		shot := func(name string) {
			time.Sleep(500 * time.Millisecond)
			// The framebuffer of the driver is empty between frames: take
			// the picture from the X server, as the owner would see it.
			out, err := exec.Command("import", "-window", "root", filepath.Join(dir, name+".png")).CombinedOutput()
			if err != nil {
				failure = fmt.Errorf("import: %v: %s", err, out)
			}
		}
		for _, size := range []fyne.Size{{Width: 1400, Height: 900}, {Width: 900, Height: 640}, {Width: 420, Height: 700}} {
			fyne.DoAndWait(func() { native.Resize(size) })
			shot("gl-" + sizeName(size))
		}
		fyne.DoAndWait(ui.openDrawer)
		shot("gl-drawer")
		fyne.DoAndWait(func() {
			ui.closeDrawer()
			native.Resize(fyne.NewSize(1180, 780))
			application.Preferences().SetString(themePreferenceKey, "dark")
			bridge.useTheme(application)
			ui.openSettings(ui.profile, ui.profileName())
		})
		shot("gl-dark-settings")
	}()
	native.ShowAndRun()
	return failure
}

// TestFyneNativeShots draws the window with the desktop driver under Xvfb
// and keeps its pictures in OURO_FYNE_SNAPSHOTS. Opt in with
// OURO_TEST_FYNE_NATIVE=1, as for the restart.
func TestFyneNativeShots(t *testing.T) {
	dir := os.Getenv("OURO_FYNE_SNAPSHOTS")
	if os.Getenv("OURO_TEST_FYNE_NATIVE") != "1" || dir == "" {
		t.Skip("requires opt-in, OURO_FYNE_SNAPSHOTS and a desktop or Xvfb")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(x11Environ(), "OURO_TEST_FYNE_SHOTS="+dir, "XDG_CONFIG_HOME="+t.TempDir())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native window failed: %v\n%s", err, output)
	}
	if strings.TrimSpace(string(output)) != "" {
		t.Logf("%s", output)
	}
}

// x11Environ is the environment without the Wayland display: GLFW prefers
// Wayland where it finds one, and would open the window on the owner's
// desktop instead of the X server the test runs under.
func x11Environ() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "WAYLAND_DISPLAY=") {
			env = append(env, e)
		}
	}
	return env
}
