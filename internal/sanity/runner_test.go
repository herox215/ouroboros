package sanity

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSummaryWriterJSON(t *testing.T) {
	var log bytes.Buffer
	w := &summaryWriter{file: &log, jsonLog: true}
	chunks := []string{"bad JSON\n{\"Action\":\"pass\"}\n{\"Action\":\"fa", "il\",\"Package\":\"p\",\"Test\":\"Broken\"}\n", "{\"Action\":\"skip\",\"Package\":\"p\",\"Test\":\"Optional\"}"}
	for _, s := range chunks {
		if n, err := w.Write([]byte(s)); n != len(s) || err != nil {
			t.Fatalf("Write=%d,%v", n, err)
		}
	}
	w.flush()
	w.flush()
	if got := w.summary.String(); got != "FAIL: p Broken\nSKIP: p Optional\n" {
		t.Fatalf("summary=%q", got)
	}
	if log.String() != strings.Join(chunks, "") {
		t.Fatal("log changed")
	}
	if w.entries != 2 || w.omitted != 0 || len(w.pending) != 0 {
		t.Fatal("incorrect counters or pending data")
	}
}

func TestSummaryWriterLimits(t *testing.T) {
	var log bytes.Buffer
	w := &summaryWriter{file: &log, jsonLog: true}
	var input strings.Builder
	for i := 0; i < 65; i++ {
		fmt.Fprintf(&input, "{\"Action\":\"fail\",\"Test\":\"Test%d\"}\n", i)
	}
	if _, err := w.Write([]byte(input.String())); err != nil {
		t.Fatal(err)
	}
	if w.entries != 60 || w.omitted != 5 || strings.Count(w.summary.String(), "FAIL:") != 60 {
		t.Fatalf("entries=%d omitted=%d", w.entries, w.omitted)
	}
	if strings.Contains(w.summary.String(), "Test60\n") {
		t.Fatal("excess entry retained")
	}
	giant := strings.Repeat("x", (1<<20)+1)
	if _, err := w.Write([]byte(giant)); err != nil {
		t.Fatal(err)
	}
	if len(w.pending) != 0 || w.tail != strings.Repeat("x", 8192) {
		t.Fatalf("pending=%d tail=%d", len(w.pending), len(w.tail))
	}
	if log.String() != input.String()+giant {
		t.Fatal("persisted log truncated")
	}
	if _, err := w.Write([]byte("\n{\"Action\":\"skip\"}\n")); err != nil {
		t.Fatal(err)
	}
	if w.omitted != 6 {
		t.Fatalf("omitted=%d", w.omitted)
	}
}

func TestRunCheckShell(t *testing.T) {
	for _, status := range []int{0, 7} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			dir := t.TempDir()
			exe := runnerShell(t, dir, fmt.Sprintf("printf 'cwd=%%s arg=%%s\\n' \"$PWD\" \"$1\"\nprintf 'stderr diagnostic\\n' >&2\nexit %d\n", status))
			log := filepath.Join(dir, "output.log")
			var report strings.Builder
			if failed := runCheck(context.Background(), dir, exe, []string{"with spaces"}, log, false, &report); failed != (status != 0) {
				t.Fatal(report.String())
			}
			runnerContains(t, runnerRead(t, log), "cwd="+dir+" arg=with spaces\n", "stderr diagnostic\n")
			if status != 0 {
				runnerContains(t, report.String(), "FAILED:", "exit status 7", "complete log: "+log, "stderr diagnostic", "arg=with spaces")
			} else {
				runnerContains(t, report.String(), "PASS\n")
			}
		})
	}
}

func TestRunCheckOmittedEntries(t *testing.T) {
	dir := t.TempDir()
	exe := runnerShell(t, dir, "i=0\nwhile [ $i -lt 62 ]; do printf '{\"Action\":\"skip\",\"Package\":\"p\",\"Test\":\"T%d\"}\\n' $i; i=$((i+1)); done\n")
	log := filepath.Join(dir, "output.json")
	var report strings.Builder
	if runCheck(context.Background(), dir, exe, nil, log, true, &report) {
		t.Fatal(report.String())
	}
	runnerContains(t, report.String(), "SKIP: p T59\n", "2 additional failure/skip entries in "+log+"\n", "PASS\n")
	if strings.Contains(report.String(), "T60") {
		t.Fatal(report.String())
	}
}

func TestRunCheckLogError(t *testing.T) {
	dir := t.TempDir()
	var report strings.Builder
	if !runCheck(context.Background(), dir, "/must/not/run", nil, filepath.Join(dir, "absent", "log"), false, &report) {
		t.Fatal("log failure reported success")
	}
	runnerContains(t, report.String(), "Cannot create log:")
}

