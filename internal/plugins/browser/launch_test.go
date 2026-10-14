package browser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// answering is a browser in bash: it answers every command with an empty
// result on its fd 4, but Browser.getVersion with the user agent $FAKE_UA,
// and ends when asked to close. It writes its arguments to the file args
// next to it, and adds them to the file starts as a line. With
// $FAKE_NO_AGENT set it does not start as a user agent.
const answering = `#!/bin/bash
printf '%s\n' "$@" > "$(dirname "$0")/args"
echo "$*" >> "$(dirname "$0")/starts"
[ -n "$FAKE_NO_AGENT" ] && [[ "$*" == *--user-agent=* ]] && exit 1
while IFS= read -r -d '' -u 3 msg; do
  id=${msg#*\"id\":}; id=${id%%,*}; id=${id%%\}*}
  case $msg in
    *Browser.getVersion*) printf '{"id":%s,"result":{"userAgent":"%s"}}\0' "$id" "$FAKE_UA" >&4;;
    *) printf '{"id":%s,"result":{}}\0' "$id" >&4;;
  esac
  case $msg in *Browser.close*) [ -z "$STUBBORN" ] && exit 0;; esac
done
[ -n "$STUBBORN" ] && exec sleep 60
`

// program writes a program with the text script and makes it the browser
// that is looked for.
func program(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "chromium")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	old := programs
	programs = map[string][]string{"": {filepath.Join(t.TempDir(), "missing"), path}}
	t.Cleanup(func() { programs = old })
	return path
}

// A browser is started with the profile, without a window unless asked,
// connected through its pipe, and ended.
func TestLaunch(t *testing.T) {
	path := program(t, answering)
	profile := filepath.Join(t.TempDir(), "browser")
	ctx := context.Background()
	for _, visible := range []bool{false, true} {
		p, err := launch(ctx, profile, visible)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.call(ctx, "", "Target.getTargets", nil, nil); err != nil {
			t.Fatal(err)
		}
		p.end()
		if !p.closed() || p.visible != visible {
			t.Fatalf("closed %v, visible %v", p.closed(), p.visible)
		}
		args, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "args"))
		if !strings.Contains(string(args), "--user-data-dir="+profile+"\n") || strings.Contains(string(args), "--headless") == visible {
			t.Fatalf("started with %s", args)
		}
	}
	if info, err := os.Stat(profile); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the profile: %v, %v", info, err)
	}
}

// A browser without a window does not say so in its user agent, nor any
// browser that it is automated; it is started once more to learn its user
// agent, once per program.
func TestLaunchAsAPerson(t *testing.T) {
	path := program(t, answering)
	t.Setenv("FAKE_UA", "Mozilla/5.0 (X11; Linux x86_64) HeadlessChrome/152.0.0.0 Safari/537.36")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "de_DE.UTF-8")
	ctx := context.Background()
	starts := func() []string {
		t.Helper()
		b, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "starts"))
		os.Remove(filepath.Join(filepath.Dir(path), "starts"))
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	for range 2 {
		p, err := launch(ctx, t.TempDir(), false)
		if err != nil {
			t.Fatal(err)
		}
		p.end()
	}
	got := starts()
	if len(got) != 3 || strings.Contains(got[0], "--user-agent") {
		t.Fatalf("started %q", got)
	}
	for _, start := range got[1:] {
		for _, want := range []string{"--user-agent=Mozilla/5.0 (X11; Linux x86_64) Chrome/152.0.0.0 Safari/537.36", "--disable-blink-features=AutomationControlled",
			"--lang=de-DE --accept-lang=de-DE,de,en-US,en", "--headless"} {
			if !strings.Contains(start, want) {
				t.Errorf("%q lacks %q", start, want)
			}
		}
	}

	// A browser with a window says what it is; then without one it starts
	// as that at once.
	path = program(t, answering)
	t.Setenv("FAKE_UA", "Mozilla/5.0 (X11; Linux x86_64) Chrome/151.0.0.0 Safari/537.36")
	for _, visible := range []bool{true, false} {
		p, err := launch(ctx, t.TempDir(), visible)
		if err != nil {
			t.Fatal(err)
		}
		p.end()
	}
	if got := starts(); len(got) != 2 || strings.Contains(got[0], "--user-agent") || !strings.Contains(got[1], "--user-agent=Mozilla/5.0 (X11; Linux x86_64) Chrome/151.0.0.0 Safari/537.36") {
		t.Fatalf("started %q", got)
	}

	// A browser that does not start the second time.
	program(t, answering)
	t.Setenv("FAKE_UA", "HeadlessChrome/150")
	t.Setenv("FAKE_NO_AGENT", "1")
	if _, err := launch(ctx, t.TempDir(), false); err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("started again: %v", err)
	}
}

