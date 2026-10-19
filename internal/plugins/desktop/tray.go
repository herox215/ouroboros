package desktop

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"ouroboros/internal/desktop"
	"ouroboros/internal/desktop/tray"
	"ouroboros/plugin"
)

// Profile wires the optional desktop adapter into the node's lifetime.
// Only a serving node owns a tray, never short-lived CLI or UI processes.
type Profile struct {
	source     plugin.ActivitySource
	log        func(error)
	controller *controller
}

type controller struct {
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	profiles []*Profile
	opening  bool
	run      func(context.Context, func(context.Context) desktop.Status, func()) error
	launch   func() error
}

// Together shares one tray across profiles whose desktop plugin is enabled.
// Its counts cover those profiles; the explicit CLI snapshots remain per-profile.
func Together(profiles []*Profile) {
	if len(profiles) == 0 {
		return
	}
	c := &controller{profiles: profiles, run: tray.Run, launch: launchGUI}
	for _, p := range profiles {
		p.controller = c
	}
}

func launchGUI() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "gui")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func (c *controller) sample(ctx context.Context) desktop.Status {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result := desktop.Status{Version: 1, State: "idle"}
	for _, p := range c.profiles {
		if p.source == nil {
			result.State = "error"
			continue
		}
		s := desktop.Read(ctx, p.source)
		result.Running += s.Running
		result.Waiting += s.Waiting
		if source, ok := p.source.(interface {
			RunningSessions(context.Context) ([]string, error)
		}); ok {
			names, err := source.RunningSessions(ctx)
			if err == nil {
				result.Sessions = append(result.Sessions, names...)
			} else {
				result.State = "error"
			}
		}
		if s.State == "error" || s.State == "offline" {
			result.State = "error"
		}
	}
	if result.State == "error" {
		return result
	}
	if result.Running > 0 {
		result.State = "working"
	}
	if result.Waiting > 0 {
		result.State = "waiting"
	}
	return result
}

func (c *controller) report(err error) {
	if err != nil && !errors.Is(err, context.Canceled) && len(c.profiles) > 0 {
		c.profiles[0].log(err)
	}
}

func (c *controller) open() {
	c.mu.Lock()
	if c.opening {
		c.mu.Unlock()
		return
	}
	c.opening = true
	c.mu.Unlock()
	go func() {
		defer func() { c.mu.Lock(); c.opening = false; c.mu.Unlock() }()
		c.report(c.launch())
	}()
}

func (p *Profile) start() {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return
	}
	if p.controller == nil {
		Together([]*Profile{p})
	}
	c := p.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel, c.done = cancel, make(chan struct{})
	go func() {
		defer close(c.done)
		c.report(c.run(ctx, c.sample, c.open))
	}()
}

func (p *Profile) stop() {
	c := p.controller
	if c == nil {
		return
	}
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func trayProfile(ctx *plugin.Context, sessions plugin.Sessions, report func(error)) *Profile {
	source, _ := sessions.(plugin.ActivitySource)
	if report == nil {
		report = func(err error) { log.Printf("desktop tray: %v", err) }
	}
	p := &Profile{source: source, log: report}
	plugin.Add(ctx, plugin.Serving{Start: p.start, Stop: p.stop})
	return p
}
