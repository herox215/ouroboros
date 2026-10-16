package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// programs are the browsers looked for, the first one found is started:
// Chromium and those made of it, which speak its DevTools. Tests replace
// them.
var programs = map[string][]string{
	"darwin": {
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	},
	"": {"chromium", "chromium-browser", "google-chrome-stable", "google-chrome", "chrome",
		"brave-browser", "brave", "microsoft-edge-stable", "microsoft-edge"},
}

// The times of a browser; tests shorten them.
var (
	// startTimeout is how long a browser may take to answer once started.
	startTimeout = 20 * time.Second
	// closeTimeout is how long a browser may take to end once asked to,
	// before it is killed.
	closeTimeout = 3 * time.Second
)

// windowSize is the size of the window of a browser, and so of what a
// screenshot shows.
const windowSize = "1280,900"

// process is a browser that runs, with the connection to its DevTools.
type process struct {
	*conn
	visible bool
	remote  bool // disconnect, never stop a hosted browser
	ownedMu sync.Mutex
	owned   map[string]bool // only contexts created by this connection
	end     func()          // ends the browser and waits for it

	startup string        // the launch page, captured before conversations create tabs
	owner   *conversation // its first owner conversation; guarded by browser.mu
}

// find returns the first browser of programs there is.
func find() (string, error) {
	candidates, ok := programs[runtime.GOOS]
	if !ok {
		candidates = programs[""]
	}
	for _, p := range candidates {
		if path, err := exec.LookPath(p); err == nil {
			return path, nil
		}
	}
	return "", errors.New("no browser found: install Chromium or Google Chrome")
}

// agents are the user agents the browsers say they are without a window,
// by the program and when it was installed: what they say with one.
var agents = struct {
	sync.Mutex
	of map[string]string
}{of: map[string]string{}}

// launch starts a browser with the profile in the directory profile, with
// a window if visible, and connects to it through the pipe of its DevTools,
// which no other program can reach. It ends when the pipe closes: with the
// process of ouro, whatever ends it.
//
// It starts the browser as one a person uses, as far as pages see it
// first: it does not say that it is automated, nor without a window that
// it has none, and it speaks the languages of the user. Without a window
// the browser says so in its user agent, which it is started with in its
// place: the first one without a window is started once more for it.
func launch(ctx context.Context, profile string, visible bool) (*process, error) {
	program, err := find()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, err
	}
	key := program
	if info, err := os.Stat(program); err == nil {
		key += " " + info.ModTime().String() // a browser updated is another one
	}
	agents.Lock()
	agent := agents.of[key]
	agents.Unlock()
	p, said, err := run(ctx, program, profile, visible, agent)
	if err != nil || agent != "" {
		return p, err
	}
	agent = strings.Replace(said, "HeadlessChrome/", "Chrome/", 1)
	agents.Lock()
	agents.of[key] = agent
	agents.Unlock()
	if agent == said {
		return p, nil
	}
	p.end()
	p, _, err = run(ctx, program, profile, visible, agent)
	return p, err
}

// languages are the flags that make a browser speak the languages of the
// user, as the environment names them, like de_DE.UTF-8: German first,
// then English.
func languages() []string {
	locale := firstOf(os.Getenv("LC_ALL"), os.Getenv("LC_MESSAGES"), os.Getenv("LANG"))
	locale, _, _ = strings.Cut(locale, ".")
	locale, _, _ = strings.Cut(locale, "@")
	if locale == "" || locale == "C" || locale == "POSIX" {
		return nil
	}
	tag := strings.ReplaceAll(locale, "_", "-")
	accepted := []string{tag}
	if base, _, _ := strings.Cut(tag, "-"); base != tag {
		accepted = append(accepted, base)
	}
	if !strings.HasPrefix(tag, "en") {
		accepted = append(accepted, "en-US", "en")
	}
	return []string{"--lang=" + tag, "--accept-lang=" + strings.Join(accepted, ",")}
}

// run starts program with the profile, with a window if visible, as the
// user agent agent unless that is "", and returns the user agent it says
// it is.
func run(ctx context.Context, program, profile string, visible bool, agent string) (*process, string, error) {
	args := []string{"--remote-debugging-pipe", "--user-data-dir=" + profile, "--no-first-run",
		"--no-default-browser-check", "--password-store=basic", "--window-size=" + windowSize,
		"--disable-blink-features=AutomationControlled"}
	args = append(args, languages()...)
	if !visible {
		args = append(args, "--headless")
	}
	if agent != "" {
		args = append(args, "--user-agent="+agent)
	}
	cmd := exec.Command(program, append(args, "about:blank")...)
	// The browser reads commands from its fd 3 and writes to its fd 4.
	commands, toBrowser, _ := os.Pipe() // fails only without file descriptors, as the start would
	fromBrowser, replies, _ := os.Pipe()
	cmd.ExtraFiles = []*os.File{commands, replies}
	var stderr tail
	cmd.Stderr = &stderr
	err := cmd.Start()
	commands.Close()
	replies.Close()
	if err != nil {
		toBrowser.Close()
		fromBrowser.Close()
		return nil, "", err
	}
	exited := make(chan struct{})
	go func() {
		cmd.Wait()
		fromBrowser.Close()
		close(exited)
	}()
	c := newConn(fromBrowser, toBrowser)
	var once sync.Once
	p := &process{conn: c, visible: visible, end: func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
			defer cancel()
			c.call(ctx, "", "Browser.close", nil, nil)
			toBrowser.Close()
			select {
			case <-exited:
			case <-ctx.Done():
				cmd.Process.Kill()
				<-exited
			}
		})
	}}
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	var version struct {
		UserAgent string `json:"userAgent"`
	}
	if err := c.call(ctx, "", "Browser.getVersion", nil, &version); err != nil {
		p.end()
		if why := stderr.String(); why != "" {
			return nil, "", fmt.Errorf("%s did not start: %s", program, why)
		}
		return nil, "", fmt.Errorf("%s did not start: %w", program, err)
	}
	var contexts struct {
		IDs []string `json:"browserContextIds"`
	}
	if err := p.call(ctx, "", "Target.getBrowserContexts", nil, &contexts); err != nil {
		p.end()
		return nil, "", err
	}
	// Only a unique blank page in the default context can be the page we
	// asked the browser to launch. Never discover arbitrary tabs later:
	// they may belong to another conversation or a peer.
	var targets struct {
		Infos []struct {
			ID      string `json:"targetId"`
			Type    string `json:"type"`
			URL     string `json:"url"`
			Context string `json:"browserContextId"`
			Opener  string `json:"openerId"`
		} `json:"targetInfos"`
	}
	if err := p.call(ctx, "", "Target.getTargets", nil, &targets); err != nil {
		p.end()
		return nil, "", err
	}
	for _, t := range targets.Infos {
		if t.Type == "page" && t.URL == "about:blank" && !slices.Contains(contexts.IDs, t.Context) && t.Opener == "" {
			if p.startup != "" {
				p.startup = "" // ambiguous: leave the pages alone
				break
			}
			p.startup = t.ID
		}
	}
	return p, version.UserAgent, nil
}

// tail keeps the last lines a browser wrote on its stderr, to say why it
// did not start.
type tail struct {
	mu sync.Mutex
	b  []byte
}

// tailSize is how much of its stderr is kept.
const tailSize = 1 << 10

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > tailSize {
		t.b = t.b[len(t.b)-tailSize:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(strings.Fields(string(bytes.ToValidUTF8(t.b, nil))), " ")
}