// The languages a browser speaks are those of the user.
func TestLanguages(t *testing.T) {
	for _, c := range []struct{ all, messages, lang, want string }{
		{"", "", "de_DE.UTF-8", "--lang=de-DE --accept-lang=de-DE,de,en-US,en"},
		{"fr_FR@euro", "", "de_DE.UTF-8", "--lang=fr-FR --accept-lang=fr-FR,fr,en-US,en"},
		{"", "en_GB.UTF-8", "de_DE", "--lang=en-GB --accept-lang=en-GB,en"},
		{"", "", "en", "--lang=en --accept-lang=en"},
		{"", "", "eo", "--lang=eo --accept-lang=eo,en-US,en"},
		{"", "", "C.UTF-8", ""},
		{"POSIX", "", "", ""},
		{"", "", "", ""},
	} {
		t.Setenv("LC_ALL", c.all)
		t.Setenv("LC_MESSAGES", c.messages)
		t.Setenv("LANG", c.lang)
		if got := strings.Join(languages(), " "); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

// A browser that does not end when asked is killed.
func TestLaunchKills(t *testing.T) {
	program(t, answering)
	t.Setenv("STUBBORN", "1")
	closeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { closeTimeout = 3 * time.Second })
	p, err := launch(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	p.end()
	p.end() // once is enough
	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("took %v", took)
	}
}

// What keeps a browser from starting.
func TestLaunchFails(t *testing.T) {
	ctx := context.Background()
	old := programs
	programs = map[string][]string{"": {filepath.Join(t.TempDir(), "missing")}}
	_, err := launch(ctx, t.TempDir(), false)
	programs = old
	if err == nil || err.Error() != "no browser found: install Chromium or Google Chrome" {
		t.Fatalf("none: %v", err)
	}

	program(t, answering)
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	if _, err := launch(ctx, filepath.Join(file, "browser"), false); err == nil {
		t.Fatal("a profile under a file")
	}

	program(t, "#!/nowhere/bash\n")
	if _, err := launch(ctx, t.TempDir(), false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a program that cannot run: %v", err)
	}

	path := program(t, "#!/bin/bash\necho 'the profile is in use' >&2\nexit 1\n")
	if _, err := launch(ctx, t.TempDir(), false); err == nil || err.Error() != path+" did not start: the profile is in use" {
		t.Fatalf("a browser that ends: %v", err)
	}
	path = program(t, "#!/bin/bash\nexit 1\n")
	if _, err := launch(ctx, t.TempDir(), false); err == nil || err.Error() != path+" did not start: Browser.getVersion: the browser has closed" && err.Error() != path+" did not start: the browser has closed" {
		t.Fatalf("a browser that ends silently: %v", err)
	}
}

// Of what a browser writes on its stderr the end is kept.
func TestTail(t *testing.T) {
	var tl tail
	tl.Write([]byte(strings.Repeat("x", tailSize)))
	tl.Write([]byte("\n  the end\xff"))
	if got := tl.String(); got != strings.Repeat("x", tailSize-11)+" the end" {
		t.Fatalf("kept %d bytes ending %q", len(got), got[len(got)-20:])
	}
}