func TestRunFakeGo(t *testing.T) {
	for _, status := range []int{0, 1} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			source := runnerSource(t)
			t.Setenv("TMPDIR", t.TempDir())
			action := "pass"
			if status != 0 {
				action = "fail"
			}
			// Shift to the known coverprofile argument, using only POSIX shell builtins.
			body := "printf 'invoked\\n' >> calls\nprintf '%%s\\n' \"$@\" > args\nshift 6\nprofile=$1\nprofile=REPLACE\n" +
				"printf 'mode: atomic\\nexample/p.go:1.1,2.2 3 1\\nexample/p.go:4.1,5.2 1 0\\n' > \"$profile\"\n" +
				"printf '%%s\\n' '{\"Action\":\"skip\",\"Package\":\"example\",\"Test\":\"Optional\"}' '{\"Action\":\"%s\",\"Package\":\"example\",\"Test\":\"Main\"}'\nexit %d\n"
			body = strings.ReplaceAll(body, "REPLACE", "$"+"{profile#-coverprofile=}")
			exe := runnerShell(t, source, fmt.Sprintf(body, action, status))
			gui := false
			report, err := Run(context.Background(), source, exe, Options{GUI: &gui})
			if err != nil {
				t.Fatal(err)
			}
			runnerContains(t, report, "SKIP: example Optional", "Statement coverage: 75.0%", "example/p.go:4-5", "explicitly skipped (gui=false)")
			if status != 0 {
				runnerContains(t, report, "FAIL: example Main", "Coverage is partial", "RESULT: problems found")
			} else {
				runnerContains(t, report, "RESULT: selected test commands passed")
				if strings.Contains(report, "Coverage is partial") {
					t.Fatal(report)
				}
			}
			if got := runnerRead(t, filepath.Join(source, "calls")); got != "invoked\n" {
				t.Fatalf("calls=%q", got)
			}
			args := strings.Split(strings.TrimSuffix(runnerRead(t, filepath.Join(source, "args")), "\n"), "\n")
			if len(args) != 8 {
				t.Fatalf("args=%q", args)
			}
			for i, want := range []string{"test", "-json", "-count=1", "-race", "-timeout=5m", "-coverpkg=./..."} {
				if args[i] != want {
					t.Fatalf("arg[%d]=%q", i, args[i])
				}
			}
			if !strings.HasPrefix(args[6], "-coverprofile=") || args[7] != "./..." {
				t.Fatalf("args=%q", args)
			}
			profile := strings.TrimPrefix(args[6], "-coverprofile=")
			runnerContains(t, runnerRead(t, profile), "mode: atomic", "example/p.go")
			dir := filepath.Dir(profile)
			runnerContains(t, report, "Full logs and coverage: "+dir)
			runnerContains(t, runnerRead(t, filepath.Join(dir, "go-tests.json")), "Optional", "Main")
			for _, name := range []string{"gui-tests.json", "frontend.log"} {
				if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Fatalf("unexpected %s: %v", name, err)
				}
			}
		})
	}
}

func TestRunInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		minutes      int
		want         string
	}{
		{"empty source", "", 0, "source checkout"},
		{"negative timeout", t.TempDir(), -1, "timeout_minutes"},
		{"excessive timeout", t.TempDir(), 61, "timeout_minutes"},
		{"missing module", t.TempDir(), 1, "source checkout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := Run(context.Background(), tc.source, "/must/not/run", Options{TimeoutMinutes: tc.minutes})
			if err == nil || !strings.Contains(err.Error(), tc.want) || report != "" {
				t.Fatalf("Run=%q,%v", report, err)
			}
		})
	}
}

func TestRunCancelledContext(t *testing.T) {
	source := runnerSource(t)
	t.Setenv("TMPDIR", t.TempDir())
	exe := runnerShell(t, source, "printf unexpected > invoked\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := Run(ctx, source, exe, Options{})
	if err != nil {
		t.Fatal(err)
	}
	runnerContains(t, report, "INCOMPLETE: context canceled", "later checks were not run")
	for _, s := range []string{"RESULT:", "Coverage unavailable:", "Statement coverage:", "internal/plugins/gui"} {
		if strings.Contains(report, s) {
			t.Fatalf("unexpected %q in %s", s, report)
		}
	}
	if _, err := os.Stat(filepath.Join(source, "invoked")); !os.IsNotExist(err) {
		t.Fatalf("cancelled command ran: %v", err)
	}
}

func TestRunWithGUI(t *testing.T) {
	yes := true
	for _, tc := range []struct {
		name             string
		gui              *bool
		failGo, failNode bool
		result           string
	}{
		{"by default", nil, false, false, "RESULT: selected test commands passed"},
		{"asked for", &yes, false, false, "RESULT: selected test commands passed"},
		{"after failed tests", nil, true, false, "RESULT: problems found"},
		{"frontend fails", nil, false, true, "RESULT: problems found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := runnerSource(t)
			t.Setenv("TMPDIR", t.TempDir())
			// Go is called by its absolute path and leads PATH for the tests
			// that call Go themselves; node is looked up in PATH.
			toolchain, tools := t.TempDir(), t.TempDir()
			t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
			goBody := "printf '%s\\n' \"$*\" >> calls\nprintf '%s\\n' \"$PATH\" > path\n" +
				"for a in \"$@\"; do case $a in -coverprofile=*) printf 'mode: set\\nexample/p.go:1.1,2.2 1 1\\n' > \"${a#-coverprofile=}\";; esac; done\n"
			if tc.failGo {
				goBody += "case \"$*\" in *./...) exit 1;; esac\n"
			}
			nodeBody := "printf 'node %s\\n' \"$*\" >> calls\n"
			if tc.failNode {
				nodeBody += "exit 3\n"
			}
			goTool := runnerScript(t, filepath.Join(toolchain, "go"), goBody)
			runnerScript(t, filepath.Join(tools, "node"), nodeBody)
			report, err := Run(context.Background(), source, goTool, Options{GUI: tc.gui})
			if err != nil {
				t.Fatal(err)
			}
			gui := goTool + " test -json -count=1 -race -timeout=5m -tags=production,webkit2_41 ./internal/plugins/gui"
			frontend := "node --test internal/plugins/gui/frontend_test.mjs"
			runnerContains(t, report, "$ "+gui+"\n", "$ "+frontend+"\n", "Statement coverage: 100.0%", tc.result)
			calls := strings.Split(runnerRead(t, filepath.Join(source, "calls")), "\n")
			if len(calls) != 4 || !strings.HasSuffix(calls[0], " ./...") || calls[1] != strings.TrimPrefix(gui, goTool+" ") || calls[2] != frontend {
				t.Fatalf("calls=%q", calls)
			}
			if path := runnerRead(t, filepath.Join(source, "path")); !strings.HasPrefix(path, toolchain+string(os.PathListSeparator)+tools+string(os.PathListSeparator)) {
				t.Fatalf("PATH of go: %s", path)
			}
			if strings.Contains(report, "explicitly skipped") {
				t.Fatal(report)
			}
		})
	}
}

func TestRunWithoutCoverage(t *testing.T) {
	source := runnerSource(t)
	t.Setenv("TMPDIR", t.TempDir())
	exe := runnerShell(t, source, "exit 0\n")
	gui := false
	report, err := Run(context.Background(), source, exe, Options{GUI: &gui})
	if err != nil {
		t.Fatal(err)
	}
	runnerContains(t, report, "PASS\n", "Coverage unavailable: read coverage profile:", "Coverage is partial", "RESULT: problems found")
}

func TestRunWithoutTemporaryDirectory(t *testing.T) {
	source := runnerSource(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if report, err := Run(context.Background(), source, "/must/not/run", Options{}); err == nil || report != "" {
		t.Fatalf("Run=%q,%v", report, err)
	}
}

func TestRunCheckCancelEndsGroup(t *testing.T) {
	dir := t.TempDir()
	// What the command starts in the background holds a FIFO open while it
	// runs: reading the FIFO ends once nothing of the group is left.
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() {
		f, err := os.Open(fifo)
		if err == nil {
			_, err = io.ReadAll(f)
			f.Close()
		}
		closed <- err
	}()
	exe := runnerShell(t, dir, "sh -c 'echo > started.new; mv started.new started; exec sleep 60' 3>fifo &\nwait\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var report strings.Builder
	done := make(chan bool)
	go func() { done <- runCheck(ctx, dir, exe, nil, filepath.Join(dir, "log"), false, &report) }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the command did not start its background process")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if !<-done {
		t.Fatal("a cancelled check passed")
	}
	// The shell may itself be killed, or reap its killed child and exit
	// successfully before the signal reaches it. CommandContext reports the
	// context error in the latter case; both outcomes are cancellation.
	if !strings.Contains(report.String(), "FAILED: signal: killed\n") && !strings.Contains(report.String(), "FAILED: context canceled\n") {
		t.Fatalf("unexpected cancellation report:\n%s", report.String())
	}
	// The background process is in the group and ends with it, long before
	// its minute is up.
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the background process still runs")
	}
}

func runnerSource(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func runnerShell(t *testing.T, dir, body string) string {
	t.Helper()
	return runnerScript(t, filepath.Join(dir, "fake-command"), body)
}
func runnerScript(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
func runnerRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func runnerContains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
